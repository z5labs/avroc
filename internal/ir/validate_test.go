// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package ir

import (
	"strings"
	"testing"

	"github.com/z5labs/avroc/avrocpb"

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

// TestValidateRejectsUnknownSortOrder is the closed-set half of
// docs/ir/SPEC.md's asymmetry, applied to the set that is easiest to fall
// through on: sort order has a meaningful zero value, so a consumer that
// switched on the three it knows and let anything else reach the default would
// order a record by ascending and say nothing about it. The user finds out when
// the generated code sorts differently from somebody else's.
func TestValidateRejectsUnknownSortOrder(t *testing.T) {
	schema := &avrocpb.Schema{
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name:     proto.String("Person"),
					FullName: proto.String("com.example.Person"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("name"),
							Type: primRef("string"),
							// A member added to SortOrder by an IR newer than
							// this consumer.
							SortOrder: avrocpb.SortOrder(99).Enum(),
						},
					},
				},
			},
		},
	}

	err := Validate(schema)
	if err == nil {
		t.Fatal("expected an error for a sort order this consumer does not recognise")
	}
	if !strings.Contains(err.Error(), "unrecognised sort order") {
		t.Errorf("error = %v, want it to name the problem", err)
	}
	if !strings.Contains(err.Error(), `field "name"`) {
		t.Errorf("error = %v, want it to name the field carrying it", err)
	}
}

// TestValidateAcceptsEverySortOrder is the guard on the test above: rejecting an
// unknown member is only correct if every known one is accepted, and a set this
// small is worth pinning so a future member cannot be added to the proto without
// the switch above being taught about it.
func TestValidateAcceptsEverySortOrder(t *testing.T) {
	orders := []avrocpb.SortOrder{
		avrocpb.SortOrder_SORT_ORDER_ASC,
		avrocpb.SortOrder_SORT_ORDER_DESC,
		avrocpb.SortOrder_SORT_ORDER_IGNORE,
	}

	if got := len(avrocpb.SortOrder_name); got != len(orders) {
		t.Fatalf("SortOrder has %d members, this test knows %d — teach validateSortOrder about the new one", got, len(orders))
	}

	for _, order := range orders {
		t.Run(order.String(), func(t *testing.T) {
			schema := &avrocpb.Schema{
				Type: &avrocpb.Type{
					Type: &avrocpb.Type_Record{
						Record: &avrocpb.Record{
							Name:     proto.String("Person"),
							FullName: proto.String("com.example.Person"),
							Fields: []*avrocpb.Field{
								{Name: proto.String("name"), Type: primRef("string"), SortOrder: order.Enum()},
							},
						},
					},
				},
			}

			if err := Validate(schema); err != nil {
				t.Errorf("Validate rejected sort order %v: %v", order, err)
			}
		})
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
