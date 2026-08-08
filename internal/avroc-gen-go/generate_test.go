// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/z5labs/avroc/avrocpb"

	avro "github.com/z5labs/avro-go"
	"google.golang.org/protobuf/proto"
)

// validateGoSyntax parses the Go source code and returns an error if it's invalid.
func validateGoSyntax(t *testing.T, code string) {
	t.Helper()
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, "test.go", code, parser.AllErrors)
	if err != nil {
		t.Errorf("generated code is not valid Go syntax: %v\n\nCode:\n%s", err, code)
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

	resp, err := generateToDir(t, tmpDir, &avrocpb.GenerateRequest{
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("avro")}},
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

	code := string(content)

	// Validate that the generated code is syntactically valid Go
	validateGoSyntax(t, code)

	// Check that the generated code contains expected elements
	// Note: go/format may add alignment spaces, so we check for key patterns
	expectations := []string{
		"package avro",
		"type Person struct",
		"Name string",
		"Age", // Don't check exact spacing due to go/format alignment
		"int32",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
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

	resp, err := generateToDir(t, tmpDir, &avrocpb.GenerateRequest{
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("avro")}},
		Schemas: []*avrocpb.Schema{schema},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(resp.OutputFiles[0])
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	code := string(content)

	// Validate that the generated code is syntactically valid Go
	validateGoSyntax(t, code)

	// Note: go/format adds alignment spaces, so check for key substrings
	expectations := []string{
		"package avro",
		"type Status int",
		"StatusPENDING",
		"Status = 0",
		"StatusACTIVE",
		"Status = 1",
		"StatusCOMPLETED",
		"Status = 2",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
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

	resp, err := generateToDir(t, tmpDir, &avrocpb.GenerateRequest{
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("avro")}},
		Schemas: []*avrocpb.Schema{schema},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(resp.OutputFiles[0])
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	code := string(content)

	// Validate that the generated code is syntactically valid Go
	validateGoSyntax(t, code)

	expectations := []string{
		"package avro",
		"type MD5 [16]byte",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
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

	resp, err := generateToDir(t, tmpDir, &avrocpb.GenerateRequest{
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("avro")}},
		Schemas: []*avrocpb.Schema{schema},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(resp.OutputFiles[0])
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	code := string(content)

	// Validate that the generated code is syntactically valid Go
	validateGoSyntax(t, code)

	// Union type names now include record name prefix to avoid collisions
	expectations := []string{
		"package avro",
		"type Event struct",
		"Data EventDataUnion",
		"type EventDataUnion interface",
		"isEventDataUnion()",
		"type EventDataUnionNull struct{}",
		"type EventDataUnionString struct",
		"Value string",
		"type EventDataUnionInt struct",
		"Value int32",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
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

	resp, err := generateToDir(t, tmpDir, &avrocpb.GenerateRequest{
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("avro")}},
		Schemas: []*avrocpb.Schema{schema},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(resp.OutputFiles[0])
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	code := string(content)

	// Validate that the generated code is syntactically valid Go
	validateGoSyntax(t, code)

	expectations := []string{
		"package avro",
		"type Numbers struct",
		"Values []int32",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
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

	resp, err := generateToDir(t, tmpDir, &avrocpb.GenerateRequest{
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("avro")}},
		Schemas: []*avrocpb.Schema{schema},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(resp.OutputFiles[0])
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	code := string(content)

	// Validate that the generated code is syntactically valid Go
	validateGoSyntax(t, code)

	expectations := []string{
		"package avro",
		"type Config struct",
		"Settings map[string]string",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

func TestGenerate_MultipleTypes(t *testing.T) {
	tmpDir := t.TempDir()

	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name:      proto.String("Order"),
					Namespace: proto.String("com.example"),
					FullName:  proto.String("com.example.Order"),
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
		Types: []*avrocpb.Type{
			{
				Type: &avrocpb.Type_EnumType{
					EnumType: &avrocpb.Enum{
						Name:      proto.String("OrderStatus"),
						Namespace: proto.String("com.example"),
						FullName:  proto.String("com.example.OrderStatus"),
						Values: []*avrocpb.Ident{
							{Value: proto.String("NEW")},
							{Value: proto.String("SHIPPED")},
						},
					},
				},
			},
		},
	}

	resp, err := generateToDir(t, tmpDir, &avrocpb.GenerateRequest{
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("avro")}},
		Schemas: []*avrocpb.Schema{schema},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(resp.OutputFiles[0])
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	code := string(content)

	// Validate that the generated code is syntactically valid Go
	validateGoSyntax(t, code)

	expectations := []string{
		"type Order struct",
		"type OrderStatus int",
		"OrderStatusNEW",
		"OrderStatusSHIPPED",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

func TestBuildSchemaFile_Filename(t *testing.T) {
	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name:      proto.String("MyRecord"),
					Namespace: proto.String("com.example"),
					FullName:  proto.String("com.example.MyRecord"),
				},
			},
		},
	}

	filename, _, err := buildSchemaFile("mypackage", schema, false)
	if err != nil {
		t.Fatalf("buildSchemaFile failed: %v", err)
	}
	if filename != "my_record.go" {
		t.Errorf("expected my_record.go, got %q", filename)
	}
}

func TestGenerate_PackageNameOption(t *testing.T) {
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

	resp, err := generateToDir(t, tmpDir, &avrocpb.GenerateRequest{
		Options: []*avrocpb.Option{
			{
				Name:  proto.String("package_name"),
				Value: proto.String("mypackage"),
			},
		},
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

	code := string(content)

	validateGoSyntax(t, code)

	if !strings.Contains(code, "package mypackage") {
		t.Errorf("expected generated code to contain %q, got:\n%s", "package mypackage", code)
	}
	if strings.Contains(code, "package avro") {
		t.Errorf("expected generated code to NOT contain %q, got:\n%s", "package avro", code)
	}
}

func TestGenerate_MissingPackageName(t *testing.T) {
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

	_, err := generateToDir(t, tmpDir, &avrocpb.GenerateRequest{
		Schemas: []*avrocpb.Schema{schema},
	})
	if err == nil {
		t.Fatal("expected error when package_name option is not set")
	}
}

func TestGenerate_SingleObjectEncoding(t *testing.T) {
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
		Options: []*avrocpb.Option{
			{Name: proto.String("package_name"), Value: proto.String("avro")},
			{Name: proto.String("encoding"), Value: proto.String("single_object")},
		},
		Schemas: []*avrocpb.Schema{schema},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(resp.OutputFiles[0])
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	code := string(content)
	validateGoSyntax(t, code)

	expectations := []string{
		"func (x *Person) Fingerprint() [8]byte {",
		"return [8]byte{",
	}
	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

func TestGenerate_SingleObjectEncoding_FingerprintComputation(t *testing.T) {
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

	// Compute the fingerprint via our code.
	fp, err := schemaFingerprint(schema)
	if err != nil {
		t.Fatalf("schemaFingerprint failed: %v", err)
	}

	// Independently compute the expected fingerprint.
	pcf := `{"name":"com.example.Person","type":"record","fields":[{"name":"name","type":"string"}]}`

	var expected [8]byte
	fpVal := avro.Fingerprint64([]byte(pcf))
	expected[0] = byte(fpVal)
	expected[1] = byte(fpVal >> 8)
	expected[2] = byte(fpVal >> 16)
	expected[3] = byte(fpVal >> 24)
	expected[4] = byte(fpVal >> 32)
	expected[5] = byte(fpVal >> 40)
	expected[6] = byte(fpVal >> 48)
	expected[7] = byte(fpVal >> 56)

	if fp != expected {
		t.Errorf("fingerprint round-trip mismatch\ngot:  %v\nwant: %v", fp, expected)
	}
}

func TestGenerate_WithoutEncodingOption_NoFingerprint(t *testing.T) {
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

	resp, err := generateToDir(t, tmpDir, &avrocpb.GenerateRequest{
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("avro")}},
		Schemas: []*avrocpb.Schema{schema},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(resp.OutputFiles[0])
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	code := string(content)
	if strings.Contains(code, "Fingerprint") {
		t.Errorf("expected no Fingerprint method without encoding option, got:\n%s", code)
	}
}

func TestGenerate_InvalidEncodingOption(t *testing.T) {
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

	_, err := generateToDir(t, tmpDir, &avrocpb.GenerateRequest{
		Options: []*avrocpb.Option{
			{Name: proto.String("package_name"), Value: proto.String("avro")},
			{Name: proto.String("encoding"), Value: proto.String("invalid_value")},
		},
		Schemas: []*avrocpb.Schema{schema},
	})
	if err == nil {
		t.Fatal("expected error for invalid encoding option value")
	}
	if !strings.Contains(err.Error(), "unsupported encoding option") {
		t.Errorf("expected error about unsupported encoding, got: %v", err)
	}
}

// TestGenerate_RejectsUnresolvedDescriptor proves the generator refuses a
// descriptor it cannot represent before emitting any of it. The code builders
// cannot report a problem, so without this check a reference claiming to name a
// primitive and naming none would be generated as a call to a Go type that does
// not exist.
func TestGenerate_RejectsUnresolvedDescriptor(t *testing.T) {
	tests := []struct {
		name string
		ref  *avrocpb.Reference
	}{
		{
			name: "unrecognised kind",
			ref:  &avrocpb.Reference{Name: proto.String("string")},
		},
		{
			name: "primitive naming no Avro primitive",
			ref: &avrocpb.Reference{
				Name: proto.String("strng"),
				Kind: avrocpb.TypeRefKind_TYPE_REF_KIND_PRIMITIVE.Enum(),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
										Type: &avrocpb.Type_Reference{Reference: tt.ref},
									},
								},
							},
						},
					},
				},
			}

			_, err := generateToDir(t, t.TempDir(), &avrocpb.GenerateRequest{
				Options: []*avrocpb.Option{
					{Name: proto.String("package_name"), Value: proto.String("avro")},
				},
				Schemas: []*avrocpb.Schema{schema},
			})
			if err == nil {
				t.Fatal("expected generation to fail on an unresolved descriptor")
			}
		})
	}
}
