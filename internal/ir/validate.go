// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package ir

import (
	"fmt"

	"github.com/z5labs/avroc/internal/avrocpb"
)

// avroPrimitives is Avro's closed list of primitive type names.
//
// Having it here is not a generator re-deriving classification: a reference
// already states which of the two it is, and nothing below asks the name that
// question. What this list is for is checking that a reference *claiming* to
// name a primitive names one that exists — the same closed-set rule
// docs/ir/SPEC.md applies to type constructors and sort orders, and the only
// thing standing between a malformed descriptor and a canonical form, and so a
// fingerprint, that is quietly wrong.
func avroPrimitives() map[string]struct{} {
	return map[string]struct{}{
		"null": {}, "boolean": {}, "int": {}, "long": {},
		"float": {}, "double": {}, "bytes": {}, "string": {},
	}
}

// Validate reports whether a schema is resolved in the sense docs/ir/SPEC.md
// defines, and is where a consumer refuses a descriptor rather than guessing at
// it. A generator calls it before emitting anything, so a malformed descriptor
// fails the invocation instead of producing partial or silently wrong output.
//
// It rejects a type constructor it does not recognise, a reference whose kind
// it does not recognise, a reference claiming to be a primitive that names none,
// and a named type carrying no fully-qualified name.
func Validate(schema *avrocpb.Schema) error {
	primitives := avroPrimitives()

	if err := validateType(schema.GetType(), primitives); err != nil {
		return err
	}
	for _, t := range schema.GetTypes() {
		if err := validateType(t, primitives); err != nil {
			return err
		}
	}
	return nil
}

func validateType(t *avrocpb.Type, primitives map[string]struct{}) error {
	if t == nil {
		return fmt.Errorf("nil type")
	}

	switch v := t.GetType().(type) {
	case *avrocpb.Type_Record:
		if v.Record.GetFullName() == "" {
			return fmt.Errorf("record %q carries no fully-qualified name", v.Record.GetName())
		}
		for _, f := range v.Record.GetFields() {
			if err := validateType(f.GetType(), primitives); err != nil {
				return fmt.Errorf("record %q field %q: %w", v.Record.GetFullName(), f.GetName(), err)
			}
		}
		return nil
	case *avrocpb.Type_EnumType:
		if v.EnumType.GetFullName() == "" {
			return fmt.Errorf("enum %q carries no fully-qualified name", v.EnumType.GetName())
		}
		return nil
	case *avrocpb.Type_Fixed:
		if v.Fixed.GetFullName() == "" {
			return fmt.Errorf("fixed %q carries no fully-qualified name", v.Fixed.GetName())
		}
		return nil
	case *avrocpb.Type_Array:
		return validateType(v.Array.GetItems(), primitives)
	case *avrocpb.Type_MapType:
		return validateType(v.MapType.GetValues(), primitives)
	case *avrocpb.Type_Union:
		for i, ut := range v.Union.GetTypes() {
			if err := validateType(ut, primitives); err != nil {
				return fmt.Errorf("union branch %d: %w", i, err)
			}
		}
		return nil
	case *avrocpb.Type_Reference:
		return validateReference(v.Reference, primitives)
	default:
		// A type constructor is a closed set. An unrecognised member is a
		// schema this consumer cannot represent, not one to skip.
		return fmt.Errorf("unsupported type: %T", t.GetType())
	}
}

func validateReference(ref *avrocpb.Reference, primitives map[string]struct{}) error {
	switch ref.GetKind() {
	case avrocpb.TypeRefKind_TYPE_REF_KIND_PRIMITIVE:
		if _, ok := primitives[ref.GetName()]; !ok {
			return fmt.Errorf("reference %q is a primitive reference but names no Avro primitive", ref.GetName())
		}
		return nil
	case avrocpb.TypeRefKind_TYPE_REF_KIND_NAMED:
		if ref.GetName() == "" {
			return fmt.Errorf("named reference carries no name")
		}
		return nil
	default:
		return fmt.Errorf("reference %q has unrecognised kind %v", ref.GetName(), ref.GetKind())
	}
}
