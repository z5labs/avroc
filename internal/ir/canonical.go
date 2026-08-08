// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package ir

import (
	"encoding/json"
	"fmt"

	"github.com/z5labs/avroc/internal/avrocpb"

	"github.com/z5labs/avro-go/canonical"
)

// Canonical converts a resolved schema's primary type to its Avro Parsing
// Canonical Form.
//
// It is the repository's only implementation of that conversion. avroc-gen-pcf
// publishes the bytes it produces and avroc-gen-go fingerprints them, so the
// two cannot disagree about a schema's canonical form — the failure mode that
// motivated resolving the IR in the first place.
//
// The walk carries no state: because the producer decided where each named type
// is written out in full and where it is referenced by name, a definition and a
// reference are distinguishable from the message alone.
func Canonical(schema *avrocpb.Schema) (canonical.Schema, error) {
	return canonicalType(schema.GetType())
}

// CanonicalJSON renders a resolved schema's Parsing Canonical Form as the bytes
// Avro defines: the input to a CRC-64-AVRO fingerprint, and the content
// avroc-gen-pcf writes.
func CanonicalJSON(schema *avrocpb.Schema) ([]byte, error) {
	cs, err := Canonical(schema)
	if err != nil {
		return nil, fmt.Errorf("failed to convert schema to canonical form: %w", err)
	}

	data, err := json.Marshal(cs)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal canonical schema: %w", err)
	}
	return data, nil
}

func canonicalType(t *avrocpb.Type) (canonical.Schema, error) {
	if t == nil {
		return canonical.Schema{}, fmt.Errorf("nil type")
	}

	switch v := t.GetType().(type) {
	case *avrocpb.Type_Record:
		return canonicalRecord(v.Record)
	case *avrocpb.Type_EnumType:
		return canonicalEnum(v.EnumType)
	case *avrocpb.Type_Fixed:
		return canonicalFixed(v.Fixed)
	case *avrocpb.Type_Array:
		return canonicalArray(v.Array)
	case *avrocpb.Type_MapType:
		return canonicalMap(v.MapType)
	case *avrocpb.Type_Union:
		return canonicalUnion(v.Union)
	case *avrocpb.Type_Reference:
		return canonicalReference(v.Reference)
	default:
		// A type constructor is a closed set: an unrecognised member is a
		// schema this generator cannot represent, not one to skip.
		return canonical.Schema{}, fmt.Errorf("unsupported type: %T", t.GetType())
	}
}

func canonicalRecord(r *avrocpb.Record) (canonical.Schema, error) {
	if r.GetFullName() == "" {
		return canonical.Schema{}, fmt.Errorf("record %q carries no fully-qualified name", r.GetName())
	}

	fields := make([]canonical.Field, 0, len(r.GetFields()))
	for _, f := range r.GetFields() {
		ft, err := canonicalType(f.GetType())
		if err != nil {
			return canonical.Schema{}, fmt.Errorf("field %q: %w", f.GetName(), err)
		}
		fields = append(fields, canonical.Field{
			Name: f.GetName(),
			Type: ft,
		})
	}

	return canonical.RecordSchema(canonical.Record{
		Name:   r.GetFullName(),
		Fields: fields,
	}), nil
}

func canonicalEnum(e *avrocpb.Enum) (canonical.Schema, error) {
	if e.GetFullName() == "" {
		return canonical.Schema{}, fmt.Errorf("enum %q carries no fully-qualified name", e.GetName())
	}

	symbols := make([]string, 0, len(e.GetValues()))
	for _, v := range e.GetValues() {
		symbols = append(symbols, v.GetValue())
	}

	return canonical.EnumSchema(canonical.Enum{
		Name:    e.GetFullName(),
		Symbols: symbols,
	}), nil
}

func canonicalFixed(f *avrocpb.Fixed) (canonical.Schema, error) {
	if f.GetFullName() == "" {
		return canonical.Schema{}, fmt.Errorf("fixed %q carries no fully-qualified name", f.GetName())
	}

	return canonical.FixedSchema(canonical.Fixed{
		Name: f.GetFullName(),
		Size: int(f.GetSize()),
	}), nil
}

func canonicalArray(a *avrocpb.Array) (canonical.Schema, error) {
	items, err := canonicalType(a.GetItems())
	if err != nil {
		return canonical.Schema{}, err
	}
	return canonical.ArraySchema(canonical.Array{Items: items}), nil
}

func canonicalMap(m *avrocpb.Map) (canonical.Schema, error) {
	values, err := canonicalType(m.GetValues())
	if err != nil {
		return canonical.Schema{}, err
	}
	return canonical.MapSchema(canonical.Map{Values: values}), nil
}

func canonicalUnion(u *avrocpb.Union) (canonical.Schema, error) {
	types := make(canonical.Union, 0, len(u.GetTypes()))
	for _, t := range u.GetTypes() {
		s, err := canonicalType(t)
		if err != nil {
			return canonical.Schema{}, err
		}
		types = append(types, s)
	}
	return canonical.UnionSchema(types), nil
}

// canonicalReference emits a reference as the bare name Avro's canonical form
// calls for. Both members of the kind are written the same way; the switch is
// here because the kind is a closed set, and an unrecognised member means a
// schema this generator has misread rather than one it may guess at.
func canonicalReference(ref *avrocpb.Reference) (canonical.Schema, error) {
	switch ref.GetKind() {
	case avrocpb.TypeRefKind_TYPE_REF_KIND_PRIMITIVE, avrocpb.TypeRefKind_TYPE_REF_KIND_NAMED:
		return canonical.PrimitiveSchema(canonical.Primitive(ref.GetName())), nil
	default:
		return canonical.Schema{}, fmt.Errorf("reference %q has unrecognised kind %v", ref.GetName(), ref.GetKind())
	}
}
