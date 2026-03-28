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

func TestValidateSchema_EnumValidDefault(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Enum{
			Name:    "Status",
			Values:  []*idl.Ident{{Value: "ACTIVE"}, {Value: "INACTIVE"}},
			Default: &idl.Ident{Value: "ACTIVE"},
		},
	}

	err := validateSchema(schema)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateSchema_EnumInvalidDefault(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Enum{
			Name:    "Status",
			Values:  []*idl.Ident{{Value: "ACTIVE"}, {Value: "INACTIVE"}},
			Default: &idl.Ident{Value: "UNKNOWN"},
		},
	}

	err := validateSchema(schema)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "UNKNOWN") {
		t.Fatalf("expected error to mention UNKNOWN, got: %v", err)
	}
}

func TestValidateSchema_EnumNilDefault(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Enum{
			Name:   "Status",
			Values: []*idl.Ident{{Value: "ACTIVE"}, {Value: "INACTIVE"}},
		},
	}

	err := validateSchema(schema)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateSchema_EnumInvalidDefaultInSupportingType(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "User",
			Fields: []*idl.Field{
				{Name: "name", Type: &idl.Ident{Value: "string"}},
			},
		},
		Types: []idl.Type{
			&idl.Enum{
				Name:    "Role",
				Values:  []*idl.Ident{{Value: "ADMIN"}, {Value: "USER"}},
				Default: &idl.Ident{Value: "GUEST"},
			},
		},
	}

	err := validateSchema(schema)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "GUEST") {
		t.Fatalf("expected error to mention GUEST, got: %v", err)
	}
}

func TestValidateSchema_NestedEnumInvalidDefault(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Container",
			Fields: []*idl.Field{
				{
					Name: "items",
					Type: &idl.Array{
						Items: &idl.Enum{
							Name:    "Priority",
							Values:  []*idl.Ident{{Value: "HIGH"}, {Value: "LOW"}},
							Default: &idl.Ident{Value: "MEDIUM"},
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
	if !strings.Contains(err.Error(), "MEDIUM") {
		t.Fatalf("expected error to mention MEDIUM, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultStringValid(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "User",
			Fields: []*idl.Field{
				{Name: "name", Type: &idl.Ident{Value: "string"}, Default: idl.StringValue("unknown")},
			},
		},
	}

	err := validateSchema(schema)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultStringInvalid(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "User",
			Fields: []*idl.Field{
				{Name: "name", Type: &idl.Ident{Value: "string"}, Default: idl.IntValue(42)},
			},
		},
	}

	err := validateSchema(schema)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Fatalf("expected error to mention field name, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultIntValid(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Config",
			Fields: []*idl.Field{
				{Name: "retries", Type: &idl.Ident{Value: "int"}, Default: idl.IntValue(3)},
			},
		},
	}

	err := validateSchema(schema)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultIntInvalid(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Config",
			Fields: []*idl.Field{
				{Name: "retries", Type: &idl.Ident{Value: "int"}, Default: idl.StringValue("three")},
			},
		},
	}

	err := validateSchema(schema)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "retries") {
		t.Fatalf("expected error to mention field name, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultBooleanValid(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Config",
			Fields: []*idl.Field{
				{Name: "enabled", Type: &idl.Ident{Value: "boolean"}, Default: idl.BoolValue(true)},
			},
		},
	}

	err := validateSchema(schema)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultBooleanInvalid(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Config",
			Fields: []*idl.Field{
				{Name: "enabled", Type: &idl.Ident{Value: "boolean"}, Default: idl.IntValue(1)},
			},
		},
	}

	err := validateSchema(schema)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "enabled") {
		t.Fatalf("expected error to mention field name, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultNullValid(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "User",
			Fields: []*idl.Field{
				{Name: "middle", Type: &idl.Ident{Value: "null"}, Default: idl.NullValue{}},
			},
		},
	}

	err := validateSchema(schema)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultNullInvalid(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "User",
			Fields: []*idl.Field{
				{Name: "middle", Type: &idl.Ident{Value: "null"}, Default: idl.StringValue("none")},
			},
		},
	}

	err := validateSchema(schema)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "middle") {
		t.Fatalf("expected error to mention field name, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultFloatAcceptsInt(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Metrics",
			Fields: []*idl.Field{
				{Name: "rate", Type: &idl.Ident{Value: "float"}, Default: idl.IntValue(0)},
				{Name: "score", Type: &idl.Ident{Value: "double"}, Default: idl.FloatValue(1.5)},
			},
		},
	}

	err := validateSchema(schema)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultFloatInvalid(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Metrics",
			Fields: []*idl.Field{
				{Name: "rate", Type: &idl.Ident{Value: "float"}, Default: idl.StringValue("fast")},
			},
		},
	}

	err := validateSchema(schema)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "rate") {
		t.Fatalf("expected error to mention field name, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultArrayValid(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "User",
			Fields: []*idl.Field{
				{Name: "tags", Type: &idl.Array{Items: &idl.Ident{Value: "string"}}, Default: idl.ArrayValue{}},
			},
		},
	}

	err := validateSchema(schema)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultArrayInvalid(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "User",
			Fields: []*idl.Field{
				{Name: "tags", Type: &idl.Array{Items: &idl.Ident{Value: "string"}}, Default: idl.StringValue("none")},
			},
		},
	}

	err := validateSchema(schema)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "tags") {
		t.Fatalf("expected error to mention field name, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultMapValid(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Config",
			Fields: []*idl.Field{
				{Name: "labels", Type: &idl.Map{Values: &idl.Ident{Value: "string"}}, Default: idl.ObjectValue{}},
			},
		},
	}

	err := validateSchema(schema)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultMapInvalid(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Config",
			Fields: []*idl.Field{
				{Name: "labels", Type: &idl.Map{Values: &idl.Ident{Value: "string"}}, Default: idl.ArrayValue{}},
			},
		},
	}

	err := validateSchema(schema)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "labels") {
		t.Fatalf("expected error to mention field name, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultEnumValid(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "User",
			Fields: []*idl.Field{
				{
					Name: "status",
					Type: &idl.Enum{
						Name:   "Status",
						Values: []*idl.Ident{{Value: "ACTIVE"}, {Value: "INACTIVE"}},
					},
					Default: idl.StringValue("ACTIVE"),
				},
			},
		},
	}

	err := validateSchema(schema)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultEnumInvalidSymbol(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "User",
			Fields: []*idl.Field{
				{
					Name: "status",
					Type: &idl.Enum{
						Name:   "Status",
						Values: []*idl.Ident{{Value: "ACTIVE"}, {Value: "INACTIVE"}},
					},
					Default: idl.StringValue("DELETED"),
				},
			},
		},
	}

	err := validateSchema(schema)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "DELETED") {
		t.Fatalf("expected error to mention DELETED, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultUnionMatchesFirstType(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "User",
			Fields: []*idl.Field{
				{
					Name: "nickname",
					Type: &idl.Union{
						Types: []idl.Type{
							&idl.Ident{Value: "null"},
							&idl.Ident{Value: "string"},
						},
					},
					Default: idl.NullValue{},
				},
			},
		},
	}

	err := validateSchema(schema)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultUnionMismatchesFirstType(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "User",
			Fields: []*idl.Field{
				{
					Name: "nickname",
					Type: &idl.Union{
						Types: []idl.Type{
							&idl.Ident{Value: "null"},
							&idl.Ident{Value: "string"},
						},
					},
					Default: idl.StringValue("anon"),
				},
			},
		},
	}

	err := validateSchema(schema)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "nickname") {
		t.Fatalf("expected error to mention field name, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultNamedTypeReference(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "User",
			Fields: []*idl.Field{
				{Name: "role", Type: &idl.Ident{Value: "Role"}, Default: idl.StringValue("USER")},
			},
		},
		Types: []idl.Type{
			&idl.Enum{
				Name:   "Role",
				Values: []*idl.Ident{{Value: "ADMIN"}, {Value: "USER"}},
			},
		},
	}

	err := validateSchema(schema)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultNamedTypeReferenceInvalid(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "User",
			Fields: []*idl.Field{
				{Name: "role", Type: &idl.Ident{Value: "Role"}, Default: idl.StringValue("GUEST")},
			},
		},
		Types: []idl.Type{
			&idl.Enum{
				Name:   "Role",
				Values: []*idl.Ident{{Value: "ADMIN"}, {Value: "USER"}},
			},
		},
	}

	err := validateSchema(schema)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "GUEST") {
		t.Fatalf("expected error to mention GUEST, got: %v", err)
	}
}

