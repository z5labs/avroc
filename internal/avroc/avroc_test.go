// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"testing"

	"github.com/z5labs/avroc/internal/avrocpb"

	"github.com/z5labs/avro-go/idl"
	"google.golang.org/protobuf/proto"
)

func TestMapToProtoSchema_Record(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name:      "User",
			Namespace: "com.example",
			Aliases:   []string{"Person"},
			Fields: []*idl.Field{
				{
					Name:      "name",
					Type:      &idl.Ident{Value: "string"},
					SortOrder: idl.SortOrderAsc,
				},
				{
					Name:    "age",
					Aliases: []string{"years"},
					Type:    &idl.Ident{Value: "int"},
				},
				{
					Name: "tags",
					Type: &idl.Array{
						Items: &idl.Ident{Value: "string"},
					},
				},
				{
					Name: "metadata",
					Type: &idl.Map{
						Values: &idl.Ident{Value: "string"},
					},
				},
				{
					Name: "optionalField",
					Type: &idl.Union{
						Types: []idl.Type{
							&idl.Ident{Value: "null"},
							&idl.Ident{Value: "string"},
						},
					},
				},
			},
		},
	}

	got, err := mapToProtoSchema(schema)
	if err != nil {
		t.Fatal(err)
	}

	if got.GetNamespace() != "com.example" {
		t.Errorf("namespace = %q, want %q", got.GetNamespace(), "com.example")
	}

	rec := got.GetType().GetRecord()
	if rec == nil {
		t.Fatal("expected record type")
	}
	if rec.GetName() != "User" {
		t.Errorf("record name = %q, want %q", rec.GetName(), "User")
	}
	if len(rec.GetAliases()) != 1 || rec.GetAliases()[0] != "Person" {
		t.Errorf("record aliases = %v, want [Person]", rec.GetAliases())
	}
	if len(rec.GetFields()) != 5 {
		t.Fatalf("fields count = %d, want 5", len(rec.GetFields()))
	}

	// name field
	nameField := rec.GetFields()[0]
	if nameField.GetName() != "name" {
		t.Errorf("field[0] name = %q, want %q", nameField.GetName(), "name")
	}
	if nameField.GetType().GetIdent().GetValue() != "string" {
		t.Errorf("field[0] type = %v, want ident(string)", nameField.GetType())
	}
	if nameField.GetSortOrder() != avrocpb.SortOrder_SORT_ORDER_ASC {
		t.Errorf("field[0] sort_order = %v, want ASC", nameField.GetSortOrder())
	}

	// age field with alias
	ageField := rec.GetFields()[1]
	if len(ageField.GetAliases()) != 1 || ageField.GetAliases()[0] != "years" {
		t.Errorf("field[1] aliases = %v, want [years]", ageField.GetAliases())
	}

	// tags field (array)
	tagsField := rec.GetFields()[2]
	arr := tagsField.GetType().GetArray()
	if arr == nil {
		t.Fatal("expected array type for tags field")
	}
	if arr.GetItems().GetIdent().GetValue() != "string" {
		t.Errorf("tags items type = %v, want ident(string)", arr.GetItems())
	}

	// metadata field (map)
	metaField := rec.GetFields()[3]
	m := metaField.GetType().GetMapType()
	if m == nil {
		t.Fatal("expected map type for metadata field")
	}
	if m.GetValues().GetValue() != "string" {
		t.Errorf("metadata values type = %q, want %q", m.GetValues().GetValue(), "string")
	}

	// optionalField (union)
	unionField := rec.GetFields()[4]
	u := unionField.GetType().GetUnion()
	if u == nil {
		t.Fatal("expected union type for optionalField")
	}
	if len(u.GetTypes()) != 2 {
		t.Fatalf("union types count = %d, want 2", len(u.GetTypes()))
	}
}

