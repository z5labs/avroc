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

	"github.com/z5labs/avroc/avrocpb"
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
// from: the name its root type is about, else its first additional type, else
// the last component of its namespace, else "schema".
func SchemaBaseName(schema *avrocpb.Schema) string {
	if name := rootTypeName(schema.GetType()); name != "" {
		return name
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

// rootTypeName returns the name the root type of a schema is about, or the
// empty string when it is about nothing that carries one.
//
// A named type is about itself, and so is a reference to one. An array and a
// map are about the type they contain — the record in schema array<Event>; is
// written out in full inside the array's items and is the only thing in the
// file — so the walk continues through them, however deeply they are nested.
// It stops at anything else: a primitive carries no name to build a filename
// from, and a union has no single subject, so both leave SchemaBaseName to its
// fallbacks rather than naming a file after one arbitrary branch.
func rootTypeName(t *avrocpb.Type) string {
	if name := NamedTypeName(t); name != "" {
		return name
	}
	switch v := t.GetType().(type) {
	case *avrocpb.Type_Reference:
		if v.Reference.GetKind() == avrocpb.TypeRefKind_TYPE_REF_KIND_NAMED {
			return v.Reference.GetName()
		}
		return ""
	case *avrocpb.Type_Array:
		return rootTypeName(v.Array.GetItems())
	case *avrocpb.Type_MapType:
		return rootTypeName(v.MapType.GetValues())
	default:
		return ""
	}
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
