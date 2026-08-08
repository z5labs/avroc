// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"github.com/z5labs/avroc/avrocpb"
)

// readMethodFor returns the BinaryReader method for a reference to an Avro
// primitive. A reference to a named type has none: it delegates to the named
// type's own UnmarshalAvroBinary.
func readMethodFor(ref *avrocpb.Reference) (string, bool) {
	if ref == nil || ref.GetKind() != avrocpb.TypeRefKind_TYPE_REF_KIND_PRIMITIVE {
		return "", false
	}
	method, ok := primitiveReadMethod[ref.GetName()]
	return method, ok
}

// generateUnmarshalMethod generates the UnmarshalAvroBinary method for a type.
func generateUnmarshalMethod(cb *codeBuilder, ttg typeToGenerate) {
	t := ttg.typ

	switch v := t.Type.(type) {
	case *avrocpb.Type_Record:
		generateRecordUnmarshal(cb, v.Record)
	case *avrocpb.Type_EnumType:
		generateEnumUnmarshal(cb, v.EnumType)
	case *avrocpb.Type_Fixed:
		generateFixedUnmarshal(cb, v.Fixed)
	case *avrocpb.Type_Union:
		generateUnionUnmarshal(cb, v.Union, ttg.recordName, ttg.fieldName)
	}
}

// generateRecordUnmarshal generates the UnmarshalAvroBinary method for a record type.
func generateRecordUnmarshal(cb *codeBuilder, r *avrocpb.Record) {
	name := toPascalCase(r.GetName())
	recordName := r.GetName()

	cb.newline()
	cb.writef("func (x *%s) UnmarshalAvroBinary(r *avro.BinaryReader) error {\n", name)

	fields := r.GetFields()
	if len(fields) == 0 {
		cb.writeln("\treturn nil")
		cb.writeln("}")
		return
	}

	cb.writeln("\tvar err error")

	for _, f := range fields {
		generateFieldRead(cb, f, recordName)
	}

	cb.writeln("\treturn nil")
	cb.writeln("}")
}

// generateFieldRead generates the read code for a single field.
func generateFieldRead(cb *codeBuilder, f *avrocpb.Field, recordName string) {
	fieldName := toFieldName(f.GetName())
	fieldType := f.GetType()

	if fieldType == nil {
		return
	}

	switch v := fieldType.Type.(type) {
	case *avrocpb.Type_Reference:
		generateReferenceRead(cb, v.Reference, fieldName)
	case *avrocpb.Type_Array:
		generateArrayFieldRead(cb, v.Array, fieldName)
	case *avrocpb.Type_MapType:
		generateMapFieldRead(cb, v.MapType, fieldName)
	case *avrocpb.Type_Union:
		unionName := unionTypeName(recordName, f.GetName())
		cb.writef("\tx.%s, err = unmarshal%s(r)\n", fieldName, unionName)
		cb.writeln("\tif err != nil {")
		cb.writeln("\t\treturn err")
		cb.writeln("\t}")
	case *avrocpb.Type_Record, *avrocpb.Type_EnumType, *avrocpb.Type_Fixed:
		cb.writef("\terr = x.%s.UnmarshalAvroBinary(r)\n", fieldName)
		cb.writeln("\tif err != nil {")
		cb.writeln("\t\treturn err")
		cb.writeln("\t}")
	}
}

// generateReferenceRead generates read code for a resolved reference.
func generateReferenceRead(cb *codeBuilder, ref *avrocpb.Reference, fieldName string) {
	if method, ok := readMethodFor(ref); ok {
		cb.writef("\tx.%s, err = r.%s()\n", fieldName, method)
		cb.writeln("\tif err != nil {")
		cb.writeln("\t\treturn err")
		cb.writeln("\t}")
	} else {
		// Named type reference - delegate to its UnmarshalAvroBinary
		cb.writef("\terr = x.%s.UnmarshalAvroBinary(r)\n", fieldName)
		cb.writeln("\tif err != nil {")
		cb.writeln("\t\treturn err")
		cb.writeln("\t}")
	}
}

