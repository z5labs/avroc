// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgenjson

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/z5labs/avroc/internal/avrocpb"
	"google.golang.org/protobuf/proto"
)

// validateJSON parses the JSON and returns an error if it's invalid.
func validateJSON(t *testing.T, data []byte) {
	t.Helper()
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Errorf("generated output is not valid JSON: %v\n\nContent:\n%s", err, string(data))
	}
}

func TestGenerate_Record(t *testing.T) {
	tmpDir := t.TempDir()

	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name:      proto.String("Person"),
					Namespace: proto.String("com.example"),
					FullName:  proto.String("com.example.Person"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("name"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Reference{Reference: primRef("string")},
							},
						},
						{
							Name: proto.String("age"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Reference{Reference: primRef("int")},
							},
						},
					},
				},
			},
		},
	}

	svc := &generatorService{}
	resp, err := generateToDir(t, svc, tmpDir, &avrocpb.GenerateRequest{
		Schemas: []*avrocpb.Schema{schema},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if len(resp.OutputFiles) != 1 {
		t.Fatalf("expected 1 output file, got %d", len(resp.OutputFiles))
	}

	content, err := os.ReadFile(resp.OutputFiles[0])
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	validateJSON(t, content)

	var result map[string]any
	if err := json.Unmarshal(content, &result); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if result["type"] != "record" {
		t.Errorf("expected type 'record', got %v", result["type"])
	}
	if result["name"] != "Person" {
		t.Errorf("expected name 'Person', got %v", result["name"])
	}
	if result["namespace"] != "com.example" {
		t.Errorf("expected namespace 'com.example', got %v", result["namespace"])
	}

	fields, ok := result["fields"].([]any)
	if !ok {
		t.Fatalf("expected fields to be an array, got %T", result["fields"])
	}
	if len(fields) != 2 {
		t.Fatalf("expected 2 fields, got %d", len(fields))
	}

	field0 := fields[0].(map[string]any)
	if field0["name"] != "name" {
		t.Errorf("expected first field name 'name', got %v", field0["name"])
	}
	if field0["type"] != "string" {
		t.Errorf("expected first field type 'string', got %v", field0["type"])
	}

	field1 := fields[1].(map[string]any)
	if field1["name"] != "age" {
		t.Errorf("expected second field name 'age', got %v", field1["name"])
	}
	if field1["type"] != "int" {
		t.Errorf("expected second field type 'int', got %v", field1["type"])
	}
}

func TestGenerate_Enum(t *testing.T) {
	tmpDir := t.TempDir()

	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_EnumType{
				EnumType: &avrocpb.Enum{
					Name:      proto.String("Status"),
					Namespace: proto.String("com.example"),
					FullName:  proto.String("com.example.Status"),
					Values: []*avrocpb.Ident{
						{Value: proto.String("PENDING")},
						{Value: proto.String("ACTIVE")},
						{Value: proto.String("COMPLETED")},
					},
				},
			},
		},
	}

	svc := &generatorService{}
	resp, err := generateToDir(t, svc, tmpDir, &avrocpb.GenerateRequest{
		Schemas: []*avrocpb.Schema{schema},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(resp.OutputFiles[0])
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	validateJSON(t, content)

	var result map[string]any
	if err := json.Unmarshal(content, &result); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if result["type"] != "enum" {
		t.Errorf("expected type 'enum', got %v", result["type"])
	}
	if result["name"] != "Status" {
		t.Errorf("expected name 'Status', got %v", result["name"])
	}
	if result["namespace"] != "com.example" {
		t.Errorf("expected namespace 'com.example', got %v", result["namespace"])
	}

	symbols, ok := result["symbols"].([]any)
	if !ok {
		t.Fatalf("expected symbols to be an array, got %T", result["symbols"])
	}
	expectedSymbols := []string{"PENDING", "ACTIVE", "COMPLETED"}
	if len(symbols) != len(expectedSymbols) {
		t.Fatalf("expected %d symbols, got %d", len(expectedSymbols), len(symbols))
	}
	for i, exp := range expectedSymbols {
		if symbols[i] != exp {
			t.Errorf("expected symbol[%d] = %q, got %v", i, exp, symbols[i])
		}
	}
}

func TestGenerate_EnumWithDefault(t *testing.T) {
	tmpDir := t.TempDir()

	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_EnumType{
				EnumType: &avrocpb.Enum{
					Name:      proto.String("Color"),
					Namespace: proto.String("com.example"),
					FullName:  proto.String("com.example.Color"),
					Values: []*avrocpb.Ident{
						{Value: proto.String("RED")},
						{Value: proto.String("GREEN")},
						{Value: proto.String("BLUE")},
					},
					Default: &avrocpb.Ident{Value: proto.String("RED")},
				},
			},
		},
	}

	svc := &generatorService{}
	resp, err := generateToDir(t, svc, tmpDir, &avrocpb.GenerateRequest{
		Schemas: []*avrocpb.Schema{schema},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(resp.OutputFiles[0])
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	validateJSON(t, content)

	var result map[string]any
	if err := json.Unmarshal(content, &result); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if result["default"] != "RED" {
		t.Errorf("expected default 'RED', got %v", result["default"])
	}
}

func TestGenerate_Fixed(t *testing.T) {
	tmpDir := t.TempDir()

	size := int32(16)
	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Fixed{
				Fixed: &avrocpb.Fixed{
					Name:      proto.String("MD5"),
					Namespace: proto.String("com.example"),
					FullName:  proto.String("com.example.MD5"),
					Size:      &size,
				},
			},
		},
	}

	svc := &generatorService{}
	resp, err := generateToDir(t, svc, tmpDir, &avrocpb.GenerateRequest{
		Schemas: []*avrocpb.Schema{schema},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(resp.OutputFiles[0])
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	validateJSON(t, content)

	var result map[string]any
	if err := json.Unmarshal(content, &result); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if result["type"] != "fixed" {
		t.Errorf("expected type 'fixed', got %v", result["type"])
	}
	if result["name"] != "MD5" {
		t.Errorf("expected name 'MD5', got %v", result["name"])
	}
	if result["namespace"] != "com.example" {
		t.Errorf("expected namespace 'com.example', got %v", result["namespace"])
	}
	// JSON numbers are float64
	if result["size"] != float64(16) {
		t.Errorf("expected size 16, got %v", result["size"])
	}
}

func TestGenerate_Union(t *testing.T) {
	tmpDir := t.TempDir()

	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name:      proto.String("Event"),
					Namespace: proto.String("com.example"),
					FullName:  proto.String("com.example.Event"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("data"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Union{
									Union: &avrocpb.Union{
										Types: []*avrocpb.Type{
											{Type: &avrocpb.Type_Reference{Reference: primRef("null")}},
											{Type: &avrocpb.Type_Reference{Reference: primRef("string")}},
											{Type: &avrocpb.Type_Reference{Reference: primRef("int")}},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	svc := &generatorService{}
	resp, err := generateToDir(t, svc, tmpDir, &avrocpb.GenerateRequest{
		Schemas: []*avrocpb.Schema{schema},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(resp.OutputFiles[0])
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	validateJSON(t, content)

	var result map[string]any
	if err := json.Unmarshal(content, &result); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	fields := result["fields"].([]any)
	field0 := fields[0].(map[string]any)
	unionTypes, ok := field0["type"].([]any)
	if !ok {
		t.Fatalf("expected union type to be an array, got %T", field0["type"])
	}

	expectedTypes := []string{"null", "string", "int"}
	if len(unionTypes) != len(expectedTypes) {
		t.Fatalf("expected %d union types, got %d", len(expectedTypes), len(unionTypes))
	}
	for i, exp := range expectedTypes {
		if unionTypes[i] != exp {
			t.Errorf("expected union type[%d] = %q, got %v", i, exp, unionTypes[i])
		}
	}
}

func TestGenerate_Array(t *testing.T) {
	tmpDir := t.TempDir()

	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name:      proto.String("Numbers"),
					Namespace: proto.String("com.example"),
					FullName:  proto.String("com.example.Numbers"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("values"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Array{
									Array: &avrocpb.Array{
										Items: &avrocpb.Type{
											Type: &avrocpb.Type_Reference{Reference: primRef("int")},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	svc := &generatorService{}
	resp, err := generateToDir(t, svc, tmpDir, &avrocpb.GenerateRequest{
		Schemas: []*avrocpb.Schema{schema},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(resp.OutputFiles[0])
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	validateJSON(t, content)

	var result map[string]any
	if err := json.Unmarshal(content, &result); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	fields := result["fields"].([]any)
	field0 := fields[0].(map[string]any)
	arrayType, ok := field0["type"].(map[string]any)
	if !ok {
		t.Fatalf("expected array type to be an object, got %T", field0["type"])
	}

	if arrayType["type"] != "array" {
		t.Errorf("expected array type 'array', got %v", arrayType["type"])
	}
	if arrayType["items"] != "int" {
		t.Errorf("expected array items 'int', got %v", arrayType["items"])
	}
}

func TestGenerate_Map(t *testing.T) {
	tmpDir := t.TempDir()

	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name:      proto.String("Config"),
					Namespace: proto.String("com.example"),
					FullName:  proto.String("com.example.Config"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("settings"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_MapType{
									MapType: &avrocpb.Map{
										Values: primType("string"),
									},
								},
							},
						},
					},
				},
			},
		},
	}

	svc := &generatorService{}
	resp, err := generateToDir(t, svc, tmpDir, &avrocpb.GenerateRequest{
		Schemas: []*avrocpb.Schema{schema},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(resp.OutputFiles[0])
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	validateJSON(t, content)

	var result map[string]any
	if err := json.Unmarshal(content, &result); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	fields := result["fields"].([]any)
	field0 := fields[0].(map[string]any)
	mapType, ok := field0["type"].(map[string]any)
	if !ok {
		t.Fatalf("expected map type to be an object, got %T", field0["type"])
	}

	if mapType["type"] != "map" {
		t.Errorf("expected map type 'map', got %v", mapType["type"])
	}
	if mapType["values"] != "string" {
		t.Errorf("expected map values 'string', got %v", mapType["values"])
	}
}

func TestGenerate_RecordWithAliases(t *testing.T) {
	tmpDir := t.TempDir()

	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name:      proto.String("LongList"),
					Namespace: proto.String("com.example"),
					FullName:  proto.String("com.example.LongList"),
					Aliases:   []string{"LinkedLongs"},
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("value"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Reference{Reference: primRef("long")},
							},
						},
					},
				},
			},
		},
	}

	svc := &generatorService{}
	resp, err := generateToDir(t, svc, tmpDir, &avrocpb.GenerateRequest{
		Schemas: []*avrocpb.Schema{schema},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(resp.OutputFiles[0])
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	validateJSON(t, content)

	var result map[string]any
	if err := json.Unmarshal(content, &result); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	aliases, ok := result["aliases"].([]any)
	if !ok {
		t.Fatalf("expected aliases to be an array, got %T", result["aliases"])
	}
	if len(aliases) != 1 || aliases[0] != "LinkedLongs" {
		t.Errorf("expected aliases [LinkedLongs], got %v", aliases)
	}
}

func TestBuildSchemaFile_Filename(t *testing.T) {
	filename, _, err := buildSchemaFile(resolvedTestRecord())
	if err != nil {
		t.Fatalf("buildSchemaFile failed: %v", err)
	}
	if filename != "test_record.avsc" {
		t.Errorf("expected test_record.avsc, got %q", filename)
	}
}

// TestGenerate_RecordEmitsResolvedNamespace asserts the generator writes the
// namespace the IR carries. It never falls back to the schema's namespace: a
// record that inherited one had that resolved before the descriptor was
// written.
func TestGenerate_RecordEmitsResolvedNamespace(t *testing.T) {
	tmpDir := t.TempDir()

	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name:      proto.String("Person"),
					Namespace: proto.String("com.example"),
					FullName:  proto.String("com.example.Person"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("name"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Reference{Reference: primRef("string")},
							},
						},
					},
				},
			},
		},
	}

	svc := &generatorService{}
	resp, err := generateToDir(t, svc, tmpDir, &avrocpb.GenerateRequest{
		Schemas: []*avrocpb.Schema{schema},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(resp.OutputFiles[0])
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(content, &result); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if result["namespace"] != "com.example" {
		t.Errorf("expected namespace 'com.example', got %v", result["namespace"])
	}
}

func TestGenerate_RecordWithOwnNamespace(t *testing.T) {
	tmpDir := t.TempDir()

	// Record with its own namespace should use it instead of schema namespace
	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name:      proto.String("Person"),
					Namespace: proto.String("com.other"),
					FullName:  proto.String("com.other.Person"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("name"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Reference{Reference: primRef("string")},
							},
						},
					},
				},
			},
		},
	}

	svc := &generatorService{}
	resp, err := generateToDir(t, svc, tmpDir, &avrocpb.GenerateRequest{
		Schemas: []*avrocpb.Schema{schema},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(resp.OutputFiles[0])
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(content, &result); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if result["namespace"] != "com.other" {
		t.Errorf("expected namespace 'com.other', got %v", result["namespace"])
	}
}

// TestGenerate_HonoursProducerOrdering feeds the generator a resolved schema —
// the enum and the fixed written out in full at their first use, and a
// fully-qualified reference at the second — and asserts the JSON follows that
// ordering. The generator keeps no record of what it has already emitted.
func TestGenerate_HonoursProducerOrdering(t *testing.T) {
	tmpDir := t.TempDir()

	svc := &generatorService{}
	resp, err := generateToDir(t, svc, tmpDir, &avrocpb.GenerateRequest{
		Schemas: []*avrocpb.Schema{resolvedTestRecord()},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if len(resp.OutputFiles) != 1 {
		t.Fatalf("expected 1 output file, got %d", len(resp.OutputFiles))
	}

	content, err := os.ReadFile(resp.OutputFiles[0])
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	validateJSON(t, content)

	var result map[string]any
	if err := json.Unmarshal(content, &result); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if result["type"] != "record" {
		t.Errorf("expected type 'record', got %v", result["type"])
	}
	if result["name"] != "TestRecord" {
		t.Errorf("expected name 'TestRecord', got %v", result["name"])
	}
	if result["namespace"] != "org.apache.avro.test" {
		t.Errorf("expected namespace 'org.apache.avro.test', got %v", result["namespace"])
	}

	fields := result["fields"].([]any)
	if len(fields) != 4 {
		t.Fatalf("expected 4 fields, got %d", len(fields))
	}

	// "name" field is a primitive reference.
	field0 := fields[0].(map[string]any)
	if field0["type"] != "string" {
		t.Errorf("expected field 'name' type 'string', got %v", field0["type"])
	}

	// "kind" carries the enum's definition: its first use.
	field1 := fields[1].(map[string]any)
	kindType, ok := field1["type"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'kind' field type to be an inlined enum object, got %T: %v", field1["type"], field1["type"])
	}
	if kindType["type"] != "enum" {
		t.Errorf("expected kind type 'enum', got %v", kindType["type"])
	}
	if kindType["name"] != "Kind" {
		t.Errorf("expected kind name 'Kind', got %v", kindType["name"])
	}

	// "hash" carries the fixed's definition: its first use.
	field2 := fields[2].(map[string]any)
	hashType, ok := field2["type"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'hash' field type to be an inlined fixed object, got %T: %v", field2["type"], field2["type"])
	}
	if hashType["type"] != "fixed" {
		t.Errorf("expected hash type 'fixed', got %v", hashType["type"])
	}
	if hashType["size"] != float64(16) {
		t.Errorf("expected hash size 16, got %v", hashType["size"])
	}

	// "nullableHash" refers to the fixed by fully-qualified name: a later use.
	field3 := fields[3].(map[string]any)
	unionTypes, ok := field3["type"].([]any)
	if !ok {
		t.Fatalf("expected 'nullableHash' type to be a union array, got %T", field3["type"])
	}
	if len(unionTypes) != 2 {
		t.Fatalf("expected 2 union types, got %d", len(unionTypes))
	}
	if unionTypes[0] != "null" {
		t.Errorf("expected union[0] = 'null', got %v", unionTypes[0])
	}
	if unionTypes[1] != "org.apache.avro.test.MD5" {
		t.Errorf("expected union[1] = 'org.apache.avro.test.MD5', got %v", unionTypes[1])
	}

	expectedFilename := filepath.Join(tmpDir, "test_record.avsc")
	if resp.OutputFiles[0] != expectedFilename {
		t.Errorf("expected output file %q, got %q", expectedFilename, resp.OutputFiles[0])
	}
}

// TestGenerate_UnrecognisedReferenceKind proves the generator fails on a
// reference whose kind it does not recognise rather than guessing at it.
func TestGenerate_UnrecognisedReferenceKind(t *testing.T) {
	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name:      proto.String("Person"),
					Namespace: proto.String("com.example"),
					FullName:  proto.String("com.example.Person"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("name"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Reference{
									Reference: &avrocpb.Reference{Name: proto.String("string")},
								},
							},
						},
					},
				},
			},
		},
	}

	svc := &generatorService{}
	_, err := generateToDir(t, svc, t.TempDir(), &avrocpb.GenerateRequest{
		Schemas: []*avrocpb.Schema{schema},
	})
	if err == nil {
		t.Fatal("expected an error for a reference with an unrecognised kind")
	}
}

