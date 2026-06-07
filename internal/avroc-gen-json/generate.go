// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgenjson

import (
	"encoding/json"
	"fmt"
	"strings"

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

// buildSchemaFile generates the Avro JSON schema for a single schema, returning
// its relative filename and content.
func buildSchemaFile(schema *avrocpb.Schema) (string, []byte, error) {
	jsonSchema := schemaToJSON(schema)

	data, err := json.MarshalIndent(jsonSchema, "", "  ")
	if err != nil {
		return "", nil, fmt.Errorf("failed to marshal JSON schema: %w", err)
	}

	return schemaFilename(schema), append(data, '\n'), nil
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

// schemaConverter holds state for converting a protobuf Schema to Avro JSON,
// tracking named type definitions so they can be inlined on first use and
// referenced by name on subsequent uses.
type schemaConverter struct {
	namespace  string
	namedTypes map[string]*avrocpb.Type
	defined    map[string]bool
}

func newSchemaConverter(schema *avrocpb.Schema) *schemaConverter {
	c := &schemaConverter{
		namespace:  schema.GetNamespace(),
		namedTypes: make(map[string]*avrocpb.Type),
		defined:    make(map[string]bool),
	}
	for _, t := range schema.GetTypes() {
		if name := typeName(t); name != "" {
			c.namedTypes[name] = t
		}
	}
	return c
}

// schemaToJSON converts a protobuf Schema to its Avro JSON schema representation.
func schemaToJSON(schema *avrocpb.Schema) any {
	c := newSchemaConverter(schema)

	if schema.Type != nil {
		return c.typeToJSON(schema.Type)
	}
	return nil
}

// typeToJSON converts a protobuf Type to its Avro JSON schema representation.
func (c *schemaConverter) typeToJSON(t *avrocpb.Type) any {
	if t == nil {
		return nil
	}

	switch v := t.Type.(type) {
	case *avrocpb.Type_Record:
		return c.recordToJSON(v.Record)
	case *avrocpb.Type_EnumType:
		return c.enumToJSON(v.EnumType)
	case *avrocpb.Type_Array:
		return c.arrayToJSON(v.Array)
	case *avrocpb.Type_MapType:
		return mapToJSON(v.MapType)
	case *avrocpb.Type_Union:
		return c.unionToJSON(v.Union)
	case *avrocpb.Type_Fixed:
		return c.fixedToJSON(v.Fixed)
	case *avrocpb.Type_Ident:
		return c.identToJSON(v.Ident)
	default:
		return nil
	}
}

// identToJSON resolves an identifier. Primitive types are returned as strings.
// Named types are inlined on first use and referenced by name afterwards.
func (c *schemaConverter) identToJSON(ident *avrocpb.Ident) any {
	name := ident.GetValue()

	// Check if it's a named type we can resolve
	if t, ok := c.namedTypes[name]; ok && !c.defined[name] {
		c.defined[name] = true
		return c.typeToJSON(t)
	}

	// Primitive type or already-defined named type: return as string
	return name
}

// avroRecord represents an Avro record type in JSON schema format.
type avroRecord struct {
	Type      string      `json:"type"`
	Name      string      `json:"name"`
	Namespace string      `json:"namespace,omitempty"`
	Aliases   []string    `json:"aliases,omitempty"`
	Fields    []avroField `json:"fields"`
}

// avroField represents an Avro record field in JSON schema format.
type avroField struct {
	Name    string   `json:"name"`
	Type    any      `json:"type"`
	Aliases []string `json:"aliases,omitempty"`
	Order   string   `json:"order,omitempty"`
}

// avroEnum represents an Avro enum type in JSON schema format.
type avroEnum struct {
	Type      string   `json:"type"`
	Name      string   `json:"name"`
	Namespace string   `json:"namespace,omitempty"`
	Aliases   []string `json:"aliases,omitempty"`
	Symbols   []string `json:"symbols"`
	Default   string   `json:"default,omitempty"`
}

// avroArray represents an Avro array type in JSON schema format.
type avroArray struct {
	Type  string `json:"type"`
	Items any    `json:"items"`
}

// avroMap represents an Avro map type in JSON schema format.
type avroMap struct {
	Type   string `json:"type"`
	Values any    `json:"values"`
}

// avroFixed represents an Avro fixed type in JSON schema format.
type avroFixed struct {
	Type      string   `json:"type"`
	Name      string   `json:"name"`
	Namespace string   `json:"namespace,omitempty"`
	Aliases   []string `json:"aliases,omitempty"`
	Size      int32    `json:"size"`
}

func (c *schemaConverter) recordToJSON(r *avrocpb.Record) avroRecord {
	ns := r.GetNamespace()
	if ns == "" {
		ns = c.namespace
	}

	fields := make([]avroField, 0, len(r.GetFields()))
	for _, f := range r.GetFields() {
		fields = append(fields, c.fieldToJSON(f))
	}

	return avroRecord{
		Type:      "record",
		Name:      r.GetName(),
		Namespace: ns,
		Aliases:   r.GetAliases(),
		Fields:    fields,
	}
}

func (c *schemaConverter) fieldToJSON(f *avrocpb.Field) avroField {
	var order string
	switch f.GetSortOrder() {
	case avrocpb.SortOrder_SORT_ORDER_DESC:
		order = "descending"
	case avrocpb.SortOrder_SORT_ORDER_IGNORE:
		order = "ignore"
	}

	return avroField{
		Name:    f.GetName(),
		Type:    c.typeToJSON(f.GetType()),
		Aliases: f.GetAliases(),
		Order:   order,
	}
}

func (c *schemaConverter) enumToJSON(e *avrocpb.Enum) avroEnum {
	ns := e.GetNamespace()
	if ns == "" {
		ns = c.namespace
	}

	symbols := make([]string, 0, len(e.GetValues()))
	for _, v := range e.GetValues() {
		symbols = append(symbols, v.GetValue())
	}

	var defaultVal string
	if d := e.GetDefault(); d != nil {
		defaultVal = d.GetValue()
	}

	return avroEnum{
		Type:      "enum",
		Name:      e.GetName(),
		Namespace: ns,
		Aliases:   e.GetAliases(),
		Symbols:   symbols,
		Default:   defaultVal,
	}
}

func (c *schemaConverter) arrayToJSON(a *avrocpb.Array) avroArray {
	return avroArray{
		Type:  "array",
		Items: c.typeToJSON(a.GetItems()),
	}
}

func mapToJSON(m *avrocpb.Map) avroMap {
	return avroMap{
		Type:   "map",
		Values: m.GetValues().GetValue(),
	}
}

func (c *schemaConverter) unionToJSON(u *avrocpb.Union) []any {
	types := make([]any, 0, len(u.GetTypes()))
	for _, t := range u.GetTypes() {
		types = append(types, c.typeToJSON(t))
	}
	return types
}

func (c *schemaConverter) fixedToJSON(f *avrocpb.Fixed) avroFixed {
	ns := f.GetNamespace()
	if ns == "" {
		ns = c.namespace
	}

	return avroFixed{
		Type:      "fixed",
		Name:      f.GetName(),
		Namespace: ns,
		Aliases:   f.GetAliases(),
		Size:      f.GetSize(),
	}
}

// schemaFilename determines the output filename for a schema.
func schemaFilename(schema *avrocpb.Schema) string {
	// Try to get a name from the primary type
	if schema.Type != nil {
		if name := typeName(schema.Type); name != "" {
			return toSnakeCase(name) + ".avsc"
		}
		// If primary type is an Ident, try resolving it from Types
		if ident, ok := schema.Type.Type.(*avrocpb.Type_Ident); ok {
			return toSnakeCase(ident.Ident.GetValue()) + ".avsc"
		}
	}

	// Try to get a name from the first type
	if len(schema.Types) > 0 {
		if name := typeName(schema.Types[0]); name != "" {
			return toSnakeCase(name) + ".avsc"
		}
	}

	// Fall back to namespace-based name
	if ns := schema.GetNamespace(); ns != "" {
		parts := strings.Split(ns, ".")
		return toSnakeCase(parts[len(parts)-1]) + ".avsc"
	}

	return "schema.avsc"
}

// typeName extracts the name from a type.
func typeName(t *avrocpb.Type) string {
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

// toSnakeCase converts a string to snake_case.
func toSnakeCase(s string) string {
	if s == "" {
		return ""
	}

	// Handle fully-qualified names
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
