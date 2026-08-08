// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// Package ir holds the operations every generator performs on the resolved IR.
//
// Everything here is a walk over the message in hand: no symbol table, no
// namespace qualification, no matching of a name against Avro's list of
// primitives. docs/ir/SPEC.md forbids a generator re-deriving any of those,
// because avroc has already answered them; what remains is shared here so that
// one algebra is not implemented once per generator.
package ir

import (
	"strings"

	"github.com/z5labs/avroc/internal/avrocpb"
)

// NamedTypeName returns the simple name of a named type, or the empty string
// for anything else.
func NamedTypeName(t *avrocpb.Type) string {
	switch v := t.GetType().(type) {
	case *avrocpb.Type_Record:
		return v.Record.GetName()
	case *avrocpb.Type_EnumType:
		return v.EnumType.GetName()
	case *avrocpb.Type_Fixed:
		return v.Fixed.GetName()
	default:
		return ""
	}
}

// FullName returns the fully-qualified name a named type carries, or the empty
// string for anything else. It reads the name the producer resolved; it never
// joins a namespace onto a simple name.
func FullName(t *avrocpb.Type) string {
	switch v := t.GetType().(type) {
	case *avrocpb.Type_Record:
		return v.Record.GetFullName()
	case *avrocpb.Type_EnumType:
		return v.EnumType.GetFullName()
	case *avrocpb.Type_Fixed:
		return v.Fixed.GetFullName()
	default:
		return ""
	}
}

// SchemaBaseName returns the name a generator should build its output filename
// from: the schema's primary type, else its first additional type, else the
// last component of its namespace, else "schema".
func SchemaBaseName(schema *avrocpb.Schema) string {
	if name := NamedTypeName(schema.GetType()); name != "" {
		return name
	}
	if ref := schema.GetType().GetReference(); ref != nil {
		return ref.GetName()
	}
	for _, t := range schema.GetTypes() {
		if name := NamedTypeName(t); name != "" {
			return name
		}
	}
	if ns := schema.GetNamespace(); ns != "" {
		parts := strings.Split(ns, ".")
		return parts[len(parts)-1]
	}
	return "schema"
}

// SnakeCase converts a name to snake_case, dropping any namespace prefix.
func SnakeCase(s string) string {
	if s == "" {
		return ""
	}

	if idx := strings.LastIndex(s, "."); idx != -1 {
		s = s[idx+1:]
	}

	var result strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			result.WriteByte('_')
		}
		result.WriteRune(r)
	}

	return strings.ToLower(result.String())
}
