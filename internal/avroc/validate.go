// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"errors"
	"fmt"

	"github.com/z5labs/avro-go/idl"
)

func validateSchema(schema *idl.Schema) error {
	primitives := avroPrimitives()

	defined := collectDefinedNames(schema)

	var refs []*idl.Ident
	collectIdentRefsFromType(schema.Type, &refs)
	for _, t := range schema.Types {
		collectIdentRefsFromType(t, &refs)
	}

	var errs []error
	for _, ref := range refs {
		if _, ok := primitives[ref.Value]; ok {
			continue
		}
		if _, ok := defined[ref.Value]; ok {
			continue
		}
		errs = append(errs, fmt.Errorf("unresolved type reference %q", ref.Value))
	}

	definedTypes := collectDefinedTypes(schema)

	validateDefaults(schema.Type, definedTypes, &errs)
	for _, t := range schema.Types {
		validateDefaults(t, definedTypes, &errs)
	}

	return errors.Join(errs...)
}

func collectDefinedTypes(schema *idl.Schema) map[string]idl.Type {
	types := make(map[string]idl.Type)

	addType := func(name, typeNamespace string, t idl.Type) {
		// Determine the effective namespace: an explicit type namespace
		// takes precedence; otherwise, the schema namespace is used.
		effectiveNamespace := typeNamespace
		if effectiveNamespace == "" {
			effectiveNamespace = schema.Namespace
		}

		// If there is no effective namespace at all, register only the bare name.
		if effectiveNamespace == "" {
			types[name] = t
			return
		}

		// Always register the fully-qualified name using the effective namespace.
		types[effectiveNamespace+"."+name] = t

		// Only register the bare name when the effective namespace matches the
		// schema namespace (i.e., when the namespace is inherited from or equal
		// to the schema namespace). This avoids leaking types from other
		// namespaces into the schema namespace or into unqualified lookups.
		if effectiveNamespace == schema.Namespace {
			types[name] = t
		}
	}

	registerType := func(t idl.Type) {
		switch v := t.(type) {
		case *idl.Record:
			addType(v.Name, v.Namespace, t)
		case *idl.Enum:
			addType(v.Name, v.Namespace, t)
		case *idl.Fixed:
			addType(v.Name, v.Namespace, t)
		}
	}

	registerType(schema.Type)
	for _, t := range schema.Types {
		registerType(t)
	}

	return types
}

func collectDefinedNames(schema *idl.Schema) map[string]struct{} {
	names := make(map[string]struct{})

	addName := func(name, typeNamespace string) {
		// Determine the effective namespace: an explicit type namespace
		// takes precedence; otherwise, the schema namespace is used.
		effectiveNamespace := typeNamespace
		if effectiveNamespace == "" {
			effectiveNamespace = schema.Namespace
		}

		// If there is no effective namespace at all, register only the bare name.
		if effectiveNamespace == "" {
			names[name] = struct{}{}
			return
		}

		// Always register the fully-qualified name using the effective namespace.
		names[effectiveNamespace+"."+name] = struct{}{}

		// Only register the bare name when the effective namespace matches the
		// schema namespace (i.e., when the namespace is inherited from or equal
		// to the schema namespace). This avoids leaking types from other
		// namespaces into the schema namespace or into unqualified lookups.
		if effectiveNamespace == schema.Namespace {
			names[name] = struct{}{}
		}
	}

	registerType := func(t idl.Type) {
		switch v := t.(type) {
		case *idl.Record:
			addName(v.Name, v.Namespace)
		case *idl.Enum:
			addName(v.Name, v.Namespace)
		case *idl.Fixed:
			addName(v.Name, v.Namespace)
		}
	}

	registerType(schema.Type)
	for _, t := range schema.Types {
		registerType(t)
	}

	return names
}

