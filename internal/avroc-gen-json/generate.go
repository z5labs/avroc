// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgenjson

import (
	"encoding/json"
	"fmt"

	"github.com/z5labs/avroc/internal/avrocpb"
	"github.com/z5labs/avroc/internal/ir"
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
	if err := ir.Validate(schema); err != nil {
		return "", nil, err
	}

	jsonSchema, err := schemaToJSON(schema)
	if err != nil {
		return "", nil, err
	}

	data, err := json.MarshalIndent(jsonSchema, "", "  ")
	if err != nil {
		return "", nil, fmt.Errorf("failed to marshal JSON schema: %w", err)
	}

	return ir.SnakeCase(ir.SchemaBaseName(schema)) + ".avsc", append(data, '\n'), nil
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

// schemaToJSON converts a resolved schema to its Avro JSON schema
// representation.
//
// It is a plain walk. The producer has already qualified every name, decided
// which named type is written out where, and stated whether a reference names a
// primitive or a named type, so there is no symbol table here and no state
// carried between types.
func schemaToJSON(schema *avrocpb.Schema) (any, error) {
	return typeToJSON(schema.GetType())
}

// typeToJSON converts a resolved Type to its Avro JSON schema representation.
func typeToJSON(t *avrocpb.Type) (any, error) {
	if t == nil {
		// An absent type is not an empty schema; emitting JSON null here would
		// write a .avsc no Avro reader accepts.
		return nil, fmt.Errorf("nil type")
	}

	switch v := t.GetType().(type) {
	case *avrocpb.Type_Record:
		return recordToJSON(v.Record)
	case *avrocpb.Type_EnumType:
		return enumToJSON(v.EnumType), nil
	case *avrocpb.Type_Array:
		return arrayToJSON(v.Array)
	case *avrocpb.Type_MapType:
		return mapToJSON(v.MapType)
	case *avrocpb.Type_Union:
		return unionToJSON(v.Union)
	case *avrocpb.Type_Fixed:
		return fixedToJSON(v.Fixed), nil
	case *avrocpb.Type_Reference:
		return referenceToJSON(v.Reference)
	default:
		// A type constructor is a closed set: an unrecognised member is a
		// schema this generator cannot represent, not one to skip.
		return nil, fmt.Errorf("unsupported type: %T", t.GetType())
	}
}

// referenceToJSON renders a reference as the name Avro's JSON schema calls for:
// the primitive's name, or the named type's fully-qualified name. Both members
// of the kind are written the same way; the switch is here because the kind is
// a closed set and an unrecognised member means a schema this generator has
// misread.
func referenceToJSON(ref *avrocpb.Reference) (any, error) {
	switch ref.GetKind() {
	case avrocpb.TypeRefKind_TYPE_REF_KIND_PRIMITIVE, avrocpb.TypeRefKind_TYPE_REF_KIND_NAMED:
		return ref.GetName(), nil
	default:
		return nil, fmt.Errorf("reference %q has unrecognised kind %v", ref.GetName(), ref.GetKind())
	}
}

func recordToJSON(r *avrocpb.Record) (avroRecord, error) {
	fields := make([]avroField, 0, len(r.GetFields()))
	for _, f := range r.GetFields() {
		jf, err := fieldToJSON(f)
		if err != nil {
			return avroRecord{}, fmt.Errorf("field %q: %w", f.GetName(), err)
		}
		fields = append(fields, jf)
	}

	return avroRecord{
		Type:      "record",
		Name:      r.GetName(),
		Namespace: r.GetNamespace(),
		Aliases:   r.GetAliases(),
		Fields:    fields,
	}, nil
}

func fieldToJSON(f *avrocpb.Field) (avroField, error) {
	var order string
	switch f.GetSortOrder() {
	case avrocpb.SortOrder_SORT_ORDER_DESC:
		order = "descending"
	case avrocpb.SortOrder_SORT_ORDER_IGNORE:
		order = "ignore"
	}

	t, err := typeToJSON(f.GetType())
	if err != nil {
		return avroField{}, err
	}

	return avroField{
		Name:    f.GetName(),
		Type:    t,
		Aliases: f.GetAliases(),
		Order:   order,
	}, nil
}

func enumToJSON(e *avrocpb.Enum) avroEnum {
	symbols := make([]string, 0, len(e.GetValues()))
	for _, v := range e.GetValues() {
		symbols = append(symbols, v.GetValue())
	}

	return avroEnum{
		Type:      "enum",
		Name:      e.GetName(),
		Namespace: e.GetNamespace(),
		Aliases:   e.GetAliases(),
		Symbols:   symbols,
		Default:   e.GetDefault().GetValue(),
	}
}

func arrayToJSON(a *avrocpb.Array) (avroArray, error) {
	items, err := typeToJSON(a.GetItems())
	if err != nil {
		return avroArray{}, err
	}
	return avroArray{
		Type:  "array",
		Items: items,
	}, nil
}

func mapToJSON(m *avrocpb.Map) (avroMap, error) {
	values, err := typeToJSON(m.GetValues())
	if err != nil {
		return avroMap{}, err
	}
	return avroMap{
		Type:   "map",
		Values: values,
	}, nil
}

func unionToJSON(u *avrocpb.Union) ([]any, error) {
	types := make([]any, 0, len(u.GetTypes()))
	for _, t := range u.GetTypes() {
		jt, err := typeToJSON(t)
		if err != nil {
			return nil, err
		}
		types = append(types, jt)
	}
	return types, nil
}

func fixedToJSON(f *avrocpb.Fixed) avroFixed {
	return avroFixed{
		Type:      "fixed",
		Name:      f.GetName(),
		Namespace: f.GetNamespace(),
		Aliases:   f.GetAliases(),
		Size:      f.GetSize(),
	}
}
