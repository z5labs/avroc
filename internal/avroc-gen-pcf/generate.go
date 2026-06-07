// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgenpcf

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/z5labs/avro-go/canonical"
	"github.com/z5labs/avroc/internal/avrocpb"
)

type generatorService struct {
	avrocpb.UnimplementedGeneratorServer
}

// Generate implements the Generator gRPC service method, streaming each
// generated file back to avroc, which performs the filesystem writes.
func (s *generatorService) Generate(req *avrocpb.GenerateRequest, stream avrocpb.Generator_GenerateServer) error {
	for _, schema := range req.Schemas {
		filename, content, err := buildSchemaFile(schema)
		if err != nil {
			return fmt.Errorf("failed to generate schema: %w", err)
		}
		if err := sendFile(stream, filename, content); err != nil {
			return err
		}
	}

	return nil
}

// buildSchemaFile generates the Avro Parsing Canonical Form for a single schema,
// returning its relative filename and content.
func buildSchemaFile(schema *avrocpb.Schema) (string, []byte, error) {
	cs, err := schemaToCanonical(schema)
	if err != nil {
		return "", nil, fmt.Errorf("failed to convert schema to canonical form: %w", err)
	}

	data, err := json.Marshal(cs)
	if err != nil {
		return "", nil, fmt.Errorf("failed to marshal canonical schema: %w", err)
	}

	return schemaFilename(schema), data, nil
}

// maxChunkSize bounds each streamed GenerateResponse so messages stay well
// under gRPC's 4MB default MaxRecvMsgSize.
const maxChunkSize = 1 << 20 // 1 MiB

// sendFile streams content to avroc as one or more chunks sharing path, with
// last set on the final chunk. Empty content emits a single terminating chunk.
func sendFile(stream avrocpb.Generator_GenerateServer, path string, content []byte) error {
	for {
		n := min(len(content), maxChunkSize)
		chunk := content[:n]
		content = content[n:]
		last := len(content) == 0

		err := stream.Send(&avrocpb.GenerateResponse{
			Path:    &path,
			Content: chunk,
			Last:    &last,
		})
		if err != nil {
			return err
		}
		if last {
			return nil
		}
	}
}

// canonicalConverter converts protobuf schema types to the Avro Parsing Canonical Form.
// It tracks namespace context and which named types have already been defined,
// so that subsequent references emit only the fully-qualified name string.
// Both namedTypes and defined are keyed by fully-qualified type name.
type canonicalConverter struct {
	defaultNamespace string
	namedTypes       map[string]*avrocpb.Type
	defined          map[string]bool
}

// schemaToCanonical converts a protobuf Schema to its canonical.Schema representation.
func schemaToCanonical(schema *avrocpb.Schema) (canonical.Schema, error) {
	c := &canonicalConverter{
		defaultNamespace: schema.GetNamespace(),
		namedTypes:       make(map[string]*avrocpb.Type),
		defined:          make(map[string]bool),
	}

	for _, t := range schema.GetTypes() {
		name := namedTypeName(t)
		if name == "" {
			continue
		}
		ns := namedTypeNamespace(t)
		fqName := qualifyName(c.defaultNamespace, ns, name)
		c.namedTypes[fqName] = t
	}

	return c.typeToCanonical(schema.Type)
}

// typeToCanonical converts a protobuf Type to its canonical.Schema representation.
func (c *canonicalConverter) typeToCanonical(t *avrocpb.Type) (canonical.Schema, error) {
	if t == nil {
		return canonical.Schema{}, fmt.Errorf("nil type")
	}

	switch v := t.Type.(type) {
	case *avrocpb.Type_Record:
		return c.recordToCanonical(v.Record)
	case *avrocpb.Type_EnumType:
		return c.enumToCanonical(v.EnumType), nil
	case *avrocpb.Type_Fixed:
		return c.fixedToCanonical(v.Fixed), nil
	case *avrocpb.Type_Array:
		return c.arrayToCanonical(v.Array)
	case *avrocpb.Type_MapType:
		return c.mapToCanonical(v.MapType)
	case *avrocpb.Type_Union:
		return c.unionToCanonical(v.Union)
	case *avrocpb.Type_Ident:
		return c.identToCanonical(v.Ident)
	default:
		return canonical.Schema{}, fmt.Errorf("unsupported type: %T", t.Type)
	}
}

func (c *canonicalConverter) recordToCanonical(r *avrocpb.Record) (canonical.Schema, error) {
	fqName := qualifyName(c.defaultNamespace, r.GetNamespace(), r.GetName())
	c.defined[fqName] = true

	fields := make([]canonical.Field, 0, len(r.GetFields()))
	for _, f := range r.GetFields() {
		ft, err := c.typeToCanonical(f.GetType())
		if err != nil {
			return canonical.Schema{}, fmt.Errorf("field %q: %w", f.GetName(), err)
		}
		fields = append(fields, canonical.Field{
			Name: f.GetName(),
			Type: ft,
		})
	}

	return canonical.RecordSchema(canonical.Record{
		Name:   fqName,
		Fields: fields,
	}), nil
}