// generateArrayFieldRead generates read code for an array field.
func generateArrayFieldRead(cb *codeBuilder, arr *avrocpb.Array, fieldName string) {
	cb.writef("\tx.%s = nil\n", fieldName)
	cb.writeln("\tfor {")
	cb.writeln("\t\tvar blkCount int64")
	cb.writeln("\t\tblkCount, err = r.ReadLong()")
	cb.writeln("\t\tif err != nil {")
	cb.writeln("\t\t\treturn err")
	cb.writeln("\t\t}")
	cb.writeln("\t\tif blkCount == 0 {")
	cb.writeln("\t\t\tbreak")
	cb.writeln("\t\t}")
	cb.writeln("\t\tif blkCount < 0 {")
	cb.writeln("\t\t\tblkCount = -blkCount")
	cb.writeln("\t\t\t_, err = r.ReadLong()")
	cb.writeln("\t\t\tif err != nil {")
	cb.writeln("\t\t\t\treturn err")
	cb.writeln("\t\t\t}")
	cb.writeln("\t\t}")
	cb.writeln("\t\tfor range blkCount {")

	itemType := arr.GetItems()
	if itemType != nil {
		generateArrayItemRead(cb, itemType, fieldName, "\t\t\t")
	}

	cb.writeln("\t\t}")
	cb.writeln("\t}")
}

// generateArrayItemRead generates read code for an individual array item.
func generateArrayItemRead(cb *codeBuilder, t *avrocpb.Type, fieldName string, indent string) {
	if t == nil {
		return
	}

	switch v := t.Type.(type) {
	case *avrocpb.Type_Reference:
		generateArrayItemReferenceRead(cb, v.Reference, fieldName, indent)
	case *avrocpb.Type_Record:
		goType := toPascalCase(v.Record.GetName())
		cb.writef("%svar v %s\n", indent, goType)
		cb.writef("%serr = v.UnmarshalAvroBinary(r)\n", indent)
		cb.writef("%sif err != nil {\n", indent)
		cb.writef("%s\treturn err\n", indent)
		cb.writef("%s}\n", indent)
		cb.writef("%sx.%s = append(x.%s, v)\n", indent, fieldName, fieldName)
	case *avrocpb.Type_EnumType:
		goType := toPascalCase(v.EnumType.GetName())
		cb.writef("%svar v %s\n", indent, goType)
		cb.writef("%serr = v.UnmarshalAvroBinary(r)\n", indent)
		cb.writef("%sif err != nil {\n", indent)
		cb.writef("%s\treturn err\n", indent)
		cb.writef("%s}\n", indent)
		cb.writef("%sx.%s = append(x.%s, v)\n", indent, fieldName, fieldName)
	case *avrocpb.Type_Fixed:
		goType := toPascalCase(v.Fixed.GetName())
		cb.writef("%svar v %s\n", indent, goType)
		cb.writef("%serr = v.UnmarshalAvroBinary(r)\n", indent)
		cb.writef("%sif err != nil {\n", indent)
		cb.writef("%s\treturn err\n", indent)
		cb.writef("%s}\n", indent)
		cb.writef("%sx.%s = append(x.%s, v)\n", indent, fieldName, fieldName)
	}
}

// generateArrayItemReferenceRead generates read code for an array item that is
// a resolved reference.
func generateArrayItemReferenceRead(cb *codeBuilder, ref *avrocpb.Reference, fieldName string, indent string) {
	if method, ok := readMethodFor(ref); ok {
		cb.writef("%svar v %s\n", indent, primitiveGoTypes[ref.GetName()])
		cb.writef("%sv, err = r.%s()\n", indent, method)
		cb.writef("%sif err != nil {\n", indent)
		cb.writef("%s\treturn err\n", indent)
		cb.writef("%s}\n", indent)
		cb.writef("%sx.%s = append(x.%s, v)\n", indent, fieldName, fieldName)
	} else {
		goType := toPascalCase(ref.GetName())
		cb.writef("%svar v %s\n", indent, goType)
		cb.writef("%serr = v.UnmarshalAvroBinary(r)\n", indent)
		cb.writef("%sif err != nil {\n", indent)
		cb.writef("%s\treturn err\n", indent)
		cb.writef("%s}\n", indent)
		cb.writef("%sx.%s = append(x.%s, v)\n", indent, fieldName, fieldName)
	}
}

