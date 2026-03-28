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
	primitives := map[string]struct{}{
		"null": {}, "boolean": {}, "int": {}, "long": {},
		"float": {}, "double": {}, "bytes": {}, "string": {},
	}

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

	return errors.Join(errs...)
}

func collectDefinedNames(schema *idl.Schema) map[string]struct{} {
	names := make(map[string]struct{})

	addName := func(name, typeNamespace string) {
		names[name] = struct{}{}
		if typeNamespace != "" {
			names[typeNamespace+"."+name] = struct{}{}
		}
		if schema.Namespace != "" && schema.Namespace != typeNamespace {
			names[schema.Namespace+"."+name] = struct{}{}
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
