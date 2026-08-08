// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"os"
	"strings"
	"testing"

	"github.com/z5labs/avroc/internal/avrocpb"
	"google.golang.org/protobuf/proto"
)

func TestMarshal_Record(t *testing.T) {
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
		"func (x *Person) MarshalAvroBinary(w *avro.BinaryWriter) error",
		"w.WriteString(x.Name)",
		"w.WriteInt(x.Age)",
		"var err error",
		"return nil",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

func TestMarshal_Fixed(t *testing.T) {
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
	resp, err := generateToDir(t, svc, tmpDir, &avrocpb.GenerateRequest{
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
		"func (x MD5) MarshalAvroBinary(w *avro.BinaryWriter) error",
		"return w.WriteFixed(x[:])",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

func TestMarshal_Enum(t *testing.T) {
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
					},
				},
			},
		},
	}

	svc := &generatorService{}
	resp, err := generateToDir(t, svc, tmpDir, &avrocpb.GenerateRequest{
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
		"func (x Status) MarshalAvroBinary(w *avro.BinaryWriter) error",
		"return w.WriteInt(int32(x))",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

func TestMarshal_Union(t *testing.T) {
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
		// Union interface includes MarshalAvroBinary
		"MarshalAvroBinary(w *avro.BinaryWriter) error",
		// Null member writes index 0
		"func (x EventDataUnionNull) MarshalAvroBinary(w *avro.BinaryWriter) error",
		"return w.WriteLong(0)",
		// String member writes index 1 then value
		"func (x EventDataUnionString) MarshalAvroBinary(w *avro.BinaryWriter) error",
		"w.WriteLong(1)",
		"w.WriteString(x.Value)",
		// Int member writes index 2 then value
		"func (x EventDataUnionInt) MarshalAvroBinary(w *avro.BinaryWriter) error",
		"w.WriteLong(2)",
		"w.WriteInt(x.Value)",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

func TestMarshal_ArrayField(t *testing.T) {
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
		"func (x *Numbers) MarshalAvroBinary(w *avro.BinaryWriter) error",
		"if len(x.Values) > 0",
		"w.WriteLong(int64(len(x.Values)))",
		"for i := range x.Values",
		"w.WriteInt(x.Values[i])",
		// Block terminator
		"w.WriteLong(0)",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

func TestMarshal_MapField(t *testing.T) {
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
		"func (x *Config) MarshalAvroBinary(w *avro.BinaryWriter) error",
		"if len(x.Settings) > 0",
		"w.WriteLong(int64(len(x.Settings)))",
		"for k := range x.Settings",
		"w.WriteString(k)",
		"w.WriteString(x.Settings[k])",
		// Block terminator
		"w.WriteLong(0)",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

func TestMarshal_NestedRecord(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a schema with two records, one referencing the other
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
								Type: &avrocpb.Type_Reference{Reference: primRef("string")},
							},
						},
						{
							Name: proto.String("customer"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Reference{Reference: namedRef("Customer")},
							},
						},
					},
				},
			},
		},
		Types: []*avrocpb.Type{
			{
				Type: &avrocpb.Type_Record{
					Record: &avrocpb.Record{
						Name: proto.String("Customer"),
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
		},
	}

	svc := &generatorService{}
	resp, err := generateToDir(t, svc, tmpDir, &avrocpb.GenerateRequest{
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
		"func (x *Order) MarshalAvroBinary(w *avro.BinaryWriter) error",
		"w.WriteString(x.Id)",
		// Nested record delegates to MarshalAvroBinary
		"x.Customer.MarshalAvroBinary(w)",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

func TestMarshal_AllPrimitiveTypes(t *testing.T) {
	tmpDir := t.TempDir()

	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name: proto.String("AllTypes"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("bool_field"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Reference{Reference: primRef("boolean")},
							},
						},
						{
							Name: proto.String("int_field"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Reference{Reference: primRef("int")},
							},
						},
						{
							Name: proto.String("long_field"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Reference{Reference: primRef("long")},
							},
						},
						{
							Name: proto.String("float_field"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Reference{Reference: primRef("float")},
							},
						},
						{
							Name: proto.String("double_field"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Reference{Reference: primRef("double")},
							},
						},
						{
							Name: proto.String("bytes_field"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Reference{Reference: primRef("bytes")},
							},
						},
						{
							Name: proto.String("string_field"),
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
		"w.WriteBool(x.BoolField)",
		"w.WriteInt(x.IntField)",
		"w.WriteLong(x.LongField)",
		"w.WriteFloat(x.FloatField)",
		"w.WriteDouble(x.DoubleField)",
		"w.WriteBytes(x.BytesField)",
		"w.WriteString(x.StringField)",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

func TestMarshal_EmptyRecord(t *testing.T) {
	tmpDir := t.TempDir()

	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name:   proto.String("Empty"),
					Fields: []*avrocpb.Field{},
				},
			},
		},
	}

	svc := &generatorService{}
	resp, err := generateToDir(t, svc, tmpDir, &avrocpb.GenerateRequest{
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
		"func (x *Empty) MarshalAvroBinary(w *avro.BinaryWriter) error",
		"return nil",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}
