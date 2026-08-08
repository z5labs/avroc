// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"strings"
	"testing"

	"github.com/z5labs/avroc/internal/avrocpb"

	"github.com/z5labs/avro-go/idl"
	"google.golang.org/protobuf/proto"
)

func TestResolveSchema_Record(t *testing.T) {
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

	got, err := resolveSchema(schema)
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
	if rec.GetFullName() != "com.example.User" {
		t.Errorf("record full name = %q, want %q", rec.GetFullName(), "com.example.User")
	}
	// Aliases are names, and are qualified the same way the full name is.
	if len(rec.GetAliases()) != 1 || rec.GetAliases()[0] != "com.example.Person" {
		t.Errorf("record aliases = %v, want [com.example.Person]", rec.GetAliases())
	}
	if len(rec.GetFields()) != 5 {
		t.Fatalf("fields count = %d, want 5", len(rec.GetFields()))
	}

	// name field
	nameField := rec.GetFields()[0]
	if nameField.GetName() != "name" {
		t.Errorf("field[0] name = %q, want %q", nameField.GetName(), "name")
	}
	assertPrimitive(t, nameField.GetType(), "string")
	if nameField.GetSortOrder() != avrocpb.SortOrder_SORT_ORDER_ASC {
		t.Errorf("field[0] sort_order = %v, want ASC", nameField.GetSortOrder())
	}

	// age field with alias. A field alias names a field, not a type, so it is
	// left alone where a named type's alias is qualified.
	ageField := rec.GetFields()[1]
	if len(ageField.GetAliases()) != 1 || ageField.GetAliases()[0] != "years" {
		t.Errorf("field[1] aliases = %v, want [years]", ageField.GetAliases())
	}

	// tags field (array)
	arr := rec.GetFields()[2].GetType().GetArray()
	if arr == nil {
		t.Fatal("expected array type for tags field")
	}
	assertPrimitive(t, arr.GetItems(), "string")

	// metadata field (map)
	m := rec.GetFields()[3].GetType().GetMapType()
	if m == nil {
		t.Fatal("expected map type for metadata field")
	}
	assertPrimitive(t, m.GetValues(), "string")

	// optionalField (union)
	u := rec.GetFields()[4].GetType().GetUnion()
	if u == nil {
		t.Fatal("expected union type for optionalField")
	}
	if len(u.GetTypes()) != 2 {
		t.Fatalf("union types count = %d, want 2", len(u.GetTypes()))
	}
	assertPrimitive(t, u.GetTypes()[0], "null")
	assertPrimitive(t, u.GetTypes()[1], "string")
}

func TestResolveSchema_Enum(t *testing.T) {
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

	got, err := resolveSchema(schema)
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
	if enum.GetFullName() != "com.example.Status" {
		t.Errorf("enum full name = %q, want %q", enum.GetFullName(), "com.example.Status")
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

// TestResolveSchema_InheritsSchemaNamespace pins the rule a generator no longer
// applies: a declaration without a namespace of its own takes the enclosing one,
// and the descriptor carries the answer.
func TestResolveSchema_InheritsSchemaNamespace(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Fixed{
			Name:    "MD5",
			Aliases: []string{"Hash"},
			Size:    16,
		},
	}

	got, err := resolveSchema(schema)
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
	if fixed.GetNamespace() != "com.example" {
		t.Errorf("fixed namespace = %q, want %q", fixed.GetNamespace(), "com.example")
	}
	if fixed.GetFullName() != "com.example.MD5" {
		t.Errorf("fixed full name = %q, want %q", fixed.GetFullName(), "com.example.MD5")
	}
	if fixed.GetSize() != 16 {
		t.Errorf("fixed size = %d, want 16", fixed.GetSize())
	}
	if len(fixed.GetAliases()) != 1 || fixed.GetAliases()[0] != "com.example.Hash" {
		t.Errorf("fixed aliases = %v, want [com.example.Hash]", fixed.GetAliases())
	}
}

// TestResolveSchema_FirstUseIsWrittenOutInFull is the ordering decision the
// three generators used to make independently: the definition travels at the
// first use, and every later use is a fully-qualified reference.
func TestResolveSchema_FirstUseIsWrittenOutInFull(t *testing.T) {
	md5 := &idl.Fixed{Name: "MD5", Size: 16}
	schema := &idl.Schema{
		Namespace: "org.apache.avro.test",
		Type:      &idl.Ident{Value: "TestRecord"},
		Types: []idl.Type{
			md5,
			&idl.Record{
				Name: "TestRecord",
				Fields: []*idl.Field{
					{Name: "hash", Type: &idl.Ident{Value: "MD5"}},
					{
						Name: "nullableHash",
						Type: &idl.Union{
							Types: []idl.Type{
								&idl.Ident{Value: "null"},
								&idl.Ident{Value: "MD5"},
							},
						},
					},
				},
			},
		},
	}

	got, err := resolveSchema(schema)
	if err != nil {
		t.Fatal(err)
	}

	// The schema's own type names TestRecord, so its definition travels there
	// rather than staying behind as a reference.
	rec := got.GetType().GetRecord()
	if rec == nil {
		t.Fatalf("expected the primary type to carry the record definition, got %T", got.GetType().GetType())
	}
	if rec.GetFullName() != "org.apache.avro.test.TestRecord" {
		t.Errorf("record full name = %q", rec.GetFullName())
	}

	// First use of MD5 carries its definition.
	hash := rec.GetFields()[0].GetType().GetFixed()
	if hash == nil {
		t.Fatalf("expected field 'hash' to carry the fixed definition, got %T", rec.GetFields()[0].GetType().GetType())
	}
	if hash.GetFullName() != "org.apache.avro.test.MD5" {
		t.Errorf("fixed full name = %q", hash.GetFullName())
	}

	// Second use refers to it by fully-qualified name.
	branches := rec.GetFields()[1].GetType().GetUnion().GetTypes()
	if len(branches) != 2 {
		t.Fatalf("union branch count = %d, want 2", len(branches))
	}
	assertNamed(t, branches[1], "org.apache.avro.test.MD5")

	// Nothing is left over: every declaration was reached from the root.
	if len(got.GetTypes()) != 0 {
		t.Errorf("expected no unreferenced types, got %d", len(got.GetTypes()))
	}
}

// TestResolveSchema_UnreferencedTypeIsKept covers the other side: a declaration
// nothing refers to still belongs in the descriptor.
func TestResolveSchema_UnreferencedTypeIsKept(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name:   "Order",
			Fields: []*idl.Field{{Name: "id", Type: &idl.Ident{Value: "string"}}},
		},
		Types: []idl.Type{
			&idl.Record{
				Name:   "Item",
				Fields: []*idl.Field{{Name: "name", Type: &idl.Ident{Value: "string"}}},
			},
		},
	}

	got, err := resolveSchema(schema)
	if err != nil {
		t.Fatal(err)
	}

	if got.GetType().GetRecord().GetFullName() != "com.example.Order" {
		t.Errorf("primary type = %q, want com.example.Order", got.GetType().GetRecord().GetFullName())
	}
	if len(got.GetTypes()) != 1 {
		t.Fatalf("supporting types count = %d, want 1", len(got.GetTypes()))
	}
	if got.GetTypes()[0].GetRecord().GetFullName() != "com.example.Item" {
		t.Errorf("supporting type = %q, want com.example.Item", got.GetTypes()[0].GetRecord().GetFullName())
	}
}

