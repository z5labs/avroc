// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"strconv"

	"github.com/z5labs/avroc/avrocpb"
)

// writeMethodFor returns the BinaryWriter method for a reference to an Avro
// primitive. A reference to a named type has none: it delegates to the named
// type's own MarshalAvroBinary.
func writeMethodFor(ref *avrocpb.Reference) (string, bool) {
	if ref == nil || ref.GetKind() != avrocpb.TypeRefKind_TYPE_REF_KIND_PRIMITIVE {
		return "", false
	}
	method, ok := primitiveWriteMethod[ref.GetName()]
	return method, ok
}

// fixedTypes is the set of full names a descriptor defines as a fixed.
//
// A reference says only that it names a type, not what kind of type, and a
// fixed is the one named type MarshalAvroBinary writes differently: inline, as
// w.WriteFixed over the value where it already is, wherever that value is
// addressable (#245). Going through the fixed's own value-receiver method
// instead hands x[:] on to an io.Writer, which moves the array to the heap —
// one allocation per fixed per encode. So the kind of a named reference is read
// from the definition the descriptor carries for that full name, which is
// walking the tree rather than re-deriving resolution (docs/ir/SPEC.md).
type fixedTypes map[string]bool

// collectFixedTypes indexes every fixed defined anywhere in the descriptor's
// schemas. It is the whole descriptor rather than one schema because a named
// type is written out in full at its first use and only referenced after it, so
// a later schema's reference can name a fixed an earlier schema defines — and
// both are generated into the same package.
func collectFixedTypes(schemas []*avrocpb.Schema) fixedTypes {
	fixed := fixedTypes{}
	for _, schema := range schemas {
		fixed.collect(schema.GetType())
		for _, t := range schema.GetTypes() {
			fixed.collect(t)
		}
	}
	return fixed
}

func (fixed fixedTypes) collect(t *avrocpb.Type) {
	switch v := t.GetType().(type) {
	case *avrocpb.Type_Fixed:
		fixed[v.Fixed.GetFullName()] = true
	case *avrocpb.Type_Record:
		for _, f := range v.Record.GetFields() {
			fixed.collect(f.GetType())
		}
	case *avrocpb.Type_Array:
		fixed.collect(v.Array.GetItems())
	case *avrocpb.Type_MapType:
		fixed.collect(v.MapType.GetValues())
	case *avrocpb.Type_Union:
		for _, branch := range v.Union.GetTypes() {
			fixed.collect(branch)
		}
	}
}

// isFixed reports whether t is a fixed: one defined in full right there, or a
// reference to one defined elsewhere in the descriptor.
func (fixed fixedTypes) isFixed(t *avrocpb.Type) bool {
	switch v := t.GetType().(type) {
	case *avrocpb.Type_Fixed:
		return true
	case *avrocpb.Type_Reference:
		ref := v.Reference
		return ref.GetKind() == avrocpb.TypeRefKind_TYPE_REF_KIND_NAMED && fixed[ref.GetName()]
	}
	return false
}

// generateFixedWrite writes a fixed value inline. varName must be addressable —
// a record field or a slice element — because slicing an array requires it.
//
// The slice points into the value where it already is, so it is the record or
// the slice's backing array that escapes to the writer rather than a copy of
// the fixed. A record already on the heap — reached through a pointer, as
// encoding one usually is — therefore costs nothing; one that would otherwise
// have lived on its caller's stack is moved to the heap once, in place of the
// one copy per fixed per encode the method made.
func generateFixedWrite(cb *codeBuilder, varName string, indent string) {
	cb.writef("%serr = w.WriteFixed(%s[:])\n", indent, varName)
	cb.writef("%sif err != nil {\n", indent)
	cb.writef("%s\treturn err\n", indent)
	cb.writef("%s}\n", indent)
}

// generateMarshalMethod generates the MarshalAvroBinary method for a type.
func generateMarshalMethod(cb *codeBuilder, ttg typeToGenerate, fixed fixedTypes) {
	t := ttg.typ

	switch v := t.Type.(type) {
	case *avrocpb.Type_Record:
		generateRecordMarshal(cb, v.Record, fixed)
	case *avrocpb.Type_EnumType:
		generateEnumMarshal(cb, v.EnumType)
	case *avrocpb.Type_Fixed:
		generateFixedMarshal(cb, v.Fixed)
	case *avrocpb.Type_Union:
		generateUnionMarshal(cb, v.Union, ttg.recordName, ttg.fieldName, fixed)
	}
}