// resolvedTestRecord is the resolved form of the example schema: TestRecord
// with an enum, a fixed, and a union reusing the fixed.
func resolvedTestRecord() *avrocpb.Schema {
	const ns = "org.apache.avro.test"

	return &avrocpb.Schema{
		Namespace: proto.String(ns),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name:      proto.String("TestRecord"),
					Namespace: proto.String(ns),
					FullName:  proto.String(ns + ".TestRecord"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("name"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Reference{Reference: primRef("string")},
							},
						},
						{
							Name: proto.String("kind"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_EnumType{
									EnumType: &avrocpb.Enum{
										Name:      proto.String("Kind"),
										Namespace: proto.String(ns),
										FullName:  proto.String(ns + ".Kind"),
										Values: []*avrocpb.Ident{
											{Value: proto.String("FOO")},
											{Value: proto.String("BAR")},
											{Value: proto.String("BAZ")},
										},
									},
								},
							},
						},
						{
							Name: proto.String("hash"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Fixed{
									Fixed: &avrocpb.Fixed{
										Name:      proto.String("MD5"),
										Namespace: proto.String(ns),
										FullName:  proto.String(ns + ".MD5"),
										Size:      proto.Int32(16),
									},
								},
							},
						},
						{
							Name: proto.String("nullableHash"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Union{
									Union: &avrocpb.Union{
										Types: []*avrocpb.Type{
											{Type: &avrocpb.Type_Reference{Reference: primRef("null")}},
											{Type: &avrocpb.Type_Reference{Reference: namedRef(ns + ".MD5")}},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}
