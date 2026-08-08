// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"fmt"
	"strings"

	"github.com/z5labs/avroc/avrocpb"

	"github.com/z5labs/avro-go/idl"
	"google.golang.org/protobuf/proto"
)

// avroPrimitives is Avro's closed list of primitive type names. It is the only
// place in avroc that knows it: a generator is told which of the two a
// reference is, and never matches a name against this list itself.
func avroPrimitives() map[string]struct{} {
	return map[string]struct{}{
		"null": {}, "boolean": {}, "int": {}, "long": {},
		"float": {}, "double": {}, "bytes": {}, "string": {},
	}
}

// resolver turns a parsed idl.Schema into the resolved IR described by
// docs/ir/SPEC.md. It owns the three questions the IDL leaves open — namespace
// qualification, primitive-versus-named classification, and where a named type
// is written out in full versus referred to by name — so that no generator
// answers any of them.
type resolver struct {
	// schemaNamespace is the namespace enclosing every top-level declaration.
	schemaNamespace string

	// primitives is Avro's primitive list, consulted only here.
	primitives map[string]struct{}

	// named indexes every top-level named declaration by its fully-qualified
	// name. The value is the declaration as parsed.
	named map[string]idl.Type

	// defined records the fully-qualified names already written out in full.
	// The first use of a named type inlines its definition; every later use
	// becomes a Reference carrying the same fully-qualified name.
	defined map[string]bool
}

// resolveSchema maps a parsed schema onto the resolved IR sent to generators.
func resolveSchema(schema *idl.Schema) (*avrocpb.Schema, error) {
	r := &resolver{
		schemaNamespace: schema.Namespace,
		primitives:      avroPrimitives(),
		named:           make(map[string]idl.Type),
		defined:         make(map[string]bool),
	}

	r.index(schema.Type)
	for _, t := range schema.Types {
		r.index(t)
	}

	root, err := r.resolveType(schema.Type, schema.Namespace)
	if err != nil {
		return nil, err
	}

	// Anything never reached from the root still belongs in the descriptor, in
	// declaration order, so a schema declaring a type nothing references does
	// not silently lose it.
	var types []*avrocpb.Type
	for _, t := range schema.Types {
		name, namespace, ok := namedDeclaration(t)
		if ok && r.defined[qualify(r.namespaceOf(namespace), name)] {
			continue
		}
		rt, err := r.resolveType(t, schema.Namespace)
		if err != nil {
			return nil, err
		}
		types = append(types, rt)
	}

	return &avrocpb.Schema{
		Namespace: proto.String(schema.Namespace),
		Type:      root,
		Types:     types,
	}, nil
}

// index registers a top-level named declaration under its fully-qualified name.
func (r *resolver) index(t idl.Type) {
	name, namespace, ok := namedDeclaration(t)
	if !ok {
		return
	}
	r.named[qualify(r.namespaceOf(namespace), name)] = t
}

// namespaceOf returns the effective namespace for a declaration: its own if it
// declared one, otherwise the schema's.
func (r *resolver) namespaceOf(declared string) string {
	if declared != "" {
		return declared
	}
	return r.schemaNamespace
}

// resolveType resolves a type in the scope of enclosingNamespace, which is the
// namespace a declaration inherits when it does not carry one of its own.
func (r *resolver) resolveType(t idl.Type, enclosingNamespace string) (*avrocpb.Type, error) {
	switch v := t.(type) {
	case *idl.Record:
		return r.resolveRecord(v, enclosingNamespace)
	case *idl.Enum:
		return r.resolveEnum(v, enclosingNamespace)
	case *idl.Fixed:
		return r.resolveFixed(v, enclosingNamespace)
	case *idl.Array:
		items, err := r.resolveType(v.Items, enclosingNamespace)
		if err != nil {
			return nil, err
		}
		return &avrocpb.Type{Type: &avrocpb.Type_Array{Array: &avrocpb.Array{Items: items}}}, nil
	case *idl.Map:
		values, err := r.resolveType(v.Values, enclosingNamespace)
		if err != nil {
			return nil, err
		}
		return &avrocpb.Type{Type: &avrocpb.Type_MapType{MapType: &avrocpb.Map{Values: values}}}, nil
	case *idl.Union:
		types := make([]*avrocpb.Type, len(v.Types))
		for i, ut := range v.Types {
			var err error
			types[i], err = r.resolveType(ut, enclosingNamespace)
			if err != nil {
				return nil, err
			}
		}
		return &avrocpb.Type{Type: &avrocpb.Type_Union{Union: &avrocpb.Union{Types: types}}}, nil
	case *idl.Ident:
		return r.resolveIdent(v, enclosingNamespace)
	case idl.Ident:
		return r.resolveIdent(&v, enclosingNamespace)
	default:
		return nil, fmt.Errorf("unsupported IDL type: %T", t)
	}
}

// resolveIdent resolves a bare identifier: an Avro primitive, the first use of
// a named type, or a later reference to one already written out.
func (r *resolver) resolveIdent(id *idl.Ident, enclosingNamespace string) (*avrocpb.Type, error) {
	if id == nil {
		return nil, fmt.Errorf("nil type reference")
	}

	if _, ok := r.primitives[id.Value]; ok {
		return referenceType(id.Value, avrocpb.TypeRefKind_TYPE_REF_KIND_PRIMITIVE), nil
	}

	fullName, decl, err := r.lookup(id.Value, enclosingNamespace)
	if err != nil {
		return nil, err
	}

	// First use writes the definition out in full; every later use is a
	// reference by fully-qualified name.
	if !r.defined[fullName] {
		return r.resolveType(decl, r.schemaNamespace)
	}
	return referenceType(fullName, avrocpb.TypeRefKind_TYPE_REF_KIND_NAMED), nil
}