func (c *canonicalConverter) enumToCanonical(e *avrocpb.Enum) canonical.Schema {
	fqName := qualifyName(c.defaultNamespace, e.GetNamespace(), e.GetName())
	c.defined[fqName] = true

	symbols := make([]string, 0, len(e.GetValues()))
	for _, v := range e.GetValues() {
		symbols = append(symbols, v.GetValue())
	}

	return canonical.EnumSchema(canonical.Enum{
		Name:    fqName,
		Symbols: symbols,
	})
}

func (c *canonicalConverter) fixedToCanonical(f *avrocpb.Fixed) canonical.Schema {
	fqName := qualifyName(c.defaultNamespace, f.GetNamespace(), f.GetName())
	c.defined[fqName] = true

	return canonical.FixedSchema(canonical.Fixed{
		Name: fqName,
		Size: int(f.GetSize()),
	})
}

func (c *canonicalConverter) arrayToCanonical(a *avrocpb.Array) (canonical.Schema, error) {
	items, err := c.typeToCanonical(a.GetItems())
	if err != nil {
		return canonical.Schema{}, err
	}
	return canonical.ArraySchema(canonical.Array{Items: items}), nil
}

func (c *canonicalConverter) mapToCanonical(m *avrocpb.Map) (canonical.Schema, error) {
	values, err := c.identToCanonical(m.GetValues())
	if err != nil {
		return canonical.Schema{}, err
	}
	return canonical.MapSchema(canonical.Map{Values: values}), nil
}

func (c *canonicalConverter) unionToCanonical(u *avrocpb.Union) (canonical.Schema, error) {
	types := make(canonical.Union, 0, len(u.GetTypes()))
	for _, t := range u.GetTypes() {
		s, err := c.typeToCanonical(t)
		if err != nil {
			return canonical.Schema{}, err
		}
		types = append(types, s)
	}
	return canonical.UnionSchema(types), nil
}

// identToCanonical resolves an identifier. Avro primitive types are returned
// as-is. Named types that have not yet been defined are inlined on first use.
// Already-defined named types are returned as their fully-qualified name.
func (c *canonicalConverter) identToCanonical(ident *avrocpb.Ident) (canonical.Schema, error) {
	name := ident.GetValue()

	// Avro primitive types are returned directly
	if isPrimitive(name) {
		return canonical.PrimitiveSchema(canonical.Primitive(name)), nil
	}

	// Compute the FQ name; namedTypes and defined are both keyed by FQ name
	fqName := qualifyName(c.defaultNamespace, "", name)

	if t, ok := c.namedTypes[fqName]; ok && !c.defined[fqName] {
		return c.typeToCanonical(t)
	}

	// Already-defined named type: return as FQ name reference
	return canonical.PrimitiveSchema(canonical.Primitive(fqName)), nil
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

// qualifyName returns the fully qualified name combining namespace and name.
// If name already contains a dot, it is returned as-is. Otherwise the
// effective namespace (typeNamespace falling back to defaultNamespace) is prepended.
func qualifyName(defaultNamespace, typeNamespace, name string) string {
	if strings.Contains(name, ".") {
		return name
	}
	ns := typeNamespace
	if ns == "" {
		ns = defaultNamespace
	}
	if ns != "" {
		return ns + "." + name
	}
	return name
}

// namedTypeName extracts the bare name from a named type.
func namedTypeName(t *avrocpb.Type) string {
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

// namedTypeNamespace extracts the namespace from a named type.
func namedTypeNamespace(t *avrocpb.Type) string {
	if t == nil {
		return ""
	}
	switch v := t.Type.(type) {
	case *avrocpb.Type_Record:
		return v.Record.GetNamespace()
	case *avrocpb.Type_EnumType:
		return v.EnumType.GetNamespace()
	case *avrocpb.Type_Fixed:
		return v.Fixed.GetNamespace()
	default:
		return ""
	}
}

// schemaFilename determines the output filename for a schema.
func schemaFilename(schema *avrocpb.Schema) string {
	if schema.Type != nil {
		if name := namedTypeName(schema.Type); name != "" {
			return toSnakeCase(name) + ".avsc"
		}
		if ident, ok := schema.Type.Type.(*avrocpb.Type_Ident); ok {
			return toSnakeCase(ident.Ident.GetValue()) + ".avsc"
		}
	}

	if len(schema.Types) > 0 {
		if name := namedTypeName(schema.Types[0]); name != "" {
			return toSnakeCase(name) + ".avsc"
		}
	}

	if ns := schema.GetNamespace(); ns != "" {
		parts := strings.Split(ns, ".")
		return toSnakeCase(parts[len(parts)-1]) + ".avsc"
	}

	return "schema.avsc"
}

// toSnakeCase converts a string to snake_case.
func toSnakeCase(s string) string {
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
