// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"strings"
	"testing"

	"github.com/z5labs/avro-go/idl"
)

func TestValidateSchema_PrimitiveIdents(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "User",
			Fields: []*idl.Field{
				{Name: "name", Type: &idl.Ident{Value: "string"}},
				{Name: "age", Type: &idl.Ident{Value: "int"}},
				{Name: "active", Type: &idl.Ident{Value: "boolean"}},
				{Name: "balance", Type: &idl.Ident{Value: "double"}},
			},
		},
	}

	err := validateSchema(schema)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateSchema_DefinedNamedType(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Order",
			Fields: []*idl.Field{
				{Name: "item", Type: &idl.Ident{Value: "Item"}},
			},
		},
		Types: []idl.Type{
			&idl.Record{
				Name: "Item",
				Fields: []*idl.Field{
					{Name: "name", Type: &idl.Ident{Value: "string"}},
				},
			},
		},
	}

	err := validateSchema(schema)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateSchema_UndefinedType(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Order",
			Fields: []*idl.Field{
				{Name: "address", Type: &idl.Ident{Value: "Address"}},
			},
		},
	}

	err := validateSchema(schema)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Address") {
		t.Fatalf("expected error to mention Address, got: %v", err)
	}
}

func TestValidateSchema_UndefinedTypeInArray(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Order",
			Fields: []*idl.Field{
				{Name: "items", Type: &idl.Array{Items: &idl.Ident{Value: "Item"}}},
			},
		},
	}

	err := validateSchema(schema)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Item") {
		t.Fatalf("expected error to mention Item, got: %v", err)
	}
}

func TestValidateSchema_UndefinedTypeInMap(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Inventory",
			Fields: []*idl.Field{
				{Name: "counts", Type: &idl.Map{Values: &idl.Ident{Value: "Widget"}}},
			},
		},
	}

	err := validateSchema(schema)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Widget") {
		t.Fatalf("expected error to mention Widget, got: %v", err)
	}
}

func TestValidateSchema_UndefinedTypeInUnion(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Event",
			Fields: []*idl.Field{
				{
					Name: "payload",
					Type: &idl.Union{
						Types: []idl.Type{
							&idl.Ident{Value: "null"},
							&idl.Ident{Value: "Payload"},
						},
					},
				},
			},
		},
	}

	err := validateSchema(schema)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Payload") {
		t.Fatalf("expected error to mention Payload, got: %v", err)
	}
}

func TestValidateSchema_FullyQualifiedReference(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Order",
			Fields: []*idl.Field{
				{Name: "item", Type: &idl.Ident{Value: "com.other.Item"}},
			},
		},
		Types: []idl.Type{
			&idl.Record{
				Name:      "Item",
				Namespace: "com.other",
				Fields: []*idl.Field{
					{Name: "name", Type: &idl.Ident{Value: "string"}},
				},
			},
		},
	}

	err := validateSchema(schema)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateSchema_SchemaNamespaceQualifiedReference(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Order",
			Fields: []*idl.Field{
				{Name: "item", Type: &idl.Ident{Value: "com.example.Item"}},
			},
		},
		Types: []idl.Type{
			&idl.Record{
				Name: "Item",
				Fields: []*idl.Field{
					{Name: "name", Type: &idl.Ident{Value: "string"}},
				},
			},
		},
	}

	err := validateSchema(schema)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateSchema_PrimaryTypeIsReferenced(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Node",
			Fields: []*idl.Field{
				{Name: "value", Type: &idl.Ident{Value: "string"}},
			},
		},
		Types: []idl.Type{
			&idl.Record{
				Name: "Tree",
				Fields: []*idl.Field{
					{Name: "root", Type: &idl.Ident{Value: "Node"}},
				},
			},
		},
	}

	err := validateSchema(schema)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateSchema_MultipleUndefinedTypes(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Order",
			Fields: []*idl.Field{
				{Name: "address", Type: &idl.Ident{Value: "Address"}},
				{Name: "payment", Type: &idl.Ident{Value: "Payment"}},
			},
		},
	}

	err := validateSchema(schema)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Address") {
		t.Fatalf("expected error to mention Address, got: %v", err)
	}
	if !strings.Contains(msg, "Payment") {
		t.Fatalf("expected error to mention Payment, got: %v", err)
	}
}

func TestValidateSchema_EnumPrimaryType(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Enum{
			Name:   "Color",
			Values: []*idl.Ident{{Value: "RED"}, {Value: "GREEN"}, {Value: "BLUE"}},
		},
	}

	err := validateSchema(schema)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateSchema_FixedPrimaryType(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Fixed{
			Name: "MD5",
			Size: 16,
		},
	}

	err := validateSchema(schema)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}