// lookup resolves a type reference to the fully-qualified name of a declaration
// the schema makes. A reference resolving to neither a primitive nor a
// declaration is an unresolved schema, and is rejected rather than emitted.
func (r *resolver) lookup(name, enclosingNamespace string) (string, idl.Type, error) {
	candidates := []string{name}
	if !strings.Contains(name, ".") {
		candidates = []string{
			qualify(enclosingNamespace, name),
			qualify(r.schemaNamespace, name),
			name,
		}
	}

	for _, c := range candidates {
		if decl, ok := r.named[c]; ok {
			return c, decl, nil
		}
	}

	return "", nil, fmt.Errorf("unresolved type reference %q", name)
}

func (r *resolver) resolveRecord(v *idl.Record, enclosingNamespace string) (*avrocpb.Type, error) {
	namespace := v.Namespace
	if namespace == "" {
		namespace = enclosingNamespace
	}
	fullName := qualify(namespace, v.Name)

	if r.defined[fullName] {
		return referenceType(fullName, avrocpb.TypeRefKind_TYPE_REF_KIND_NAMED), nil
	}
	r.defined[fullName] = true

	fields := make([]*avrocpb.Field, len(v.Fields))
	for i, f := range v.Fields {
		ft, err := r.resolveType(f.Type, namespace)
		if err != nil {
			return nil, fmt.Errorf("record %q field %q: %w", fullName, f.Name, err)
		}
		sortOrder := avrocpb.SortOrder(f.SortOrder)
		fields[i] = &avrocpb.Field{
			Name: proto.String(f.Name),
			// A field alias is an alternate field name, not a type name, so it
			// is not qualified.
			Aliases:   f.Aliases,
			Type:      ft,
			SortOrder: &sortOrder,
		}
	}

	return &avrocpb.Type{
		Type: &avrocpb.Type_Record{
			Record: &avrocpb.Record{
				Name:      proto.String(v.Name),
				Namespace: proto.String(namespace),
				FullName:  proto.String(fullName),
				Aliases:   qualifyAll(namespace, v.Aliases),
				Fields:    fields,
			},
		},
	}, nil
}

func (r *resolver) resolveEnum(v *idl.Enum, enclosingNamespace string) (*avrocpb.Type, error) {
	namespace := v.Namespace
	if namespace == "" {
		namespace = enclosingNamespace
	}
	fullName := qualify(namespace, v.Name)

	if r.defined[fullName] {
		return referenceType(fullName, avrocpb.TypeRefKind_TYPE_REF_KIND_NAMED), nil
	}
	r.defined[fullName] = true

	values := make([]*avrocpb.Ident, len(v.Values))
	for i, val := range v.Values {
		values[i] = mapToProtoIdent(val)
	}

	return &avrocpb.Type{
		Type: &avrocpb.Type_EnumType{
			EnumType: &avrocpb.Enum{
				Name:      proto.String(v.Name),
				Namespace: proto.String(namespace),
				FullName:  proto.String(fullName),
				Aliases:   qualifyAll(namespace, v.Aliases),
				Values:    values,
				Default:   mapToProtoIdent(v.Default),
			},
		},
	}, nil
}

func (r *resolver) resolveFixed(v *idl.Fixed, enclosingNamespace string) (*avrocpb.Type, error) {
	namespace := v.Namespace
	if namespace == "" {
		namespace = enclosingNamespace
	}
	fullName := qualify(namespace, v.Name)

	if r.defined[fullName] {
		return referenceType(fullName, avrocpb.TypeRefKind_TYPE_REF_KIND_NAMED), nil
	}
	r.defined[fullName] = true

	size := int32(v.Size)
	return &avrocpb.Type{
		Type: &avrocpb.Type_Fixed{
			Fixed: &avrocpb.Fixed{
				Name:      proto.String(v.Name),
				Namespace: proto.String(namespace),
				FullName:  proto.String(fullName),
				Aliases:   qualifyAll(namespace, v.Aliases),
				Size:      &size,
			},
		},
	}, nil
}

// referenceType wraps a resolved reference as a Type.
func referenceType(name string, kind avrocpb.TypeRefKind) *avrocpb.Type {
	return &avrocpb.Type{
		Type: &avrocpb.Type_Reference{
			Reference: &avrocpb.Reference{
				Name: proto.String(name),
				Kind: kind.Enum(),
			},
		},
	}
}

// qualify joins a namespace and a simple name into a full name. A name that is
// already qualified is returned unchanged.
func qualify(namespace, name string) string {
	if strings.Contains(name, ".") {
		return name
	}
	if namespace == "" {
		return name
	}
	return namespace + "." + name
}

// qualifyAll qualifies every name in names, returning nil for an empty input so
// that an absent alias list stays absent.
func qualifyAll(namespace string, names []string) []string {
	if len(names) == 0 {
		return nil
	}
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = qualify(namespace, n)
	}
	return out
}

// namedDeclaration reports the simple name and declared namespace of a named
// IDL declaration.
func namedDeclaration(t idl.Type) (name, namespace string, ok bool) {
	switch v := t.(type) {
	case *idl.Record:
		return v.Name, v.Namespace, true
	case *idl.Enum:
		return v.Name, v.Namespace, true
	case *idl.Fixed:
		return v.Name, v.Namespace, true
	default:
		return "", "", false
	}
}

func mapToProtoIdent(id *idl.Ident) *avrocpb.Ident {
	if id == nil {
		return nil
	}
	return &avrocpb.Ident{
		Value: proto.String(id.Value),
	}
}
