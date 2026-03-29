// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"testing"

	"github.com/z5labs/avroc/internal/avrocpb"
	"google.golang.org/protobuf/proto"
)

func TestCanonicalForm_SimpleRecord(t *testing.T) {
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

	got := canonicalForm(schema)
	expected := `{"name":"com.example.Person","type":"record","fields":[{"name":"name","type":"string"},{"name":"age","type":"int"}]}`

	if got != expected {
		t.Errorf("canonicalForm mismatch\ngot:  %s\nwant: %s", got, expected)
	}
}

func TestCanonicalForm_RecordWithEnum(t *testing.T) {
	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name: proto.String("Event"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("kind"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("Kind")}},
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
						Name: proto.String("Kind"),
						Values: []*avrocpb.Ident{
							{Value: proto.String("FOO")},
							{Value: proto.String("BAR")},
						},
					},
				},
			},
		},
	}

	got := canonicalForm(schema)
	expected := `{"name":"com.example.Event","type":"record","fields":[{"name":"kind","type":{"name":"com.example.Kind","type":"enum","symbols":["FOO","BAR"]}}]}`

	if got != expected {
		t.Errorf("canonicalForm mismatch\ngot:  %s\nwant: %s", got, expected)
	}
}

func TestCanonicalForm_RecordWithFixed(t *testing.T) {
	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name: proto.String("Hash"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("md5"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("MD5")}},
							},
						},
					},
				},
			},
		},
		Types: []*avrocpb.Type{
			{
				Type: &avrocpb.Type_Fixed{
					Fixed: &avrocpb.Fixed{
						Name: proto.String("MD5"),
						Size: proto.Int32(16),
					},
				},
			},
		},
	}

	got := canonicalForm(schema)
	expected := `{"name":"com.example.Hash","type":"record","fields":[{"name":"md5","type":{"name":"com.example.MD5","type":"fixed","size":16}}]}`

	if got != expected {
		t.Errorf("canonicalForm mismatch\ngot:  %s\nwant: %s", got, expected)
	}
}

func TestCanonicalForm_RecordWithArray(t *testing.T) {
	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name: proto.String("List"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("items"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Array{
									Array: &avrocpb.Array{
										Items: &avrocpb.Type{
											Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("string")}},
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

	got := canonicalForm(schema)
	expected := `{"name":"com.example.List","type":"record","fields":[{"name":"items","type":{"type":"array","items":"string"}}]}`

	if got != expected {
		t.Errorf("canonicalForm mismatch\ngot:  %s\nwant: %s", got, expected)
	}
}

func TestCanonicalForm_RecordWithMap(t *testing.T) {
	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name: proto.String("Config"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("props"),
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

	got := canonicalForm(schema)
	expected := `{"name":"com.example.Config","type":"record","fields":[{"name":"props","type":{"type":"map","values":"string"}}]}`

	if got != expected {
		t.Errorf("canonicalForm mismatch\ngot:  %s\nwant: %s", got, expected)
	}
}

func TestCanonicalForm_RecordWithUnion(t *testing.T) {
	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name: proto.String("Nullable"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("value"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Union{
									Union: &avrocpb.Union{
										Types: []*avrocpb.Type{
											{Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("null")}}},
											{Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("string")}}},
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

	got := canonicalForm(schema)
	expected := `{"name":"com.example.Nullable","type":"record","fields":[{"name":"value","type":["null","string"]}]}`

	if got != expected {
		t.Errorf("canonicalForm mismatch\ngot:  %s\nwant: %s", got, expected)
	}
}

func TestCanonicalForm_RepeatedNamedTypeReference(t *testing.T) {
	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name: proto.String("TwoKinds"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("first"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("Kind")}},
							},
						},
						{
							Name: proto.String("second"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("Kind")}},
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
						Name: proto.String("Kind"),
						Values: []*avrocpb.Ident{
							{Value: proto.String("A")},
							{Value: proto.String("B")},
						},
					},
				},
			},
		},
	}

	got := canonicalForm(schema)
	// First reference inlines the full definition; second is just the FQ name.
	expected := `{"name":"com.example.TwoKinds","type":"record","fields":[{"name":"first","type":{"name":"com.example.Kind","type":"enum","symbols":["A","B"]}},{"name":"second","type":"com.example.Kind"}]}`

	if got != expected {
		t.Errorf("canonicalForm mismatch\ngot:  %s\nwant: %s", got, expected)
	}
}

func TestCanonicalForm_NoNamespace(t *testing.T) {
	schema := &avrocpb.Schema{
		Namespace: proto.String(""),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name: proto.String("Simple"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("x"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("int")}},
							},
						},
					},
				},
			},
		},
	}

	got := canonicalForm(schema)
	expected := `{"name":"Simple","type":"record","fields":[{"name":"x","type":"int"}]}`

	if got != expected {
		t.Errorf("canonicalForm mismatch\ngot:  %s\nwant: %s", got, expected)
	}
}

func TestCanonicalForm_StripsAliasesAndOrder(t *testing.T) {
	// Aliases and sort_order should not appear in canonical form.
	so := avrocpb.SortOrder_SORT_ORDER_DESC
	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name:    proto.String("Aliased"),
					Aliases: []string{"OldName"},
					Fields: []*avrocpb.Field{
						{
							Name:      proto.String("value"),
							Aliases:   []string{"old_value"},
							SortOrder: &so,
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Ident{Ident: &avrocpb.Ident{Value: proto.String("string")}},
							},
						},
					},
				},
			},
		},
	}

	got := canonicalForm(schema)
	expected := `{"name":"com.example.Aliased","type":"record","fields":[{"name":"value","type":"string"}]}`

	if got != expected {
		t.Errorf("canonicalForm mismatch\ngot:  %s\nwant: %s", got, expected)
	}
}

func TestCanonicalForm_NestedRecord(t *testing.T) {
	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name: proto.String("Outer"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("inner"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Record{
									Record: &avrocpb.Record{
										Name: proto.String("Inner"),
										Fields: []*avrocpb.Field{
											{
												Name: proto.String("x"),
												Type: &avrocpb.Type{
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
			},
		},
	}

	got := canonicalForm(schema)
	expected := `{"name":"com.example.Outer","type":"record","fields":[{"name":"inner","type":{"name":"com.example.Inner","type":"record","fields":[{"name":"x","type":"int"}]}}]}`

	if got != expected {
		t.Errorf("canonicalForm mismatch\ngot:  %s\nwant: %s", got, expected)
	}
}
