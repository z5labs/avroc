// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/ir"

	"github.com/z5labs/avro-go/idl"
)

// TestEverySortOrderReachesTheDescriptor takes each order from IDL text all the
// way to a decoded descriptor: parsed off disk, resolved, encoded with
// ir.MarshalDescriptor (which is what writeDescriptor encodes with), and decoded
// the way a generator decodes it. It is
// the descriptor a generator reads that has to carry the order, so asserting on
// the resolver's return value alone would miss a step that dropped or remapped
// it.
func TestEverySortOrderReachesTheDescriptor(t *testing.T) {
	const src = `namespace com.example;
schema Event;

record Event {
  string plain;
  string @order("ascending") ascending;
  string @order("descending") descending;
  string @order("ignore") ignored;
}
`
	path := filepath.Join(t.TempDir(), "event.avdl")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	schema, err := loadSchema(context.Background(), "event.avdl", path)
	if err != nil {
		t.Fatal(err)
	}

	b, err := ir.MarshalDescriptor(newDescriptor(nil, []*avrocpb.Schema{schema}))
	if err != nil {
		t.Fatal(err)
	}
	desc, err := ir.UnmarshalDescriptor(b)
	if err != nil {
		t.Fatal(err)
	}

	if n := len(desc.GetSchemas()); n != 1 {
		t.Fatalf("descriptor carries %d schemas, want 1", n)
	}
	rec := desc.GetSchemas()[0].GetType().GetRecord()
	if rec == nil {
		t.Fatal("expected the schema's root to be a record")
	}

	want := []struct {
		name  string
		order avrocpb.SortOrder
	}{
		{name: "plain", order: avrocpb.SortOrder_SORT_ORDER_ASC},
		{name: "ascending", order: avrocpb.SortOrder_SORT_ORDER_ASC},
		{name: "descending", order: avrocpb.SortOrder_SORT_ORDER_DESC},
		{name: "ignored", order: avrocpb.SortOrder_SORT_ORDER_IGNORE},
	}
	fields := rec.GetFields()
	if len(fields) != len(want) {
		t.Fatalf("record has %d fields, want %d", len(fields), len(want))
	}
	for i, w := range want {
		f := fields[i]
		if f.GetName() != w.name {
			t.Errorf("field[%d] name = %q, want %q", i, f.GetName(), w.name)
		}
		// Present, and not merely defaulted: an absent sort_order reads as ASC
		// through the getter, which would let a field that lost its order pass
		// as the unannotated one.
		if f.SortOrder == nil {
			t.Errorf("field %q carries no sort_order", w.name)
			continue
		}
		if got := f.GetSortOrder(); got != w.order {
			t.Errorf("field %q sort_order = %v, want %v", w.name, got, w.order)
		}
	}
}

func TestResolveSchema_RejectsAnUnknownSortOrder(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Event",
			Fields: []*idl.Field{
				{Name: "ok", Type: &idl.Ident{Value: "string"}},
				{
					Name: "strange",
					Type: &idl.Ident{Value: "string"},
					// Far outside any plausible enum rather than one past the
					// last constant, so the test does not itself rest on
					// avro-go's numbering.
					SortOrder: idl.SortOrder(math.MaxInt),
				},
			},
		},
	}

	_, err := resolveSchema(schema)
	if err == nil {
		t.Fatal("expected an unknown sort order to fail resolution")
	}
	for _, want := range []string{`"com.example.Event"`, `"strange"`, "sort order"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}