// generateMapFieldRead generates read code for a map field.
func generateMapFieldRead(cb *codeBuilder, m *avrocpb.Map, fieldName string) {
	valueType := m.GetValues()
	valueGoType := goTypeForType(valueType, "")

	cb.writef("\tx.%s = make(map[string]%s)\n", fieldName, valueGoType)
	cb.writeln("\tfor {")
	cb.writeln("\t\tvar blkCount int64")
	cb.writeln("\t\tblkCount, err = r.ReadLong()")
	cb.writeln("\t\tif err != nil {")
	cb.writeln("\t\t\treturn err")
	cb.writeln("\t\t}")
	cb.writeln("\t\tif blkCount == 0 {")
	cb.writeln("\t\t\tbreak")
	cb.writeln("\t\t}")
	cb.writeln("\t\tif blkCount < 0 {")
	cb.writeln("\t\t\tblkCount = -blkCount")
	cb.writeln("\t\t\t_, err = r.ReadLong()")
	cb.writeln("\t\t\tif err != nil {")
	cb.writeln("\t\t\t\treturn err")
	cb.writeln("\t\t\t}")
	cb.writeln("\t\t}")
	cb.writeln("\t\tfor range blkCount {")

	// Read key
	cb.writeln("\t\t\tvar k string")
	cb.writeln("\t\t\tk, err = r.ReadString()")
	cb.writeln("\t\t\tif err != nil {")
	cb.writeln("\t\t\t\treturn err")
	cb.writeln("\t\t\t}")

	// Read value
	if valueType != nil {
		generateMapValueRead(cb, valueType, fieldName, "\t\t\t")
	}

	cb.writeln("\t\t}")
	cb.writeln("\t}")
}

// generateMapValueRead generates read code for a map value.
func generateMapValueRead(cb *codeBuilder, t *avrocpb.Type, fieldName string, indent string) {
	if method, ok := readMethodFor(t.GetReference()); ok {
		cb.writef("%svar v %s\n", indent, primitiveGoTypes[t.GetReference().GetName()])
		cb.writef("%sv, err = r.%s()\n", indent, method)
		cb.writef("%sif err != nil {\n", indent)
		cb.writef("%s\treturn err\n", indent)
		cb.writef("%s}\n", indent)
		cb.writef("%sx.%s[k] = v\n", indent, fieldName)
	} else {
		goType := goTypeForType(t, "")
		cb.writef("%svar v %s\n", indent, goType)
		cb.writef("%serr = v.UnmarshalAvroBinary(r)\n", indent)
		cb.writef("%sif err != nil {\n", indent)
		cb.writef("%s\treturn err\n", indent)
		cb.writef("%s}\n", indent)
		cb.writef("%sx.%s[k] = v\n", indent, fieldName)
	}
}

// generateEnumUnmarshal generates the UnmarshalAvroBinary method for an enum type.
func generateEnumUnmarshal(cb *codeBuilder, e *avrocpb.Enum) {
	name := toPascalCase(e.GetName())

	cb.newline()
	cb.writef("func (x *%s) UnmarshalAvroBinary(r *avro.BinaryReader) error {\n", name)
	cb.writeln("\tv, err := r.ReadInt()")
	cb.writeln("\tif err != nil {")
	cb.writeln("\t\treturn err")
	cb.writeln("\t}")
	cb.writef("\t*x = %s(v)\n", name)
	cb.writeln("\treturn nil")
	cb.writeln("}")
}

// generateFixedUnmarshal generates the UnmarshalAvroBinary method for a fixed type.
func generateFixedUnmarshal(cb *codeBuilder, f *avrocpb.Fixed) {
	name := toPascalCase(f.GetName())
	size := f.GetSize()

	cb.newline()
	cb.writef("func (x *%s) UnmarshalAvroBinary(r *avro.BinaryReader) error {\n", name)
	cb.writef("\tb, err := r.ReadFixed(%d)\n", size)
	cb.writeln("\tif err != nil {")
	cb.writeln("\t\treturn err")
	cb.writeln("\t}")
	cb.writeln("\tcopy(x[:], b)")
	cb.writeln("\treturn nil")
	cb.writeln("}")
}

// generateUnionUnmarshal generates the standalone unmarshal function for a union type.
func generateUnionUnmarshal(cb *codeBuilder, u *avrocpb.Union, recordName string, fieldName string) {
	unionName := unionTypeName(recordName, fieldName)

	cb.newline()
	cb.writef("func unmarshal%s(r *avro.BinaryReader) (%s, error) {\n", unionName, unionName)
	cb.writeln("\tindex, err := r.ReadLong()")
	cb.writeln("\tif err != nil {")
	cb.writeln("\t\treturn nil, err")
	cb.writeln("\t}")
	cb.writeln("\tswitch index {")

	for i, t := range u.Types {
		generateUnionMemberRead(cb, unionName, t, i)
	}

	cb.writeln("\tdefault:")
	cb.writef("\t\treturn nil, fmt.Errorf(\"unknown union index: %%d\", index)\n")
	cb.writeln("\t}")
	cb.writeln("}")
}

