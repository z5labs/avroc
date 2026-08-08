// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package ir

import (
	"strings"
	"testing"

	"github.com/z5labs/avroc/internal/avrocpb"

	"google.golang.org/protobuf/proto"
)

func primRef(name string) *avrocpb.Type {
	return &avrocpb.Type{
		Type: &avrocpb.Type_Reference{
			Reference: &avrocpb.Reference{
				Name: proto.String(name),
				Kind: avrocpb.TypeRefKind_TYPE_REF_KIND_PRIMITIVE.Enum(),
			},
		},
	}
}

func namedRef(fullName string) *avrocpb.Type {
	return &avrocpb.Type{
		Type: &avrocpb.Type_Reference{
			Reference: &avrocpb.Reference{
				Name: proto.String(fullName),
				Kind: avrocpb.TypeRefKind_TYPE_REF_KIND_NAMED.Enum(),
			},
		},
	}
}

func TestCanonicalJSON(t *testing.T) {
	const ns = "org.apache.avro.test"

	tests := []struct {
		name   string
		schema *avrocpb.Schema
		want   string
	}{
		{
			name: "primitives",
			schema: &avrocpb.Schema{
				Type: &avrocpb.Type{
					Type: &avrocpb.Type_Record{
						Record: &avrocpb.Record{
							Name:     proto.String("Person"),
							FullName: proto.String("com.example.Person"),
							Fields: []*avrocpb.Field{
								{Name: proto.String("name"), Type: primRef("string")},
								{Name: proto.String("age"), Type: primRef("int")},
							},
						},
					},
				},
			},
			want: `{"name":"com.example.Person","type":"record","fields":[{"name":"name","type":"string"},{"name":"age","type":"int"}]}`,
		},
		{
			name: "definition at first use, name at the second",
			schema: &avrocpb.Schema{
				Type: &avrocpb.Type{
					Type: &avrocpb.Type_Record{
						Record: &avrocpb.Record{
							Name:     proto.String("TestRecord"),
							FullName: proto.String(ns + ".TestRecord"),
							Fields: []*avrocpb.Field{
								{
									Name: proto.String("hash"),
									Type: &avrocpb.Type{
										Type: &avrocpb.Type_Fixed{
											Fixed: &avrocpb.Fixed{
												Name:     proto.String("MD5"),
												FullName: proto.String(ns + ".MD5"),
												Size:     proto.Int32(16),
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
													primRef("null"),
													namedRef(ns + ".MD5"),
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			want: `{"name":"org.apache.avro.test.TestRecord","type":"record","fields":[{"name":"hash","type":{"name":"org.apache.avro.test.MD5","type":"fixed","size":16}},{"name":"nullableHash","type":["null","org.apache.avro.test.MD5"]}]}`,
		},
		{
			name: "array and map",
			schema: &avrocpb.Schema{
				Type: &avrocpb.Type{
					Type: &avrocpb.Type_Record{
						Record: &avrocpb.Record{
							Name:     proto.String("Bag"),
							FullName: proto.String("com.example.Bag"),
							Fields: []*avrocpb.Field{
								{
									Name: proto.String("tags"),
									Type: &avrocpb.Type{
										Type: &avrocpb.Type_Array{
											Array: &avrocpb.Array{Items: primRef("string")},
										},
									},
								},
								{
									Name: proto.String("meta"),
									Type: &avrocpb.Type{
										Type: &avrocpb.Type_MapType{
											MapType: &avrocpb.Map{Values: primRef("long")},
										},
									},
								},
							},
						},
					},
				},
			},
			want: `{"name":"com.example.Bag","type":"record","fields":[{"name":"tags","type":{"type":"array","items":"string"}},{"name":"meta","type":{"type":"map","values":"long"}}]}`,
		},
		{
			name: "enum",
			schema: &avrocpb.Schema{
				Type: &avrocpb.Type{
					Type: &avrocpb.Type_EnumType{
						EnumType: &avrocpb.Enum{
							Name:     proto.String("Kind"),
							FullName: proto.String(ns + ".Kind"),
							Values: []*avrocpb.Ident{
								{Value: proto.String("FOO")},
								{Value: proto.String("BAR")},
							},
						},
					},
				},
			},
			want: `{"name":"org.apache.avro.test.Kind","type":"enum","symbols":["FOO","BAR"]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalJSON(tt.schema)
			if err != nil {
				t.Fatalf("CanonicalJSON failed: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("canonical form mismatch\ngot:  %s\nwant: %s", got, tt.want)
			}
		})
	}
}

// TestCanonicalJSON_RejectsUnresolved covers the two ways a descriptor can fail
// to be resolved. Neither is skipped: a canonical form derived from a schema
// the walk did not fully understand is the failure the fingerprint exists to
// prevent.
func TestCanonicalJSON_RejectsUnresolved(t *testing.T) {
	tests := []struct {
		name   string
		schema *avrocpb.Schema
		want   string
	}{
		{
			name: "named type without a full name",
			schema: &avrocpb.Schema{
				Type: &avrocpb.Type{
					Type: &avrocpb.Type_Record{
						Record: &avrocpb.Record{Name: proto.String("Person")},
					},
				},
			},
			want: "no fully-qualified name",
		},
		{
			name: "reference without a kind",
			schema: &avrocpb.Schema{
				Type: &avrocpb.Type{
					Type: &avrocpb.Type_Record{
						Record: &avrocpb.Record{
							Name:     proto.String("Person"),
							FullName: proto.String("com.example.Person"),
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
			},
			want: "unrecognised kind",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CanonicalJSON(tt.schema)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}