// TestResolveSchema_ReferenceAcrossNamespaces proves a reference is qualified
// against the namespace enclosing it, and can name a type in another one.
func TestResolveSchema_ReferenceAcrossNamespaces(t *testing.T) {
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
				Fields:    []*idl.Field{{Name: "name", Type: &idl.Ident{Value: "string"}}},
			},
		},
	}

	got, err := resolveSchema(schema)
	if err != nil {
		t.Fatal(err)
	}

	item := got.GetType().GetRecord().GetFields()[0].GetType().GetRecord()
	if item == nil {
		t.Fatalf("expected field 'item' to carry the record definition")
	}
	if item.GetFullName() != "com.other.Item" {
		t.Errorf("item full name = %q, want com.other.Item", item.GetFullName())
	}
	// A record in another namespace does not adopt the enclosing one.
	if item.GetFields()[0].GetType().GetReference().GetName() != "string" {
		t.Errorf("nested field type = %v", item.GetFields()[0].GetType())
	}
}

// TestResolveSchema_RejectsUnresolvedReference is the spec's "a schema carrying
// a reference to neither is not resolved, and avroc MUST reject it".
func TestResolveSchema_RejectsUnresolvedReference(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name:   "Order",
			Fields: []*idl.Field{{Name: "item", Type: &idl.Ident{Value: "Item"}}},
		},
	}

	_, err := resolveSchema(schema)
	if err == nil {
		t.Fatal("expected an error for a reference naming neither a primitive nor a declaration")
	}
	if !strings.Contains(err.Error(), "unresolved type reference") {
		t.Errorf("error = %v, want it to name the unresolved reference", err)
	}
}

func TestResolveSchema_EnumNilDefault(t *testing.T) {
	schema := &idl.Schema{
		Type: &idl.Enum{
			Name:   "Color",
			Values: []*idl.Ident{{Value: "RED"}},
		},
	}

	got, err := resolveSchema(schema)
	if err != nil {
		t.Fatal(err)
	}

	if got.GetType().GetEnumType().GetDefault() != nil {
		t.Errorf("expected nil default, got %v", got.GetType().GetEnumType().GetDefault())
	}
	// With no namespace anywhere, the full name is the simple name.
	if got.GetType().GetEnumType().GetFullName() != "Color" {
		t.Errorf("enum full name = %q, want %q", got.GetType().GetEnumType().GetFullName(), "Color")
	}
}

func TestResolveSchema_ProtoRoundTrip(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "ns",
		Type:      &idl.Ident{Value: "string"},
	}

	got, err := resolveSchema(schema)
	if err != nil {
		t.Fatal(err)
	}

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
	assertPrimitive(t, roundtrip.GetType(), "string")
}

func assertPrimitive(t *testing.T, typ *avrocpb.Type, name string) {
	t.Helper()

	ref := typ.GetReference()
	if ref == nil {
		t.Fatalf("expected a reference, got %T", typ.GetType())
	}
	if ref.GetName() != name {
		t.Errorf("reference name = %q, want %q", ref.GetName(), name)
	}
	if ref.GetKind() != avrocpb.TypeRefKind_TYPE_REF_KIND_PRIMITIVE {
		t.Errorf("reference %q kind = %v, want PRIMITIVE", name, ref.GetKind())
	}
}

func assertNamed(t *testing.T, typ *avrocpb.Type, fullName string) {
	t.Helper()

	ref := typ.GetReference()
	if ref == nil {
		t.Fatalf("expected a reference, got %T", typ.GetType())
	}
	if ref.GetName() != fullName {
		t.Errorf("reference name = %q, want %q", ref.GetName(), fullName)
	}
	if ref.GetKind() != avrocpb.TypeRefKind_TYPE_REF_KIND_NAMED {
		t.Errorf("reference %q kind = %v, want NAMED", fullName, ref.GetKind())
	}
}