func validateDefaults(t idl.Type, definedTypes map[string]idl.Type, errs *[]error) {
	switch v := t.(type) {
	case *idl.Enum:
		if v.Default == nil {
			return
		}
		for _, val := range v.Values {
			if val.Value == v.Default.Value {
				return
			}
		}
		*errs = append(*errs, fmt.Errorf("enum %q default %q is not a declared value", v.Name, v.Default.Value))
	case *idl.Record:
		for _, f := range v.Fields {
			validateDefaults(f.Type, definedTypes, errs)
			if f.Default == nil {
				continue
			}
			if err := validateFieldDefault(f, definedTypes); err != nil {
				*errs = append(*errs, err)
			}
		}
	case *idl.Array:
		validateDefaults(v.Items, definedTypes, errs)
	case *idl.Union:
		for _, ut := range v.Types {
			validateDefaults(ut, definedTypes, errs)
		}
	}
}

func validateFieldDefault(f *idl.Field, definedTypes map[string]idl.Type) error {
	return validateDefaultForType(f.Name, f.Type, f.Default, definedTypes)
}

func validateDefaultForType(fieldName string, t idl.Type, val idl.Value, definedTypes map[string]idl.Type) error {
	switch v := t.(type) {
	case *idl.Ident:
		return validateDefaultForIdent(fieldName, v.Value, val, definedTypes)
	case *idl.Array:
		if _, ok := val.(idl.ArrayValue); !ok {
			return fmt.Errorf("field %q: expected array default, got %T", fieldName, val)
		}
		return nil
	case *idl.Map:
		if _, ok := val.(idl.ObjectValue); !ok {
			return fmt.Errorf("field %q: expected object default for map, got %T", fieldName, val)
		}
		return nil
	case *idl.Record:
		if _, ok := val.(idl.ObjectValue); !ok {
			return fmt.Errorf("field %q: expected object default for record, got %T", fieldName, val)
		}
		return nil
	case *idl.Enum:
		sv, ok := val.(idl.StringValue)
		if !ok {
			return fmt.Errorf("field %q: expected string default for enum, got %T", fieldName, val)
		}
		for _, ev := range v.Values {
			if ev.Value == string(sv) {
				return nil
			}
		}
		return fmt.Errorf("field %q: enum default %q is not a declared value of %q", fieldName, string(sv), v.Name)
	case *idl.Fixed:
		if _, ok := val.(idl.StringValue); !ok {
			return fmt.Errorf("field %q: expected string default for fixed, got %T", fieldName, val)
		}
		return nil
	case *idl.Union:
		if len(v.Types) == 0 {
			return fmt.Errorf("field %q: union has no types", fieldName)
		}
		return validateDefaultForType(fieldName, v.Types[0], val, definedTypes)
	default:
		return nil
	}
}

func validateDefaultForIdent(fieldName string, typeName string, val idl.Value, definedTypes map[string]idl.Type) error {
	switch typeName {
	case "null":
		if _, ok := val.(idl.NullValue); !ok {
			return fmt.Errorf("field %q: expected null default, got %T", fieldName, val)
		}
	case "boolean":
		if _, ok := val.(idl.BoolValue); !ok {
			return fmt.Errorf("field %q: expected boolean default, got %T", fieldName, val)
		}
	case "int", "long":
		if _, ok := val.(idl.IntValue); !ok {
			return fmt.Errorf("field %q: expected int default, got %T", fieldName, val)
		}
	case "float", "double":
		switch val.(type) {
		case idl.FloatValue, idl.IntValue:
		default:
			return fmt.Errorf("field %q: expected numeric default, got %T", fieldName, val)
		}
	case "bytes", "string":
		if _, ok := val.(idl.StringValue); !ok {
			return fmt.Errorf("field %q: expected string default, got %T", fieldName, val)
		}
	default:
		// Named type reference — resolve and validate
		if dt, ok := definedTypes[typeName]; ok {
			return validateDefaultForType(fieldName, dt, val, definedTypes)
		}
	}
	return nil
}

func collectIdentRefsFromType(t idl.Type, refs *[]*idl.Ident) {
	switch v := t.(type) {
	case *idl.Ident:
		*refs = append(*refs, v)
	case *idl.Record:
		for _, f := range v.Fields {
			collectIdentRefsFromType(f.Type, refs)
		}
	case *idl.Array:
		collectIdentRefsFromType(v.Items, refs)
	case *idl.Map:
		if v.Values != nil {
			*refs = append(*refs, v.Values)
		}
	case *idl.Union:
		for _, ut := range v.Types {
			collectIdentRefsFromType(ut, refs)
		}
	}
}
