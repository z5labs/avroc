// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgenpcf

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/z5labs/avroc/internal/avrocpb"
	"google.golang.org/protobuf/proto"
)

func TestGenerate_Record(t *testing.T) {
	tmpDir := t.TempDir()

	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name: proto.String("Person"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("name"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("string")}},
							},
						},
						{
							Name: proto.String("age"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("int")}},
							},
						},
					},
				},
			},
		},
	}

	svc := &generatorService{}
	resp, err := svc.Generate(context.Background(), &avrocpb.GenerateRequest{
		OutputDirectory: proto.String(tmpDir),
		Schemas:         []*avrocpb.Schema{schema},
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

	// Validate it's valid JSON
	var v any
	if err := json.Unmarshal(content, &v); err != nil {
		t.Fatalf("output is not valid JSON: %v\nContent: %s", err, string(content))
	}

	// Verify canonical form structure: {"name":...,"type":"record","fields":[...]}
	obj, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected JSON object, got %T", v)
	}
	if obj["type"] != "record" {
		t.Errorf("expected type=record, got %v", obj["type"])
	}
	if obj["name"] != "com.example.Person" {
		t.Errorf("expected name=com.example.Person, got %v", obj["name"])
	}
}

func TestGenerate_RecordWithNamedTypes(t *testing.T) {
	tmpDir := t.TempDir()

	// TestRecord with Kind (enum), MD5 (fixed), union{null, MD5}
	schema := &avrocpb.Schema{
		Namespace: proto.String("org.apache.avro.test"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name: proto.String("TestRecord"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("name"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("string")}},
							},
						},
						{
							Name: proto.String("kind"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("Kind")}},
							},
						},
						{
							Name: proto.String("hash"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("MD5")}},
							},
						},
						{
							Name: proto.String("nullableHash"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Union{
									Union: &avrocpb.Union{
										Types: []*avrocpb.Type{
											{Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("null")}}},
											{Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("MD5")}}},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		Types: []*avrocpb.Type{
			{
				Type: &avrocpb.Type_EnumType{
					EnumType: &avrocpb.Enum{
						Name:      proto.String("Kind"),
						Namespace: proto.String("org.apache.avro.test"),
						Values: []*avrocpb.Ident{
							{Value: proto.String("FOO")},
							{Value: proto.String("BAR")},
							{Value: proto.String("BAZ")},
						},
					},
				},
			},
			{
				Type: &avrocpb.Type_Fixed{
					Fixed: &avrocpb.Fixed{
						Name:      proto.String("MD5"),
						Namespace: proto.String("org.apache.avro.test"),
						Size:      proto.Int32(16),
					},
				},
			},
		},
	}

	svc := &generatorService{}
	resp, err := svc.Generate(context.Background(), &avrocpb.GenerateRequest{
		OutputDirectory: proto.String(tmpDir),
		Schemas:         []*avrocpb.Schema{schema},
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

	// Validate it's valid JSON
	var v any
	if err := json.Unmarshal(content, &v); err != nil {
		t.Fatalf("output is not valid JSON: %v\nContent: %s", err, string(content))
	}

	obj, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected JSON object, got %T", v)
	}

	// Verify canonical form: name should be FQ, type should be record
	if obj["name"] != "org.apache.avro.test.TestRecord" {
		t.Errorf("expected FQ name, got %v", obj["name"])
	}
	if obj["type"] != "record" {
		t.Errorf("expected type=record, got %v", obj["type"])
	}

	// Kind should be inlined (first use), MD5 should be inlined (first use),
	// nullableHash should reference MD5 by FQ name (second use)
	fields, ok := obj["fields"].([]any)
	if !ok {
		t.Fatalf("expected fields array")
	}
	if len(fields) != 4 {
		t.Fatalf("expected 4 fields, got %d", len(fields))
	}

	kindField, ok := fields[1].(map[string]any)
	if !ok {
		t.Fatalf("expected kind field to be a JSON object, got %T", fields[1])
	}
	kindType, ok := kindField["type"].(map[string]any)
	if !ok {
		t.Errorf("expected kind field type to be inlined object, got %T", kindField["type"])
	} else if kindType["type"] != "enum" {
		t.Errorf("expected kind type=enum, got %v", kindType["type"])
	}

	hashField, ok := fields[2].(map[string]any)
	if !ok {
		t.Fatalf("expected hash field to be a JSON object, got %T", fields[2])
	}
	hashType, ok := hashField["type"].(map[string]any)
	if !ok {
		t.Errorf("expected hash field type to be inlined object, got %T", hashField["type"])
	} else if hashType["type"] != "fixed" {
		t.Errorf("expected hash type=fixed, got %v", hashType["type"])
	}

	nullableHashField, ok := fields[3].(map[string]any)
	if !ok {
		t.Fatalf("expected nullableHash field to be a JSON object, got %T", fields[3])
	}
	nullableHashTypes, ok := nullableHashField["type"].([]any)
	if !ok {
		t.Fatalf("expected nullableHash field type to be union array, got %T", nullableHashField["type"])
	}
	if nullableHashTypes[0] != "null" {
		t.Errorf("expected first union type=null, got %v", nullableHashTypes[0])
	}
	// MD5 was already defined, so it should be a reference string
	if nullableHashTypes[1] != "org.apache.avro.test.MD5" {
		t.Errorf("expected second union type=org.apache.avro.test.MD5, got %v", nullableHashTypes[1])
	}
}

func TestGenerate_EmptyOutputDir(t *testing.T) {
	svc := &generatorService{}
	_, err := svc.Generate(context.Background(), &avrocpb.GenerateRequest{
		OutputDirectory: proto.String(""),
		Schemas:         []*avrocpb.Schema{},
	})
	if err == nil {
		t.Fatal("expected error for empty output directory")
	}
}

func TestSchemaFilename(t *testing.T) {
	tests := []struct {
		name     string
		schema   *avrocpb.Schema
		expected string
	}{
		{
			name: "record type",
			schema: &avrocpb.Schema{
				Type: &avrocpb.Type{
					Type: &avrocpb.Type_Record{
						Record: &avrocpb.Record{Name: proto.String("TestRecord")},
					},
				},
			},
			expected: "test_record.avsc",
		},
		{
			name: "enum type",
			schema: &avrocpb.Schema{
				Type: &avrocpb.Type{
					Type: &avrocpb.Type_EnumType{
						EnumType: &avrocpb.Enum{Name: proto.String("Status")},
					},
				},
			},
			expected: "status.avsc",
		},
		{
			name:     "fallback",
			schema:   &avrocpb.Schema{Namespace: proto.String("com.example")},
			expected: "example.avsc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := schemaFilename(tt.schema)
			if got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}