// generateRecordMarshal generates the MarshalAvroBinary method for a record type.
func generateRecordMarshal(cb *codeBuilder, r *avrocpb.Record, fixed fixedTypes) {
	name := toPascalCase(r.GetName())

	cb.newline()
	cb.writef("func (x *%s) MarshalAvroBinary(w *avro.BinaryWriter) error {\n", name)

	fields := r.GetFields()
	if len(fields) == 0 {
		cb.writeln("\treturn nil")
		cb.writeln("}")
		return
	}

	cb.writeln("\tvar err error")

	for _, f := range fields {
		generateFieldWrite(cb, f, fixed)
	}

	cb.writeln("\treturn nil")
	cb.writeln("}")
}

// generateFieldWrite generates the write code for a single field.
//
// A record field is addressable — x is the record's pointer receiver — so a
// fixed one is written inline rather than through its method.
func generateFieldWrite(cb *codeBuilder, f *avrocpb.Field, fixed fixedTypes) {
	fieldName := toFieldName(f.GetName())
	fieldType := f.GetType()

	if fieldType == nil {
		return
	}

	if fixed.isFixed(fieldType) {
		generateFixedWrite(cb, "x."+fieldName, "\t")
		return
	}

	switch v := fieldType.Type.(type) {
	case *avrocpb.Type_Reference:
		generateReferenceWrite(cb, v.Reference, fieldName)
	case *avrocpb.Type_Array:
		generateArrayFieldWrite(cb, v.Array, fieldName, fixed)
	case *avrocpb.Type_MapType:
		generateMapFieldWrite(cb, v.MapType, fieldName, fixed)
	case *avrocpb.Type_Union:
		cb.writef("\terr = x.%s.MarshalAvroBinary(w)\n", fieldName)
		cb.writeln("\tif err != nil {")
		cb.writeln("\t\treturn err")
		cb.writeln("\t}")
	case *avrocpb.Type_Record, *avrocpb.Type_EnumType:
		cb.writef("\terr = x.%s.MarshalAvroBinary(w)\n", fieldName)
		cb.writeln("\tif err != nil {")
		cb.writeln("\t\treturn err")
		cb.writeln("\t}")
	}
}

// generateReferenceWrite generates write code for a resolved reference.
func generateReferenceWrite(cb *codeBuilder, ref *avrocpb.Reference, fieldName string) {
	if method, ok := writeMethodFor(ref); ok {
		cb.writef("\terr = w.%s(x.%s)\n", method, fieldName)
		cb.writeln("\tif err != nil {")
		cb.writeln("\t\treturn err")
		cb.writeln("\t}")
	} else {
		// Named type reference - delegate to its MarshalAvroBinary
		cb.writef("\terr = x.%s.MarshalAvroBinary(w)\n", fieldName)
		cb.writeln("\tif err != nil {")
		cb.writeln("\t\treturn err")
		cb.writeln("\t}")
	}
}

// generateArrayFieldWrite generates write code for an array field.
func generateArrayFieldWrite(cb *codeBuilder, arr *avrocpb.Array, fieldName string, fixed fixedTypes) {
	cb.writef("\tif len(x.%s) > 0 {\n", fieldName)
	cb.writef("\t\terr = w.WriteLong(int64(len(x.%s)))\n", fieldName)
	cb.writeln("\t\tif err != nil {")
	cb.writeln("\t\t\treturn err")
	cb.writeln("\t\t}")
	cb.writef("\t\tfor i := range x.%s {\n", fieldName)

	// Generate write for each element using index to ensure addressability
	if itemType := arr.GetItems(); itemType != nil {
		generateItemWrite(cb, itemType, "x."+fieldName+"[i]", "\t\t\t", 0, fixed)
	}

	cb.writeln("\t\t}")
	cb.writeln("\t}")
	cb.writeln("\terr = w.WriteLong(0)")
	cb.writeln("\tif err != nil {")
	cb.writeln("\t\treturn err")
	cb.writeln("\t}")
}

