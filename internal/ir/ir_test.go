// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package ir

import (
	"testing"

	"github.com/z5labs/avroc/avrocpb"

	"google.golang.org/protobuf/proto"
)

func arrayOf(items *avrocpb.Type) *avrocpb.Type {
	return &avrocpb.Type{
		Type: &avrocpb.Type_Array{Array: &avrocpb.Array{Items: items}},
	}
}

func mapOf(values *avrocpb.Type) *avrocpb.Type {
	return &avrocpb.Type{
		Type: &avrocpb.Type_MapType{MapType: &avrocpb.Map{Values: values}},
	}
}

func TestSchemaBaseName(t *testing.T) {
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
			want: "MyRecord",
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
			want: "MyEnum",
		},
		{
			name: "fixed type",
			schema: &avrocpb.Schema{
				Type: &avrocpb.Type{
					Type: &avrocpb.Type_Fixed{
						Fixed: &avrocpb.Fixed{Name: proto.String("MD5")},
					},
				},
			},
			want: "MD5",
		},
		{
			name:   "reference primary type",
			schema: &avrocpb.Schema{Type: namedRef("com.example.MyRecord")},
			want:   "com.example.MyRecord",
		},
		{
			name: "primitive primary type falls back to the namespace",
			schema: &avrocpb.Schema{
				Namespace: proto.String("org.example"),
				Type:      primRef("string"),
			},
			want: "example",
		},
		{
			name: "array root names its item type",
			schema: &avrocpb.Schema{
				Namespace: proto.String("org.example"),
				Type: arrayOf(&avrocpb.Type{
					Type: &avrocpb.Type_Record{
						Record: &avrocpb.Record{Name: proto.String("Event")},
					},
				}),
			},
			want: "Event",
		},
		{
			name: "nested array root names the type it finally reaches",
			schema: &avrocpb.Schema{
				Namespace: proto.String("org.example"),
				Type: arrayOf(arrayOf(&avrocpb.Type{
					Type: &avrocpb.Type_EnumType{
						EnumType: &avrocpb.Enum{Name: proto.String("Suit")},
					},
				})),
			},
			want: "Suit",
		},
		{
			name: "map root names its value type",
			schema: &avrocpb.Schema{
				Namespace: proto.String("org.example"),
				Type: mapOf(&avrocpb.Type{
					Type: &avrocpb.Type_Fixed{
						Fixed: &avrocpb.Fixed{Name: proto.String("MD5")},
					},
				}),
			},
			want: "MD5",
		},
		{
			name: "array of a reference names what it references",
			schema: &avrocpb.Schema{
				Namespace: proto.String("org.example"),
				Type:      arrayOf(namedRef("org.example.Event")),
			},
			want: "org.example.Event",
		},
		{
			name: "array of a primitive falls back to the namespace",
			schema: &avrocpb.Schema{
				Namespace: proto.String("org.example"),
				Type:      arrayOf(primRef("string")),
			},
			want: "example",
		},
		{
			name: "array of a primitive prefers an additional type",
			schema: &avrocpb.Schema{
				Namespace: proto.String("org.example"),
				Type:      arrayOf(primRef("string")),
				Types: []*avrocpb.Type{
					{
						Type: &avrocpb.Type_Record{
							Record: &avrocpb.Record{Name: proto.String("Leftover")},
						},
					},
				},
			},
			want: "Leftover",
		},
		{
			name: "union root has no single subject",
			schema: &avrocpb.Schema{
				Namespace: proto.String("org.example"),
				Type: &avrocpb.Type{
					Type: &avrocpb.Type_Union{
						Union: &avrocpb.Union{
							Types: []*avrocpb.Type{primRef("null"), namedRef("org.example.Event")},
						},
					},
				},
			},
			want: "example",
		},
		{
			name: "first additional type",
			schema: &avrocpb.Schema{
				Types: []*avrocpb.Type{
					{
						Type: &avrocpb.Type_Record{
							Record: &avrocpb.Record{Name: proto.String("Leftover")},
						},
					},
				},
			},
			want: "Leftover",
		},
		{
			name:   "namespace fallback",
			schema: &avrocpb.Schema{Namespace: proto.String("com.example.events")},
			want:   "events",
		},
		{
			name:   "empty schema",
			schema: &avrocpb.Schema{},
			want:   "schema",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SchemaBaseName(tt.schema); got != tt.want {
				t.Errorf("SchemaBaseName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSnakeCase(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "MyRecord", want: "my_record"},
		{in: "TestRecord", want: "test_record"},
		{in: "MD5", want: "m_d5"},
		{in: "com.example.MyRecord", want: "my_record"},
		{in: "lower", want: "lower"},
		{in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := SnakeCase(tt.in); got != tt.want {
				t.Errorf("SnakeCase(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNamedTypeNameAndFullName(t *testing.T) {
	record := &avrocpb.Type{
		Type: &avrocpb.Type_Record{
			Record: &avrocpb.Record{
				Name:     proto.String("Person"),
				FullName: proto.String("com.example.Person"),
			},
		},
	}

	if got := NamedTypeName(record); got != "Person" {
		t.Errorf("NamedTypeName() = %q, want %q", got, "Person")
	}
	if got := FullName(record); got != "com.example.Person" {
		t.Errorf("FullName() = %q, want %q", got, "com.example.Person")
	}

	// A reference is not a named type's definition, so neither helper reports
	// one: that is what tells a definition apart from a use.
	if got := NamedTypeName(namedRef("com.example.Person")); got != "" {
		t.Errorf("NamedTypeName(reference) = %q, want %q", got, "")
	}
	if got := FullName(namedRef("com.example.Person")); got != "" {
		t.Errorf("FullName(reference) = %q, want %q", got, "")
	}
}
