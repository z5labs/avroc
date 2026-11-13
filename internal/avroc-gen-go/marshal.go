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

// generateMarshalMethod generates the MarshalAvroBinary method for a type.
func generateMarshalMethod(cb *codeBuilder, ttg typeToGenerate) {
	t := ttg.typ

	switch v := t.Type.(type) {
	case *avrocpb.Type_Record:
		generateRecordMarshal(cb, v.Record)
	case *avrocpb.Type_EnumType:
		generateEnumMarshal(cb, v.EnumType)
	case *avrocpb.Type_Fixed:
		generateFixedMarshal(cb, v.Fixed)
	case *avrocpb.Type_Union:
		generateUnionMarshal(cb, v.Union, ttg.recordName, ttg.fieldName)
	}
}

// generateRecordMarshal generates the MarshalAvroBinary method for a record type.
func generateRecordMarshal(cb *codeBuilder, r *avrocpb.Record) {
	name := toPascalCase(r.GetName())
	recordName := r.GetName()

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
		generateFieldWrite(cb, f, recordName)
	}

	cb.writeln("\treturn nil")
	cb.writeln("}")
}

// generateFieldWrite generates the write code for a single field.
func generateFieldWrite(cb *codeBuilder, f *avrocpb.Field, recordName string) {
	fieldName := toFieldName(f.GetName())
	fieldType := f.GetType()

	if fieldType == nil {
		return
	}

	switch v := fieldType.Type.(type) {
	case *avrocpb.Type_Reference:
		generateReferenceWrite(cb, v.Reference, fieldName)
	case *avrocpb.Type_Array:
		generateArrayFieldWrite(cb, v.Array, fieldName)
	case *avrocpb.Type_MapType:
		generateMapFieldWrite(cb, v.MapType, fieldName)
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
	case *avrocpb.Type_Fixed:
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
func generateArrayFieldWrite(cb *codeBuilder, arr *avrocpb.Array, fieldName string) {
	cb.writef("\tif len(x.%s) > 0 {\n", fieldName)
	cb.writef("\t\terr = w.WriteLong(int64(len(x.%s)))\n", fieldName)
	cb.writeln("\t\tif err != nil {")
	cb.writeln("\t\t\treturn err")
	cb.writeln("\t\t}")
	cb.writef("\t\tfor i := range x.%s {\n", fieldName)

	// Generate write for each element using index to ensure addressability
	if itemType := arr.GetItems(); itemType != nil {
		generateItemWrite(cb, itemType, "x."+fieldName+"[i]", "\t\t\t", 0)
	}

	cb.writeln("\t\t}")
	cb.writeln("\t}")
	cb.writeln("\terr = w.WriteLong(0)")
	cb.writeln("\tif err != nil {")
	cb.writeln("\t\treturn err")
	cb.writeln("\t}")
}

// generateMapFieldWrite generates write code for a map field.
func generateMapFieldWrite(cb *codeBuilder, m *avrocpb.Map, fieldName string) {
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
		generateValueWrite(cb, valueType, "x."+fieldName+"[k]", "\t\t\t", 0)
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
func generateItemWrite(cb *codeBuilder, t *avrocpb.Type, varName string, indent string, depth int) {
	if t == nil {
		return
	}

	switch v := t.Type.(type) {
	case *avrocpb.Type_Reference:
		generateValueWrite(cb, t, varName, indent, depth)
	case *avrocpb.Type_Record, *avrocpb.Type_EnumType, *avrocpb.Type_Fixed, *avrocpb.Type_Union:
		// A union is the field's union interface, which writes its own branch
		// index.
		cb.writef("%serr = %s.MarshalAvroBinary(w)\n", indent, varName)
		cb.writef("%sif err != nil {\n", indent)
		cb.writef("%s\treturn err\n", indent)
		cb.writef("%s}\n", indent)
	case *avrocpb.Type_Array:
		generateNestedArrayWrite(cb, v.Array, varName, indent, depth+1)
	case *avrocpb.Type_MapType:
		generateNestedMapWrite(cb, v.MapType, varName, indent, depth+1)
	}
}

// generateValueWrite generates write code for a value in a non-addressable
// position (e.g. a map index), so for named types with pointer receivers we
// copy to a local variable first. An array or a map there needs no copy: it is
// only measured and indexed, never called a method on.
func generateValueWrite(cb *codeBuilder, t *avrocpb.Type, varName string, indent string, depth int) {
	switch v := t.GetType().(type) {
	case *avrocpb.Type_Array:
		generateNestedArrayWrite(cb, v.Array, varName, indent, depth+1)
		return
	case *avrocpb.Type_MapType:
		generateNestedMapWrite(cb, v.MapType, varName, indent, depth+1)
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
func generateNestedArrayWrite(cb *codeBuilder, arr *avrocpb.Array, varName string, indent string, depth int) {
	i := depthName("i", depth)

	generateBlockCountWrite(cb, varName, indent)
	cb.writef("%s\tfor %s := range %s {\n", indent, i, varName)
	if itemType := arr.GetItems(); itemType != nil {
		generateItemWrite(cb, itemType, varName+"["+i+"]", indent+"\t\t", depth)
	}
	cb.writef("%s\t}\n", indent)
	cb.writef("%s}\n", indent)
	generateBlockEndWrite(cb, indent)
}

// generateNestedMapWrite generates write code for a map that is itself an
// array's item or a map's value, as generateNestedArrayWrite does for an
// array.
func generateNestedMapWrite(cb *codeBuilder, m *avrocpb.Map, varName string, indent string, depth int) {
	k := depthName("k", depth)

	generateBlockCountWrite(cb, varName, indent)
	cb.writef("%s\tfor %s := range %s {\n", indent, k, varName)
	cb.writef("%s\t\terr = w.WriteString(%s)\n", indent, k)
	cb.writef("%s\t\tif err != nil {\n", indent)
	cb.writef("%s\t\t\treturn err\n", indent)
	cb.writef("%s\t\t}\n", indent)
	if valueType := m.GetValues(); valueType != nil {
		generateValueWrite(cb, valueType, varName+"["+k+"]", indent+"\t\t", depth)
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
func generateUnionMarshal(cb *codeBuilder, u *avrocpb.Union, recordName string, fieldName string) {
	unionName := unionTypeName(recordName, fieldName)

	for i, t := range u.Types {
		generateUnionMemberMarshal(cb, unionName, t, i)
	}
}

// generateUnionMemberMarshal generates the MarshalAvroBinary method for a union member.
func generateUnionMemberMarshal(cb *codeBuilder, unionName string, t *avrocpb.Type, index int) {
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
			generateUnionArrayMarshal(cb, v.Array)
		case *avrocpb.Type_MapType:
			generateUnionMapMarshal(cb, v.MapType)
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
func generateUnionArrayMarshal(cb *codeBuilder, arr *avrocpb.Array) {
	cb.writeln("\tif len(x.Value) > 0 {")
	cb.writeln("\t\terr = w.WriteLong(int64(len(x.Value)))")
	cb.writeln("\t\tif err != nil {")
	cb.writeln("\t\t\treturn err")
	cb.writeln("\t\t}")
	cb.writeln("\t\tfor i := range x.Value {")

	itemType := arr.GetItems()
	if itemType != nil {
		generateItemWrite(cb, itemType, "x.Value[i]", "\t\t\t", 0)
	}

	cb.writeln("\t\t}")
	cb.writeln("\t}")
	cb.writeln("\terr = w.WriteLong(0)")
	cb.writeln("\tif err != nil {")
	cb.writeln("\t\treturn err")
	cb.writeln("\t}")
}

// generateUnionMapMarshal generates map marshal code for a union member.
func generateUnionMapMarshal(cb *codeBuilder, m *avrocpb.Map) {
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
		generateValueWrite(cb, valueType, "x.Value[k]", "\t\t\t", 0)
	}

	cb.writeln("\t\t}")
	cb.writeln("\t}")
	cb.writeln("\terr = w.WriteLong(0)")
	cb.writeln("\tif err != nil {")
	cb.writeln("\t\treturn err")
	cb.writeln("\t}")
}
