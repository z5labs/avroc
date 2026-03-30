// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgenpcf

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/z5labs/avroc/internal/avrocpb"
	"github.com/z5labs/avro-go/canonical"
)

type generatorService struct {
	avrocpb.UnimplementedGeneratorServer
}

// Generate implements the Generator gRPC service method.
func (s *generatorService) Generate(ctx context.Context, req *avrocpb.GenerateRequest) (*avrocpb.GenerateResponse, error) {
	outputDir := req.GetOutputDirectory()
	if outputDir == "" {
		return nil, fmt.Errorf("output directory is required")
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	var outputFiles []string

	for _, schema := range req.Schemas {
		filename, err := generateSchemaFile(outputDir, schema)
		if err != nil {
			return nil, fmt.Errorf("failed to generate schema: %w", err)
		}
		outputFiles = append(outputFiles, filename)
	}

	return &avrocpb.GenerateResponse{
		OutputFiles: outputFiles,
	}, nil
}

// generateSchemaFile generates an Avro Parsing Canonical Form file for a single schema.
func generateSchemaFile(outputDir string, schema *avrocpb.Schema) (string, error) {
	cs, err := schemaToCanonical(schema)
	if err != nil {
		return "", fmt.Errorf("failed to convert schema to canonical form: %w", err)
	}

	data, err := json.Marshal(cs)
	if err != nil {
		return "", fmt.Errorf("failed to marshal canonical schema: %w", err)
	}

	filename := schemaFilename(schema)
	outputPath := filepath.Join(outputDir, filename)

	if err := os.WriteFile(outputPath, append(data, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("failed to write file %s: %w", outputPath, err)
	}

	return outputPath, nil
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
		return c.identToCanonical(v.Ident), nil
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
	values := c.identToCanonical(m.GetValues())
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
func (c *canonicalConverter) identToCanonical(ident *avrocpb.Ident) canonical.Schema {
	name := ident.GetValue()

	// Avro primitive types are returned directly
	if isPrimitive(name) {
		return canonical.PrimitiveSchema(canonical.Primitive(name))
	}

	// Compute the FQ name; namedTypes and defined are both keyed by FQ name
	fqName := qualifyName(c.defaultNamespace, "", name)

	if t, ok := c.namedTypes[fqName]; ok && !c.defined[fqName] {
		if s, err := c.typeToCanonical(t); err == nil {
			return s
		}
	}

	// Already-defined named type: return as FQ name reference
	return canonical.PrimitiveSchema(canonical.Primitive(fqName))
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