// generateMapFieldWrite generates write code for a map field.
func generateMapFieldWrite(cb *codeBuilder, m *avrocpb.Map, fieldName string, fixed fixedTypes) {
	cb.writef("\tif len(x.%s) > 0 {\n", fieldName)
	cb.writef("\t\terr = w.WriteLong(int64(len(x.%s)))\n", fieldName)
	cb.writeln("\t\tif err != nil {")
	cb.writeln("\t\t\treturn err")
	cb.writeln("\t\t}")
	cb.writef("\t\tfor k := range x.%s {\n", fieldName)
	cb.writeln("\t\t\terr = w.WriteString(k)")
	cb.writeln("\t\t\tif err != nil {")
	cb.writeln("\t\t\t\treturn err")
	cb.writeln("\t\t\t}")

	// Generate write for map value using index access for addressability
	valueType := m.GetValues()
	if valueType != nil {
		generateValueWrite(cb, valueType, "x."+fieldName+"[k]", "\t\t\t", 0, fixed)
	}

	cb.writeln("\t\t}")
	cb.writeln("\t}")
	cb.writeln("\terr = w.WriteLong(0)")
	cb.writeln("\tif err != nil {")
	cb.writeln("\t\treturn err")
	cb.writeln("\t}")
}

// generateItemWrite generates write code for an array item or similar value.
//
// varName is an addressable expression — a slice element — so a named type's
// pointer-receiver MarshalAvroBinary is called on it directly. depth is how
// many containers deep varName already is beneath the outermost one, and is
// what keeps a nested container's loop variable from shadowing the loop it is
// inside (#237).
//
// Being addressable is also what lets a fixed item be written inline, at any
// depth: a slice element is addressable even when the slice is itself a map's
// value.
func generateItemWrite(cb *codeBuilder, t *avrocpb.Type, varName string, indent string, depth int, fixed fixedTypes) {
	if t == nil {
		return
	}

	if fixed.isFixed(t) {
		generateFixedWrite(cb, varName, indent)
		return
	}

	switch v := t.Type.(type) {
	case *avrocpb.Type_Reference:
		generateValueWrite(cb, t, varName, indent, depth, fixed)
	case *avrocpb.Type_Record, *avrocpb.Type_EnumType, *avrocpb.Type_Union:
		// A union is the field's union interface, which writes its own branch
		// index.
		cb.writef("%serr = %s.MarshalAvroBinary(w)\n", indent, varName)
		cb.writef("%sif err != nil {\n", indent)
		cb.writef("%s\treturn err\n", indent)
		cb.writef("%s}\n", indent)
	case *avrocpb.Type_Array:
		generateNestedArrayWrite(cb, v.Array, varName, indent, depth+1, fixed)
	case *avrocpb.Type_MapType:
		generateNestedMapWrite(cb, v.MapType, varName, indent, depth+1, fixed)
	}
}

// generateValueWrite generates write code for a value in a non-addressable
// position (e.g. a map index), so for named types with pointer receivers we
// copy to a local variable first. An array or a map there needs no copy: it is
// only measured and indexed, never called a method on.
//
// A fixed here is written through its method like any other named type: a map
// value is not addressable, so it cannot be sliced where it is, and the copy it
// would take to make it so escapes exactly as the method's receiver does.
// Removing that allocation would mean changing the generated Go types (#245).
func generateValueWrite(cb *codeBuilder, t *avrocpb.Type, varName string, indent string, depth int, fixed fixedTypes) {
	switch v := t.GetType().(type) {
	case *avrocpb.Type_Array:
		generateNestedArrayWrite(cb, v.Array, varName, indent, depth+1, fixed)
		return
	case *avrocpb.Type_MapType:
		generateNestedMapWrite(cb, v.MapType, varName, indent, depth+1, fixed)
		return
	}

	if method, ok := writeMethodFor(t.GetReference()); ok {
		cb.writef("%serr = w.%s(%s)\n", indent, method, varName)
		cb.writef("%sif err != nil {\n", indent)
		cb.writef("%s\treturn err\n", indent)
		cb.writef("%s}\n", indent)
	} else {
		// Named type reference. Use a temporary variable to ensure addressability
		// for pointer-receiver methods (e.g., when varName is a map index).
		cb.writef("%s{\n", indent)
		cb.writef("%s\ttmp := %s\n", indent, varName)
		cb.writef("%s\terr = tmp.MarshalAvroBinary(w)\n", indent)
		cb.writef("%s\tif err != nil {\n", indent)
		cb.writef("%s\t\treturn err\n", indent)
		cb.writef("%s\t}\n", indent)
		cb.writef("%s}\n", indent)
	}
}

