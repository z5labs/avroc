// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/z5labs/avroc/internal/avrocpb"
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
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("avro")}},
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
					Name: proto.String("Status"),
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
	resp, err := svc.Generate(context.Background(), &avrocpb.GenerateRequest{
		OutputDirectory: proto.String(tmpDir),
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("avro")}},
		Schemas:         []*avrocpb.Schema{schema},
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
					Name: proto.String("MD5"),
					Size: &size,
				},
			},
		},
	}

	svc := &generatorService{}
	resp, err := svc.Generate(context.Background(), &avrocpb.GenerateRequest{
		OutputDirectory: proto.String(tmpDir),
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("avro")}},
		Schemas:         []*avrocpb.Schema{schema},
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
					Name: proto.String("Event"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("data"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Union{
									Union: &avrocpb.Union{
										Types: []*avrocpb.Type{
											{Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("null")}}},
											{Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("string")}}},
											{Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("int")}}},
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
	resp, err := svc.Generate(context.Background(), &avrocpb.GenerateRequest{
		OutputDirectory: proto.String(tmpDir),
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("avro")}},
		Schemas:         []*avrocpb.Schema{schema},
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
					Name: proto.String("Numbers"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("values"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Array{
									Array: &avrocpb.Array{
										Items: &avrocpb.Type{
											Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("int")}},
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
	resp, err := svc.Generate(context.Background(), &avrocpb.GenerateRequest{
		OutputDirectory: proto.String(tmpDir),
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("avro")}},
		Schemas:         []*avrocpb.Schema{schema},
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
					Name: proto.String("Config"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("settings"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_MapType{
									MapType: &avrocpb.Map{
										Values: &avrocpb.Ident{Value: proto.String("string")},
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
	resp, err := svc.Generate(context.Background(), &avrocpb.GenerateRequest{
		OutputDirectory: proto.String(tmpDir),
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("avro")}},
		Schemas:         []*avrocpb.Schema{schema},
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
					Name: proto.String("Order"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("id"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("string")}},
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
						Name: proto.String("OrderStatus"),
						Values: []*avrocpb.Ident{
							{Value: proto.String("NEW")},
							{Value: proto.String("SHIPPED")},
						},
					},
				},
			},
		},
	}

	svc := &generatorService{}
	resp, err := svc.Generate(context.Background(), &avrocpb.GenerateRequest{
		OutputDirectory: proto.String(tmpDir),
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("avro")}},
		Schemas:         []*avrocpb.Schema{schema},
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

func TestGenerate_EmptyOutputDirectory(t *testing.T) {
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
		name   string
		schema *avrocpb.Schema
		want   string
	}{
		{
			name: "record type",
			schema: &avrocpb.Schema{
				Type: &avrocpb.Type{
					Type: &avrocpb.Type_Record{
						Record: &avrocpb.Record{Name: proto.String("MyRecord")},
					},
				},
			},
			want: "my_record.go",
		},
		{
			name: "enum type",
			schema: &avrocpb.Schema{
				Type: &avrocpb.Type{
					Type: &avrocpb.Type_EnumType{
						EnumType: &avrocpb.Enum{Name: proto.String("MyEnum")},
					},
				},
			},
			want: "my_enum.go",
		},
		{
			name: "namespace fallback",
			schema: &avrocpb.Schema{
				Namespace: proto.String("com.example.events"),
			},
			want: "events.go",
		},
		{
			name:   "empty schema",
			schema: &avrocpb.Schema{},
			want:   "schema.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := schemaFilename(tt.schema)
			if got != tt.want {
				t.Errorf("schemaFilename() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGenerate_CreatesOutputDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "nested", "output")

	schema := &avrocpb.Schema{
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name: proto.String("Test"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("value"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("string")}},
							},
						},
					},
				},
			},
		},
	}

	svc := &generatorService{}
	resp, err := svc.Generate(context.Background(), &avrocpb.GenerateRequest{
		OutputDirectory: proto.String(outputDir),
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("avro")}},
		Schemas:         []*avrocpb.Schema{schema},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if len(resp.OutputFiles) != 1 {
		t.Fatalf("expected 1 output file, got %d", len(resp.OutputFiles))
	}

	// Verify the file exists
	if _, err := os.Stat(resp.OutputFiles[0]); os.IsNotExist(err) {
		t.Errorf("output file does not exist: %s", resp.OutputFiles[0])
	}
}

func TestGenerate_PackageNameOption(t *testing.T) {
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
					},
				},
			},
		},
	}

	svc := &generatorService{}
	resp, err := svc.Generate(context.Background(), &avrocpb.GenerateRequest{
		OutputDirectory: proto.String(tmpDir),
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
					Name: proto.String("Person"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("name"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("string")}},
							},
						},
					},
				},
			},
		},
	}

	svc := &generatorService{}
	_, err := svc.Generate(context.Background(), &avrocpb.GenerateRequest{
		OutputDirectory: proto.String(tmpDir),
		Schemas:         []*avrocpb.Schema{schema},
	})
	if err == nil {
		t.Fatal("expected error when package_name option is not set")
	}
}