// generateUnionMemberRead generates read code for a single union member case.
func generateUnionMemberRead(cb *codeBuilder, unionName string, t *avrocpb.Type, index int) {
	memberName := unionMemberName(unionName, t)

	cb.writef("\tcase %d:\n", index)

	if isNullType(t) {
		cb.writef("\t\treturn %s{}, nil\n", memberName)
		return
	}

	switch v := t.Type.(type) {
	case *avrocpb.Type_Reference:
		if method, ok := readMethodFor(v.Reference); ok {
			cb.writef("\t\tvar v %s\n", primitiveGoTypes[v.Reference.GetName()])
			cb.writef("\t\tv, err = r.%s()\n", method)
			cb.writeln("\t\tif err != nil {")
			cb.writeln("\t\t\treturn nil, err")
			cb.writeln("\t\t}")
			cb.writef("\t\treturn %s{Value: v}, nil\n", memberName)
		} else {
			// Named type reference
			cb.writef("\t\tvar v %s\n", memberName)
			cb.writeln("\t\terr = v.Value.UnmarshalAvroBinary(r)")
			cb.writeln("\t\tif err != nil {")
			cb.writeln("\t\t\treturn nil, err")
			cb.writeln("\t\t}")
			cb.writeln("\t\treturn v, nil")
		}
	case *avrocpb.Type_Record, *avrocpb.Type_EnumType, *avrocpb.Type_Fixed:
		cb.writef("\t\tvar v %s\n", memberName)
		cb.writeln("\t\terr = v.Value.UnmarshalAvroBinary(r)")
		cb.writeln("\t\tif err != nil {")
		cb.writeln("\t\t\treturn nil, err")
		cb.writeln("\t\t}")
		cb.writeln("\t\treturn v, nil")
	case *avrocpb.Type_Array:
		cb.writef("\t\tvar result %s\n", memberName)
		generateUnionArrayRead(cb, v.Array)
		cb.writeln("\t\treturn result, nil")
	case *avrocpb.Type_MapType:
		generateUnionMapReadInit(cb, v.MapType, memberName)
		generateUnionMapRead(cb, v.MapType)
		cb.writeln("\t\treturn result, nil")
	}
}

// generateUnionArrayRead generates array unmarshal code for a union member.
func generateUnionArrayRead(cb *codeBuilder, arr *avrocpb.Array) {
	cb.writeln("\t\tfor {")
	cb.writeln("\t\t\tvar blkCount int64")
	cb.writeln("\t\t\tblkCount, err = r.ReadLong()")
	cb.writeln("\t\t\tif err != nil {")
	cb.writeln("\t\t\t\treturn nil, err")
	cb.writeln("\t\t\t}")
	cb.writeln("\t\t\tif blkCount == 0 {")
	cb.writeln("\t\t\t\tbreak")
	cb.writeln("\t\t\t}")
	cb.writeln("\t\t\tif blkCount < 0 {")
	cb.writeln("\t\t\t\tblkCount = -blkCount")
	cb.writeln("\t\t\t\t_, err = r.ReadLong()")
	cb.writeln("\t\t\t\tif err != nil {")
	cb.writeln("\t\t\t\t\treturn nil, err")
	cb.writeln("\t\t\t\t}")
	cb.writeln("\t\t\t}")
	cb.writeln("\t\t\tfor range blkCount {")

	itemType := arr.GetItems()
	if itemType != nil {
		generateUnionArrayItemRead(cb, itemType, "\t\t\t\t")
	}

	cb.writeln("\t\t\t}")
	cb.writeln("\t\t}")
}