func TestValidateSchema_FieldDefaultNilSkipped(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "User",
			Fields: []*idl.Field{
				{Name: "name", Type: &idl.Ident{Value: "string"}},
				{Name: "age", Type: &idl.Ident{Value: "int"}},
			},
		},
	}

	err := validateSchema(schema)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateSchema_BareNameDoesNotResolveToOtherNamespace(t *testing.T) {
	// A bare "Item" reference from com.example should NOT resolve to com.other.Item
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
				Name:      "Item",
				Namespace: "com.other",
				Fields: []*idl.Field{
					{Name: "name", Type: &idl.Ident{Value: "string"}},
				},
			},
		},
	}

	err := validateSchema(schema)
	if err == nil {
		t.Fatal("expected error for unresolved bare Item reference, got nil")
	}
	if !strings.Contains(err.Error(), "Item") {
		t.Fatalf("expected error to mention Item, got: %v", err)
	}
}

func TestValidateSchema_WrongFullyQualifiedNamespace(t *testing.T) {
	// A reference to "com.example.Item" should NOT resolve to com.other.Item
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
				Name:      "Item",
				Namespace: "com.other",
				Fields: []*idl.Field{
					{Name: "name", Type: &idl.Ident{Value: "string"}},
				},
			},
		},
	}

	err := validateSchema(schema)
	if err == nil {
		t.Fatal("expected error for unresolved com.example.Item reference, got nil")
	}
	if !strings.Contains(err.Error(), "com.example.Item") {
		t.Fatalf("expected error to mention com.example.Item, got: %v", err)
	}
}

func TestValidateSchema_NoNamespaceType(t *testing.T) {
	// A type with no namespace in a schema with no namespace should be resolvable by bare name
	schema := &idl.Schema{
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
