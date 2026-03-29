// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"github.com/z5labs/avroc/internal/avrocpb"
)

// generateMarshalMethod generates the MarshalAvroBinary method for a type.
func generateMarshalMethod(cb *codeBuilder, ttg typeToGenerate) {
	t := ttg.typ

	switch v := t.Type.(type) {
	case *avrocpb.Type_Record:
		generateRecordMarshal(cb, v.Record)
	case *avrocpb.Type_EnumType:
		generateEnumMarshal(cb, v.EnumType)
	case *avrocpb.Type_Union:
		generateUnionMarshal(cb, v.Union, ttg.recordName, ttg.fieldName)
	}
}

// generateRecordMarshal generates the MarshalAvroBinary method for a record type.
func generateRecordMarshal(cb *codeBuilder, r *avrocpb.Record) {
	name := toPascalCase(r.GetName())
	recordName := r.GetName()

	cb.newline()
	cb.writef("func (x *%s) MarshalAvroBinary(w *avrolib.BinaryWriter) error {\n", name)

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
	case *avrocpb.Type_Ident:
		generateIdentWrite(cb, v.Ident, fieldName)
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
	}
}

// generateIdentWrite generates write code for an identifier type.
func generateIdentWrite(cb *codeBuilder, ident *avrocpb.Ident, fieldName string) {
	typeName := ident.GetValue()

	if method, ok := primitiveWriteMethod[typeName]; ok {
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
	cb.writef("\t\tfor _, v := range x.%s {\n", fieldName)

	// Generate write for each element
	itemType := arr.GetItems()
	if itemType != nil {
		generateItemWrite(cb, itemType, "v", "\t\t\t")
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
	cb.writef("\t\tfor k, v := range x.%s {\n", fieldName)
	cb.writeln("\t\t\terr = w.WriteString(k)")
	cb.writeln("\t\t\tif err != nil {")
	cb.writeln("\t\t\t\treturn err")
	cb.writeln("\t\t\t}")

	// Generate write for map value
	valueType := m.GetValues()
	if valueType != nil {
		generateValueIdentWrite(cb, valueType, "v", "\t\t\t")
	}

	cb.writeln("\t\t}")
	cb.writeln("\t}")
	cb.writeln("\terr = w.WriteLong(0)")
	cb.writeln("\tif err != nil {")
	cb.writeln("\t\treturn err")
	cb.writeln("\t}")
}

// generateItemWrite generates write code for an array item or similar value.
func generateItemWrite(cb *codeBuilder, t *avrocpb.Type, varName string, indent string) {
	if t == nil {
		return
	}

	switch v := t.Type.(type) {
	case *avrocpb.Type_Ident:
		generateValueIdentWrite(cb, v.Ident, varName, indent)
	case *avrocpb.Type_Record, *avrocpb.Type_EnumType:
		cb.writef("%serr = %s.MarshalAvroBinary(w)\n", indent, varName)
		cb.writef("%sif err != nil {\n", indent)
		cb.writef("%s\treturn err\n", indent)
		cb.writef("%s}\n", indent)
	}
}

// generateValueIdentWrite generates write code for a value with an ident type.
func generateValueIdentWrite(cb *codeBuilder, ident *avrocpb.Ident, varName string, indent string) {
	typeName := ident.GetValue()

	if method, ok := primitiveWriteMethod[typeName]; ok {
		cb.writef("%serr = w.%s(%s)\n", indent, method, varName)
		cb.writef("%sif err != nil {\n", indent)
		cb.writef("%s\treturn err\n", indent)
		cb.writef("%s}\n", indent)
	} else {
		// Named type reference
		cb.writef("%serr = %s.MarshalAvroBinary(w)\n", indent, varName)
		cb.writef("%sif err != nil {\n", indent)
		cb.writef("%s\treturn err\n", indent)
		cb.writef("%s}\n", indent)
	}
}

// generateEnumMarshal generates the MarshalAvroBinary method for an enum type.
func generateEnumMarshal(cb *codeBuilder, e *avrocpb.Enum) {
	name := toPascalCase(e.GetName())

	cb.newline()
	cb.writef("func (x %s) MarshalAvroBinary(w *avrolib.BinaryWriter) error {\n", name)
	cb.writeln("\treturn w.WriteInt(int32(x))")
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
	cb.writef("func (x %s) MarshalAvroBinary(w *avrolib.BinaryWriter) error {\n", memberName)

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
		case *avrocpb.Type_Ident:
			typeName := v.Ident.GetValue()
			if method, ok := primitiveWriteMethod[typeName]; ok {
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
	cb.writeln("\t\tfor _, v := range x.Value {")

	itemType := arr.GetItems()
	if itemType != nil {
		generateItemWrite(cb, itemType, "v", "\t\t\t")
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
	cb.writeln("\t\tfor k, v := range x.Value {")
	cb.writeln("\t\t\terr = w.WriteString(k)")
	cb.writeln("\t\t\tif err != nil {")
	cb.writeln("\t\t\t\treturn err")
	cb.writeln("\t\t\t}")

	valueType := m.GetValues()
	if valueType != nil {
		generateValueIdentWrite(cb, valueType, "v", "\t\t\t")
	}

	cb.writeln("\t\t}")
	cb.writeln("\t}")
	cb.writeln("\terr = w.WriteLong(0)")
	cb.writeln("\tif err != nil {")
	cb.writeln("\t\treturn err")
	cb.writeln("\t}")
}
