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
// reference are distinguishable from the message alone. Validate runs first, so
// the walk below can read what the descriptor says rather than re-checking it at
// every node.
func Canonical(schema *avrocpb.Schema) (canonical.Schema, error) {
	if err := Validate(schema); err != nil {
		return canonical.Schema{}, err
	}
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
		return canonicalEnum(v.EnumType), nil
	case *avrocpb.Type_Fixed:
		return canonicalFixed(v.Fixed), nil
	case *avrocpb.Type_Array:
		return canonicalArray(v.Array)
	case *avrocpb.Type_MapType:
		return canonicalMap(v.MapType)
	case *avrocpb.Type_Union:
		return canonicalUnion(v.Union)
	case *avrocpb.Type_Reference:
		// Both kinds are written as a bare name: the primitive's, or the named
		// type's fully-qualified one. Validate has already established that the
		// kind is recognised and the name is one the kind allows.
		return canonical.PrimitiveSchema(canonical.Primitive(v.Reference.GetName())), nil
	default:
		// A type constructor is a closed set: an unrecognised member is a
		// schema this generator cannot represent, not one to skip.
		return canonical.Schema{}, fmt.Errorf("unsupported type: %T", t.GetType())
	}
}

func canonicalRecord(r *avrocpb.Record) (canonical.Schema, error) {
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

func canonicalEnum(e *avrocpb.Enum) canonical.Schema {
	symbols := make([]string, 0, len(e.GetValues()))
	for _, v := range e.GetValues() {
		symbols = append(symbols, v.GetValue())
	}

	return canonical.EnumSchema(canonical.Enum{
		Name:    e.GetFullName(),
		Symbols: symbols,
	})
}

func canonicalFixed(f *avrocpb.Fixed) canonical.Schema {
	return canonical.FixedSchema(canonical.Fixed{
		Name: f.GetFullName(),
		Size: int(f.GetSize()),
	})
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
