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

// TestValidate_RejectsMisdeclaredPrimitive covers the case a kind alone cannot
// catch: a reference that says PRIMITIVE and names something Avro has no
// primitive for. Emitting it would produce a canonical form, and so a
// fingerprint, that is quietly wrong.
func TestValidate_RejectsMisdeclaredPrimitive(t *testing.T) {
	schema := &avrocpb.Schema{
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
									Reference: &avrocpb.Reference{
										Name: proto.String("strng"),
										Kind: avrocpb.TypeRefKind_TYPE_REF_KIND_PRIMITIVE.Enum(),
									},
								},
							},
						},
					},
				},
			},
		},
	}

	err := Validate(schema)
	if err == nil {
		t.Fatal("expected an error for a primitive reference naming no Avro primitive")
	}
	if !strings.Contains(err.Error(), "names no Avro primitive") {
		t.Errorf("error = %v, want it to name the problem", err)
	}
}

// TestValidate_RejectsNilType is the other half: an absent type is not an empty
// schema, and a generator emitting one writes a file no Avro reader accepts.
func TestValidate_RejectsNilType(t *testing.T) {
	if err := Validate(&avrocpb.Schema{}); err == nil {
		t.Fatal("expected an error for a schema with no type")
	}
}

// TestValidate_AcceptsNamedTypeSpelledLikeAPrimitive is the rule the primitive
// list must not overreach into: the kind decides what a reference is, so a
// named type may legitimately be called "string".
func TestValidate_AcceptsNamedTypeSpelledLikeAPrimitive(t *testing.T) {
	schema := &avrocpb.Schema{
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name:     proto.String("Person"),
					FullName: proto.String("com.example.Person"),
					Fields: []*avrocpb.Field{
						{Name: proto.String("name"), Type: namedRef("com.example.string")},
					},
				},
			},
		},
	}

	if err := Validate(schema); err != nil {
		t.Errorf("Validate rejected a named type spelled like a primitive: %v", err)
	}
}
