// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"fmt"
	"strings"

	"github.com/z5labs/avroc/internal/avrocpb"
)

// canonicalConverter converts protobuf schema types to the Avro Parsing Canonical Form.
// It tracks namespace context and which named types have already been defined,
// so that subsequent references emit only the fully-qualified name string.
type canonicalConverter struct {
	defaultNamespace string
	namedTypes       map[string]*avrocpb.Type
	defined          map[string]bool
}

// canonicalForm returns the Parsing Canonical Form JSON string for a schema's
// primary type. The result is suitable for computing a CRC-64-AVRO fingerprint.
func canonicalForm(schema *avrocpb.Schema) string {
	c := &canonicalConverter{
		defaultNamespace: schema.GetNamespace(),
		namedTypes:       make(map[string]*avrocpb.Type),
		defined:          make(map[string]bool),
	}

	for _, t := range schema.GetTypes() {
		if name := canonicalTypeName(t); name != "" {
			c.namedTypes[name] = t
		}
	}

	var sb strings.Builder
	c.typeCanonical(&sb, schema.Type)
	return sb.String()
}

// typeCanonical writes the canonical form of a type to the builder.
func (c *canonicalConverter) typeCanonical(sb *strings.Builder, t *avrocpb.Type) {
	if t == nil {
		return
	}

	switch v := t.Type.(type) {
	case *avrocpb.Type_Record:
		c.recordCanonical(sb, v.Record)
	case *avrocpb.Type_EnumType:
		c.enumCanonical(sb, v.EnumType)
	case *avrocpb.Type_Fixed:
		c.fixedCanonical(sb, v.Fixed)
	case *avrocpb.Type_Array:
		c.arrayCanonical(sb, v.Array)
	case *avrocpb.Type_MapType:
		c.mapCanonical(sb, v.MapType)
	case *avrocpb.Type_Union:
		c.unionCanonical(sb, v.Union)
	case *avrocpb.Type_Ident:
		c.identCanonical(sb, v.Ident)
	}
}

// recordCanonical writes: {"name":"fq.Name","type":"record","fields":[...]}
func (c *canonicalConverter) recordCanonical(sb *strings.Builder, r *avrocpb.Record) {
	fqName := c.fullyQualifiedName(r.GetName(), r.GetNamespace())
	c.defined[r.GetName()] = true

	sb.WriteString(`{"name":"`)
	sb.WriteString(fqName)
	sb.WriteString(`","type":"record","fields":[`)

	for i, f := range r.GetFields() {
		if i > 0 {
			sb.WriteByte(',')
		}
		c.fieldCanonical(sb, f)
	}

	sb.WriteString(`]}`)
}

// fieldCanonical writes: {"name":"fieldname","type":...}
func (c *canonicalConverter) fieldCanonical(sb *strings.Builder, f *avrocpb.Field) {
	sb.WriteString(`{"name":"`)
	sb.WriteString(f.GetName())
	sb.WriteString(`","type":`)
	c.typeCanonical(sb, f.GetType())
	sb.WriteByte('}')
}

// enumCanonical writes: {"name":"fq.Name","type":"enum","symbols":["A","B",...]}
func (c *canonicalConverter) enumCanonical(sb *strings.Builder, e *avrocpb.Enum) {
	fqName := c.fullyQualifiedName(e.GetName(), e.GetNamespace())
	c.defined[e.GetName()] = true

	sb.WriteString(`{"name":"`)
	sb.WriteString(fqName)
	sb.WriteString(`","type":"enum","symbols":[`)

	for i, v := range e.GetValues() {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteByte('"')
		sb.WriteString(v.GetValue())
		sb.WriteByte('"')
	}

	sb.WriteString(`]}`)
}

// fixedCanonical writes: {"name":"fq.Name","type":"fixed","size":N}
func (c *canonicalConverter) fixedCanonical(sb *strings.Builder, f *avrocpb.Fixed) {
	fqName := c.fullyQualifiedName(f.GetName(), f.GetNamespace())
	c.defined[f.GetName()] = true

	sb.WriteString(`{"name":"`)
	sb.WriteString(fqName)
	sb.WriteString(`","type":"fixed","size":`)
	fmt.Fprintf(sb, "%d", f.GetSize())
	sb.WriteByte('}')
}

// arrayCanonical writes: {"type":"array","items":...}
func (c *canonicalConverter) arrayCanonical(sb *strings.Builder, a *avrocpb.Array) {
	sb.WriteString(`{"type":"array","items":`)
	c.typeCanonical(sb, a.GetItems())
	sb.WriteByte('}')
}

// mapCanonical writes: {"type":"map","values":...}
func (c *canonicalConverter) mapCanonical(sb *strings.Builder, m *avrocpb.Map) {
	sb.WriteString(`{"type":"map","values":`)
	c.identCanonical(sb, m.GetValues())
	sb.WriteByte('}')
}

// unionCanonical writes: [type1,type2,...]
func (c *canonicalConverter) unionCanonical(sb *strings.Builder, u *avrocpb.Union) {
	sb.WriteByte('[')
	for i, t := range u.GetTypes() {
		if i > 0 {
			sb.WriteByte(',')
		}
		c.typeCanonical(sb, t)
	}
	sb.WriteByte(']')
}

// identCanonical writes the canonical form of an identifier.
// Primitive types are emitted as quoted strings. Named types that have not
// yet been defined are resolved and inlined; already-defined named types
// are emitted as their fully-qualified name string.
func (c *canonicalConverter) identCanonical(sb *strings.Builder, ident *avrocpb.Ident) {
	name := ident.GetValue()

	if isPrimitive(name) {
		sb.WriteByte('"')
		sb.WriteString(name)
		sb.WriteByte('"')
		return
	}

	// Named type reference: inline on first use, name-reference on subsequent.
	if t, ok := c.namedTypes[name]; ok && !c.defined[name] {
		c.typeCanonical(sb, t)
		return
	}

	// Already defined or unknown: emit as FQ name string.
	fqName := c.fullyQualifiedName(name, "")
	sb.WriteByte('"')
	sb.WriteString(fqName)
	sb.WriteByte('"')
}

// fullyQualifiedName returns the fully qualified name for a type.
// If the name already contains a dot, it is already fully qualified.
// Otherwise, the type's own namespace is used if non-empty, falling back
// to the schema's default namespace.
func (c *canonicalConverter) fullyQualifiedName(name string, namespace string) string {
	if strings.Contains(name, ".") {
		return name
	}

	ns := namespace
	if ns == "" {
		ns = c.defaultNamespace
	}
	if ns != "" {
		return ns + "." + name
	}
	return name
}

// canonicalTypeName extracts the bare name from a named type.
func canonicalTypeName(t *avrocpb.Type) string {
	if t == nil {
		return ""
	}
	switch v := t.Type.(type) {
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

// isPrimitive returns true if the name is an Avro primitive type.
func isPrimitive(name string) bool {
	switch name {
	case "null", "boolean", "int", "long", "float", "double", "bytes", "string":
		return true
	default:
		return false
	}
}
