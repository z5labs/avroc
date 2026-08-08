// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgenpcf

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/z5labs/avroc/avrocpb"
	"google.golang.org/protobuf/proto"
)

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

	resp, err := generateToDir(t, tmpDir, &avrocpb.GenerateRequest{
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

// TestGenerate_RecordWithNamedTypes feeds the generator a resolved schema — the
// enum and the fixed written out in full at their first use, and a
// fully-qualified reference at the second — and asserts the canonical form
// follows that ordering exactly. The generator decides none of it: no symbol
// table, no first-use bookkeeping.
func TestGenerate_RecordWithNamedTypes(t *testing.T) {
	tmpDir := t.TempDir()

	resp, err := generateToDir(t, tmpDir, &avrocpb.GenerateRequest{
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

	// Kind is written out in full at its first use, MD5 likewise, and
	// nullableHash refers to MD5 by fully-qualified name at its second.
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

// TestGenerate_UnresolvedReference proves the generator refuses a descriptor
// whose reference classification it does not recognise, rather than guessing.
func TestGenerate_UnresolvedReference(t *testing.T) {
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

	_, err := generateToDir(t, t.TempDir(), &avrocpb.GenerateRequest{
		Schemas: []*avrocpb.Schema{schema},
	})
	if err == nil {
		t.Fatal("expected an error for a reference with an unrecognised kind")
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

// TestBuildSchemaFile_ArrayRootFilename asserts this generator names the file
// after what an array root is about rather than after the namespace, which is
// where ir.SchemaBaseName used to land for a root that is not itself a named
// type.
func TestBuildSchemaFile_ArrayRootFilename(t *testing.T) {
	filename, _, err := buildSchemaFile(arrayRootSchema())
	if err != nil {
		t.Fatalf("buildSchemaFile failed: %v", err)
	}
	if filename != "event.avsc" {
		t.Errorf("expected event.avsc, got %q", filename)
	}
}

// arrayRootSchema is the resolved form of `schema array<Event>;`: the record is
// written out in full at its first use, inside the array's items, so the
// schema's own types list is empty and its namespace is all that is left to
// name a file after.
func arrayRootSchema() *avrocpb.Schema {
	const ns = "org.example"

	return &avrocpb.Schema{
		Namespace: proto.String(ns),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Array{
				Array: &avrocpb.Array{
					Items: &avrocpb.Type{
						Type: &avrocpb.Type_Record{
							Record: &avrocpb.Record{
								Name:      proto.String("Event"),
								Namespace: proto.String(ns),
								FullName:  proto.String(ns + ".Event"),
								Fields: []*avrocpb.Field{
									{
										Name: proto.String("id"),
										Type: &avrocpb.Type{
											Type: &avrocpb.Type_Reference{Reference: primRef("string")},
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