// generateUnionArrayItemRead generates read code for a union array item.
func generateUnionArrayItemRead(cb *codeBuilder, t *avrocpb.Type, indent string) {
	if t == nil {
		return
	}

	switch v := t.Type.(type) {
	case *avrocpb.Type_Reference:
		if method, ok := readMethodFor(v.Reference); ok {
			cb.writef("%svar v %s\n", indent, primitiveGoTypes[v.Reference.GetName()])
			cb.writef("%sv, err = r.%s()\n", indent, method)
			cb.writef("%sif err != nil {\n", indent)
			cb.writef("%s\treturn nil, err\n", indent)
			cb.writef("%s}\n", indent)
			cb.writef("%sresult.Value = append(result.Value, v)\n", indent)
		} else {
			goType := toPascalCase(v.Reference.GetName())
			cb.writef("%svar v %s\n", indent, goType)
			cb.writef("%serr = v.UnmarshalAvroBinary(r)\n", indent)
			cb.writef("%sif err != nil {\n", indent)
			cb.writef("%s\treturn nil, err\n", indent)
			cb.writef("%s}\n", indent)
			cb.writef("%sresult.Value = append(result.Value, v)\n", indent)
		}
	case *avrocpb.Type_Record:
		goType := toPascalCase(v.Record.GetName())
		cb.writef("%svar v %s\n", indent, goType)
		cb.writef("%serr = v.UnmarshalAvroBinary(r)\n", indent)
		cb.writef("%sif err != nil {\n", indent)
		cb.writef("%s\treturn nil, err\n", indent)
		cb.writef("%s}\n", indent)
		cb.writef("%sresult.Value = append(result.Value, v)\n", indent)
	case *avrocpb.Type_EnumType:
		goType := toPascalCase(v.EnumType.GetName())
		cb.writef("%svar v %s\n", indent, goType)
		cb.writef("%serr = v.UnmarshalAvroBinary(r)\n", indent)
		cb.writef("%sif err != nil {\n", indent)
		cb.writef("%s\treturn nil, err\n", indent)
		cb.writef("%s}\n", indent)
		cb.writef("%sresult.Value = append(result.Value, v)\n", indent)
	case *avrocpb.Type_Fixed:
		goType := toPascalCase(v.Fixed.GetName())
		cb.writef("%svar v %s\n", indent, goType)
		cb.writef("%serr = v.UnmarshalAvroBinary(r)\n", indent)
		cb.writef("%sif err != nil {\n", indent)
		cb.writef("%s\treturn nil, err\n", indent)
		cb.writef("%s}\n", indent)
		cb.writef("%sresult.Value = append(result.Value, v)\n", indent)
	}
}

// generateUnionMapReadInit generates the initialization for a union map member.
func generateUnionMapReadInit(cb *codeBuilder, m *avrocpb.Map, memberName string) {
	valueGoType := goTypeForType(m.GetValues(), "")
	cb.writef("\t\tresult := %s{Value: make(map[string]%s)}\n", memberName, valueGoType)
}

// generateUnionMapRead generates map unmarshal code for a union member.
func generateUnionMapRead(cb *codeBuilder, m *avrocpb.Map) {
	cb.writeln("\t\tfor {")
	cb.writeln("\t\t\tvar blkCount int64")
	cb.writeln("\t\t\tblkCount, err = r.ReadLong()")
	cb.writeln("\t\t\tif err != nil {")
	cb.writeln("\t\t\t\treturn nil, err")
	cb.writeln("\t\t\t}")
	cb.writeln("\t\t\tif blkCount == 0 {")
	cb.writeln("\t\t\t\tbreak")
	cb.writeln("\t\t\t}")
	cb.writeln("\t\t\tif blkCount < 0 {")
	cb.writeln("\t\t\t\tblkCount = -blkCount")
	cb.writeln("\t\t\t\t_, err = r.ReadLong()")
	cb.writeln("\t\t\t\tif err != nil {")
	cb.writeln("\t\t\t\t\treturn nil, err")
	cb.writeln("\t\t\t\t}")
	cb.writeln("\t\t\t}")
	cb.writeln("\t\t\tfor range blkCount {")

	// Read key
	cb.writeln("\t\t\t\tvar k string")
	cb.writeln("\t\t\t\tk, err = r.ReadString()")
	cb.writeln("\t\t\t\tif err != nil {")
	cb.writeln("\t\t\t\t\treturn nil, err")
	cb.writeln("\t\t\t\t}")

	// Read value
	valueType := m.GetValues()
	if valueType != nil {
		generateUnionMapValueRead(cb, valueType, "\t\t\t\t")
	}

	cb.writeln("\t\t\t}")
	cb.writeln("\t\t}")
}

// generateUnionMapValueRead generates read code for a union map value.
func generateUnionMapValueRead(cb *codeBuilder, t *avrocpb.Type, indent string) {
	if method, ok := readMethodFor(t.GetReference()); ok {
		cb.writef("%svar v %s\n", indent, primitiveGoTypes[t.GetReference().GetName()])
		cb.writef("%sv, err = r.%s()\n", indent, method)
		cb.writef("%sif err != nil {\n", indent)
		cb.writef("%s\treturn nil, err\n", indent)
		cb.writef("%s}\n", indent)
		cb.writef("%sresult.Value[k] = v\n", indent)
	} else {
		goType := goTypeForType(t, "")
		cb.writef("%svar v %s\n", indent, goType)
		cb.writef("%serr = v.UnmarshalAvroBinary(r)\n", indent)
		cb.writef("%sif err != nil {\n", indent)
		cb.writef("%s\treturn nil, err\n", indent)
		cb.writef("%s}\n", indent)
		cb.writef("%sresult.Value[k] = v\n", indent)
	}
}