func TestMapToProtoSchema_Enum(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Enum{
			Name:      "Status",
			Namespace: "com.example",
			Values: []*idl.Ident{
				{Value: "ACTIVE"},
				{Value: "INACTIVE"},
			},
			Default: &idl.Ident{Value: "ACTIVE"},
		},
	}

	got, err := mapToProtoSchema(schema)
	if err != nil {
		t.Fatal(err)
	}

	enum := got.GetType().GetEnumType()
	if enum == nil {
		t.Fatal("expected enum type")
	}
	if enum.GetName() != "Status" {
		t.Errorf("enum name = %q, want %q", enum.GetName(), "Status")
	}
	if len(enum.GetValues()) != 2 {
		t.Fatalf("enum values count = %d, want 2", len(enum.GetValues()))
	}
	if enum.GetValues()[0].GetValue() != "ACTIVE" {
		t.Errorf("enum value[0] = %q, want %q", enum.GetValues()[0].GetValue(), "ACTIVE")
	}
	if enum.GetDefault().GetValue() != "ACTIVE" {
		t.Errorf("enum default = %q, want %q", enum.GetDefault().GetValue(), "ACTIVE")
	}
}

func TestMapToProtoSchema_Fixed(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Fixed{
			Name:    "MD5",
			Aliases: []string{"Hash"},
			Size:    16,
		},
	}

	got, err := mapToProtoSchema(schema)
	if err != nil {
		t.Fatal(err)
	}

	fixed := got.GetType().GetFixed()
	if fixed == nil {
		t.Fatal("expected fixed type")
	}
	if fixed.GetName() != "MD5" {
		t.Errorf("fixed name = %q, want %q", fixed.GetName(), "MD5")
	}
	if fixed.GetSize() != 16 {
		t.Errorf("fixed size = %d, want 16", fixed.GetSize())
	}
	if len(fixed.GetAliases()) != 1 || fixed.GetAliases()[0] != "Hash" {
		t.Errorf("fixed aliases = %v, want [Hash]", fixed.GetAliases())
	}
}

func TestMapToProtoSchema_WithSupportingTypes(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Order",
			Fields: []*idl.Field{
				{
					Name: "item",
					Type: &idl.Ident{Value: "Item"},
				},
			},
		},
		Types: []idl.Type{
			&idl.Record{
				Name: "Item",
				Fields: []*idl.Field{
					{
						Name: "name",
						Type: &idl.Ident{Value: "string"},
					},
				},
			},
		},
	}

	got, err := mapToProtoSchema(schema)
	if err != nil {
		t.Fatal(err)
	}

	if got.GetType().GetRecord().GetName() != "Order" {
		t.Errorf("primary type name = %q, want %q", got.GetType().GetRecord().GetName(), "Order")
	}
	if len(got.GetTypes()) != 1 {
		t.Fatalf("supporting types count = %d, want 1", len(got.GetTypes()))
	}
	if got.GetTypes()[0].GetRecord().GetName() != "Item" {
		t.Errorf("supporting type name = %q, want %q", got.GetTypes()[0].GetRecord().GetName(), "Item")
	}
}

func TestMapToProtoSchema_EnumNilDefault(t *testing.T) {
	schema := &idl.Schema{
		Type: &idl.Enum{
			Name: "Color",
			Values: []*idl.Ident{
				{Value: "RED"},
			},
		},
	}

	got, err := mapToProtoSchema(schema)
	if err != nil {
		t.Fatal(err)
	}

	if got.GetType().GetEnumType().GetDefault() != nil {
		t.Errorf("expected nil default, got %v", got.GetType().GetEnumType().GetDefault())
	}
}

// Verify proto.String is used correctly by checking roundtrip.
func TestMapToProtoSchema_ProtoStringFields(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "ns",
		Type: &idl.Ident{
			Value: "string",
		},
	}

	got, err := mapToProtoSchema(schema)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the proto message can be serialized and deserialized.
	data, err := proto.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}

	var roundtrip avrocpb.Schema
	if err := proto.Unmarshal(data, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if roundtrip.GetNamespace() != "ns" {
		t.Errorf("roundtrip namespace = %q, want %q", roundtrip.GetNamespace(), "ns")
	}
	if roundtrip.GetType().GetIdent().GetValue() != "string" {
		t.Errorf("roundtrip type = %v, want ident(string)", roundtrip.GetType())
	}
}
