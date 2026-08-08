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

func TestUnmarshal_Record(t *testing.T) {
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

	validateGoSyntax(t, code)

	expectations := []string{
		"func (x *Person) UnmarshalAvroBinary(r *avro.BinaryReader) error",
		"r.ReadString()",
		"r.ReadInt()",
		"var err error",
		"return nil",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

func TestUnmarshal_Fixed(t *testing.T) {
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

	validateGoSyntax(t, code)

	expectations := []string{
		"func (x *MD5) UnmarshalAvroBinary(r *avro.BinaryReader) error",
		"r.ReadFixed(16)",
		"copy(x[:], b)",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

func TestUnmarshal_Enum(t *testing.T) {
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

	validateGoSyntax(t, code)

	expectations := []string{
		"func (x *Status) UnmarshalAvroBinary(r *avro.BinaryReader) error",
		"r.ReadInt()",
		"*x = Status(v)",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

func TestUnmarshal_Union(t *testing.T) {
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

	validateGoSyntax(t, code)

	expectations := []string{
		// Standalone unmarshal function
		"func unmarshalEventDataUnion(r *avro.BinaryReader) (EventDataUnion, error)",
		"r.ReadLong()",
		"switch index",
		// Null member returns directly
		"return EventDataUnionNull{}, nil",
		// String member reads value
		"r.ReadString()",
		"return EventDataUnionString{Value: v}, nil",
		// Int member reads value
		"r.ReadInt()",
		"return EventDataUnionInt{Value: v}, nil",
		// Record unmarshal calls standalone function
		"x.Data, err = unmarshalEventDataUnion(r)",
		// fmt import for error handling
		`"fmt"`,
		"unknown union index",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

func TestUnmarshal_ArrayField(t *testing.T) {
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

	validateGoSyntax(t, code)

	expectations := []string{
		"func (x *Numbers) UnmarshalAvroBinary(r *avro.BinaryReader) error",
		"x.Values = nil",
		"r.ReadLong()",
		"blkCount == 0",
		"blkCount < 0",
		"for range blkCount",
		"r.ReadInt()",
		"x.Values = append(x.Values, v)",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

func TestUnmarshal_MapField(t *testing.T) {
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

	validateGoSyntax(t, code)

	expectations := []string{
		"func (x *Config) UnmarshalAvroBinary(r *avro.BinaryReader) error",
		"x.Settings = make(map[string]string)",
		"r.ReadLong()",
		"blkCount == 0",
		"blkCount < 0",
		"for range blkCount",
		"r.ReadString()",
		"x.Settings[k] = v",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

func TestUnmarshal_NestedRecord(t *testing.T) {
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

	validateGoSyntax(t, code)

	expectations := []string{
		"func (x *Order) UnmarshalAvroBinary(r *avro.BinaryReader) error",
		"r.ReadString()",
		"x.Customer.UnmarshalAvroBinary(r)",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

func TestUnmarshal_AllPrimitiveTypes(t *testing.T) {
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

	validateGoSyntax(t, code)

	expectations := []string{
		"r.ReadBool()",
		"r.ReadInt()",
		"r.ReadLong()",
		"r.ReadFloat()",
		"r.ReadDouble()",
		"r.ReadBytes()",
		"r.ReadString()",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

func TestUnmarshal_EmptyRecord(t *testing.T) {
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

	validateGoSyntax(t, code)

	expectations := []string{
		"func (x *Empty) UnmarshalAvroBinary(r *avro.BinaryReader) error",
		"return nil",
	}

	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}