// generateNestedArrayWrite generates write code for an array that is itself an
// array's item or a map's value: the same block sequence a top-level array
// field is written as, over varName, with a loop variable of its own depth.
func generateNestedArrayWrite(cb *codeBuilder, arr *avrocpb.Array, varName string, indent string, depth int, fixed fixedTypes) {
	i := depthName("i", depth)

	generateBlockCountWrite(cb, varName, indent)
	cb.writef("%s\tfor %s := range %s {\n", indent, i, varName)
	if itemType := arr.GetItems(); itemType != nil {
		generateItemWrite(cb, itemType, varName+"["+i+"]", indent+"\t\t", depth, fixed)
	}
	cb.writef("%s\t}\n", indent)
	cb.writef("%s}\n", indent)
	generateBlockEndWrite(cb, indent)
}

// generateNestedMapWrite generates write code for a map that is itself an
// array's item or a map's value, as generateNestedArrayWrite does for an
// array.
func generateNestedMapWrite(cb *codeBuilder, m *avrocpb.Map, varName string, indent string, depth int, fixed fixedTypes) {
	k := depthName("k", depth)

	generateBlockCountWrite(cb, varName, indent)
	cb.writef("%s\tfor %s := range %s {\n", indent, k, varName)
	cb.writef("%s\t\terr = w.WriteString(%s)\n", indent, k)
	cb.writef("%s\t\tif err != nil {\n", indent)
	cb.writef("%s\t\t\treturn err\n", indent)
	cb.writef("%s\t\t}\n", indent)
	if valueType := m.GetValues(); valueType != nil {
		generateValueWrite(cb, valueType, varName+"["+k+"]", indent+"\t\t", depth, fixed)
	}
	cb.writef("%s\t}\n", indent)
	cb.writef("%s}\n", indent)
	generateBlockEndWrite(cb, indent)
}

// generateBlockCountWrite opens a nested container's single block: the
// container is written as one block of len(varName) items, and as no block at
// all when it is empty. The caller closes the if it opens.
func generateBlockCountWrite(cb *codeBuilder, varName string, indent string) {
	cb.writef("%sif len(%s) > 0 {\n", indent, varName)
	cb.writef("%s\terr = w.WriteLong(int64(len(%s)))\n", indent, varName)
	cb.writef("%s\tif err != nil {\n", indent)
	cb.writef("%s\t\treturn err\n", indent)
	cb.writef("%s\t}\n", indent)
}

// generateBlockEndWrite writes the zero-count block that ends a nested
// container.
func generateBlockEndWrite(cb *codeBuilder, indent string) {
	cb.writef("%serr = w.WriteLong(0)\n", indent)
	cb.writef("%sif err != nil {\n", indent)
	cb.writef("%s\treturn err\n", indent)
	cb.writef("%s}\n", indent)
}

// depthName is the name of a generated local at a container depth: base itself
// for the outermost container, whose names generated code has always used, and
// base suffixed with the depth beneath it, so that a nested loop never shadows
// the variable of the loop it is inside.
func depthName(base string, depth int) string {
	if depth == 0 {
		return base
	}
	return base + strconv.Itoa(depth)
}

// generateEnumMarshal generates the MarshalAvroBinary method for an enum type.
func generateEnumMarshal(cb *codeBuilder, e *avrocpb.Enum) {
	name := toPascalCase(e.GetName())

	cb.newline()
	cb.writef("func (x %s) MarshalAvroBinary(w *avro.BinaryWriter) error {\n", name)
	cb.writeln("\treturn w.WriteInt(int32(x))")
	cb.writeln("}")
}

// generateFixedMarshal generates the MarshalAvroBinary method for a fixed type.
func generateFixedMarshal(cb *codeBuilder, f *avrocpb.Fixed) {
	name := toPascalCase(f.GetName())

	cb.newline()
	cb.writef("func (x %s) MarshalAvroBinary(w *avro.BinaryWriter) error {\n", name)
	cb.writeln("\treturn w.WriteFixed(x[:])")
	cb.writeln("}")
}

// generateUnionMarshal generates the MarshalAvroBinary methods for union member types.
func generateUnionMarshal(cb *codeBuilder, u *avrocpb.Union, recordName string, fieldName string, fixed fixedTypes) {
	unionName := unionTypeName(recordName, fieldName)

	for i, t := range u.Types {
		generateUnionMemberMarshal(cb, unionName, t, i, fixed)
	}
}

