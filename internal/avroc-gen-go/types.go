// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"strings"
	"unicode"

	"github.com/z5labs/avroc/internal/avrocpb"
)

// primitiveGoTypes maps Avro primitive type names to their Go equivalents.
var primitiveGoTypes = map[string]string{
	"null":    "", // handled specially in unions
	"boolean": "bool",
	"int":     "int32",
	"long":    "int64",
	"float":   "float32",
	"double":  "float64",
	"bytes":   "[]byte",
	"string":  "string",
}

// primitiveWriteMethod maps Avro primitive type names to BinaryWriter method names.
var primitiveWriteMethod = map[string]string{
	"boolean": "WriteBool",
	"int":     "WriteInt",
	"long":    "WriteLong",
	"float":   "WriteFloat",
	"double":  "WriteDouble",
	"bytes":   "WriteBytes",
	"string":  "WriteString",
}

// goTypeForIdent returns the Go type string for an Avro Ident.
// For primitives, it returns the Go equivalent.
// For named types, it returns the PascalCase Go identifier.
func goTypeForIdent(ident *avrocpb.Ident) string {
	if ident == nil {
		return ""
	}

	name := ident.GetValue()
	if goType, ok := primitiveGoTypes[name]; ok {
		return goType
	}

	// Named type reference - convert to Go identifier
	return toPascalCase(name)
}

// goTypeForType returns the Go type string for an Avro Type.
func goTypeForType(t *avrocpb.Type, unionName string) string {
	if t == nil {
		return ""
	}

	switch v := t.Type.(type) {
	case *avrocpb.Type_Ident:
		return goTypeForIdent(v.Ident)
	case *avrocpb.Type_Record:
		return toPascalCase(v.Record.GetName())
	case *avrocpb.Type_EnumType:
		return toPascalCase(v.EnumType.GetName())
	case *avrocpb.Type_Fixed:
		return toPascalCase(v.Fixed.GetName())
	case *avrocpb.Type_Array:
		itemType := goTypeForType(v.Array.GetItems(), "")
		return "[]" + itemType
	case *avrocpb.Type_MapType:
		valueType := goTypeForIdent(v.MapType.GetValues())
		return "map[string]" + valueType
	case *avrocpb.Type_Union:
		// Union types are represented as interfaces. If no union name is
		// provided (e.g., in nested contexts like array item types), fall
		// back to a compilable generic type.
		if unionName == "" {
			return "any"
		}
		return unionName
	default:
		return "any"
	}
}

// toPascalCase converts a string to PascalCase.
// Handles dot-separated names by taking the last component.
func toPascalCase(s string) string {
	if s == "" {
		return ""
	}

	// Handle fully-qualified names (e.g., "com.example.MyType")
	if idx := strings.LastIndex(s, "."); idx != -1 {
		s = s[idx+1:]
	}

	// Handle snake_case and convert to PascalCase
	var result strings.Builder
	capitalizeNext := true

	for _, r := range s {
		if r == '_' || r == '-' {
			capitalizeNext = true
			continue
		}
		if capitalizeNext {
			result.WriteRune(unicode.ToUpper(r))
			capitalizeNext = false
		} else {
			result.WriteRune(r)
		}
	}

	return result.String()
}

// toFieldName converts an Avro field name to a Go exported field name.
func toFieldName(name string) string {
	return toPascalCase(name)
}

// unionTypeName generates a name for a union type based on the record and field it belongs to.
// This includes the record name to avoid collisions when multiple records have fields with the same name.
func unionTypeName(recordName string, fieldName string) string {
	if recordName == "" {
		return toPascalCase(fieldName) + "Union"
	}
	return toPascalCase(recordName) + toPascalCase(fieldName) + "Union"
}

// isNullType returns true if the type is the null primitive.
func isNullType(t *avrocpb.Type) bool {
	if t == nil {
		return false
	}
	if ident, ok := t.Type.(*avrocpb.Type_Ident); ok {
		return ident.Ident.GetValue() == "null"
	}
	return false
}

// unionMemberName returns the Go type name for a union member wrapper.
func unionMemberName(unionName string, t *avrocpb.Type) string {
	if isNullType(t) {
		return unionName + "Null"
	}

	switch v := t.Type.(type) {
	case *avrocpb.Type_Ident:
		return unionName + toPascalCase(v.Ident.GetValue())
	case *avrocpb.Type_Record:
		return unionName + toPascalCase(v.Record.GetName())
	case *avrocpb.Type_EnumType:
		return unionName + toPascalCase(v.EnumType.GetName())
	case *avrocpb.Type_Fixed:
		return unionName + toPascalCase(v.Fixed.GetName())
	case *avrocpb.Type_Array:
		return unionName + "Array"
	case *avrocpb.Type_MapType:
		return unionName + "Map"
	default:
		return unionName + "Unknown"
	}
}