// generateUnionMemberMarshal generates the MarshalAvroBinary method for a union member.
//
// A fixed branch is still written through its method: the member's receiver
// is a copy, and slicing a copy moves it to the heap just as the method does,
// so writing it inline would buy nothing short of changing the generated Go
// types (#245). Items of an array branch are slice elements, and those are
// written inline like any other array's.
func generateUnionMemberMarshal(cb *codeBuilder, unionName string, t *avrocpb.Type, index int, fixed fixedTypes) {
	memberName := unionMemberName(unionName, t)

	cb.newline()
	cb.writef("func (x %s) MarshalAvroBinary(w *avro.BinaryWriter) error {\n", memberName)

	if isNullType(t) {
		// Null member: just write the index
		cb.writef("\treturn w.WriteLong(%d)\n", index)
	} else {
		cb.writeln("\tvar err error")
		cb.writef("\terr = w.WriteLong(%d)\n", index)
		cb.writeln("\tif err != nil {")
		cb.writeln("\t\treturn err")
		cb.writeln("\t}")

		// Write the value based on type
		switch v := t.Type.(type) {
		case *avrocpb.Type_Reference:
			if method, ok := writeMethodFor(v.Reference); ok {
				cb.writef("\terr = w.%s(x.Value)\n", method)
				cb.writeln("\tif err != nil {")
				cb.writeln("\t\treturn err")
				cb.writeln("\t}")
			} else {
				// Named type reference
				cb.writeln("\terr = x.Value.MarshalAvroBinary(w)")
				cb.writeln("\tif err != nil {")
				cb.writeln("\t\treturn err")
				cb.writeln("\t}")
			}
		case *avrocpb.Type_Record, *avrocpb.Type_EnumType:
			cb.writeln("\terr = x.Value.MarshalAvroBinary(w)")
			cb.writeln("\tif err != nil {")
			cb.writeln("\t\treturn err")
			cb.writeln("\t}")
		case *avrocpb.Type_Array:
			generateUnionArrayMarshal(cb, v.Array, fixed)
		case *avrocpb.Type_MapType:
			generateUnionMapMarshal(cb, v.MapType, fixed)
		case *avrocpb.Type_Fixed:
			cb.writeln("\terr = x.Value.MarshalAvroBinary(w)")
			cb.writeln("\tif err != nil {")
			cb.writeln("\t\treturn err")
			cb.writeln("\t}")
		}
		cb.writeln("\treturn nil")
	}

	cb.writeln("}")
}

// generateUnionArrayMarshal generates array marshal code for a union member.
func generateUnionArrayMarshal(cb *codeBuilder, arr *avrocpb.Array, fixed fixedTypes) {
	cb.writeln("\tif len(x.Value) > 0 {")
	cb.writeln("\t\terr = w.WriteLong(int64(len(x.Value)))")
	cb.writeln("\t\tif err != nil {")
	cb.writeln("\t\t\treturn err")
	cb.writeln("\t\t}")
	cb.writeln("\t\tfor i := range x.Value {")

	itemType := arr.GetItems()
	if itemType != nil {
		generateItemWrite(cb, itemType, "x.Value[i]", "\t\t\t", 0, fixed)
	}

	cb.writeln("\t\t}")
	cb.writeln("\t}")
	cb.writeln("\terr = w.WriteLong(0)")
	cb.writeln("\tif err != nil {")
	cb.writeln("\t\treturn err")
	cb.writeln("\t}")
}

// generateUnionMapMarshal generates map marshal code for a union member.
func generateUnionMapMarshal(cb *codeBuilder, m *avrocpb.Map, fixed fixedTypes) {
	cb.writeln("\tif len(x.Value) > 0 {")
	cb.writeln("\t\terr = w.WriteLong(int64(len(x.Value)))")
	cb.writeln("\t\tif err != nil {")
	cb.writeln("\t\t\treturn err")
	cb.writeln("\t\t}")
	cb.writeln("\t\tfor k := range x.Value {")
	cb.writeln("\t\t\terr = w.WriteString(k)")
	cb.writeln("\t\t\tif err != nil {")
	cb.writeln("\t\t\t\treturn err")
	cb.writeln("\t\t\t}")

	valueType := m.GetValues()
	if valueType != nil {
		generateValueWrite(cb, valueType, "x.Value[k]", "\t\t\t", 0, fixed)
	}

	cb.writeln("\t\t}")
	cb.writeln("\t}")
	cb.writeln("\terr = w.WriteLong(0)")
	cb.writeln("\tif err != nil {")
	cb.writeln("\t\treturn err")
	cb.writeln("\t}")
}
