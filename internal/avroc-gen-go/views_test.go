// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/ir"
	"google.golang.org/protobuf/proto"
)

// viewSampleSchemas is every shape a view indexes, in three schemas.
//
// Sample holds every type a view reads, each once before its first
// variable-size field where it can be — so that it is given an offset constant
// — and once after it; a nested record of constant width in both positions; a
// nested record of variable width, whose own tail is bytes, in the middle; and
// a nested record last, so that Sample's prefix ends inside it (leaf.text).
// Big has more than 63 symbols, so that an ordinal past the one-byte encoding
// reaches ConsumeIndex.
//
// Whole ends in a fixed, so its prefix is the whole record; it holds a record
// with no fields at all. Keyed ends in an ignored bytes field, so its Compare
// needs only the prefix.
//
// testdata/views/sample_test.go is what they are checked by.
func viewSampleSchemas() []*avrocpb.Schema {
	ns := unionTestNamespace
	big := make([]string, 70)
	for i := range big {
		big[i] = fmt.Sprintf("S%d", i)
	}

	sample := recordOf("Sample",
		fieldOf("flag", primType("boolean")),
		fieldOf("ratio", primType("float")),
		fieldOf("score", primType("double")),
		fieldOf("digest", fixedOf("Digest", 4)),
		fieldOf("pair", recordOf("Pair", fieldOf("on", primType("boolean")), fieldOf("tag", fixedOf("Tag", 2)))),
		fieldOf("count", primType("int")),
		fieldOf("total", primType("long")),
		fieldOf("blob", primType("bytes")),
		fieldOf("name", primType("string")),
		fieldOf("color", enumOf("Color", "RED", "GREEN", "BLUE")),
		fieldOf("big", enumOf("Big", big...)),
		fieldOf("inner", recordOf("Inner",
			fieldOf("id", primType("long")),
			fieldOf("label", primType("string")),
			fieldOf("data", primType("bytes")),
		)),
		fieldOf("digestAgain", namedType(ns+".Digest")),
		fieldOf("flagAgain", primType("boolean")),
		fieldOf("ratioAgain", primType("float")),
		fieldOf("scoreAgain", primType("double")),
		fieldOf("pairAgain", namedType(ns+".Pair")),
		descending("leaf", recordOf("Leaf", fieldOf("n", primType("int")), fieldOf("text", primType("string")))),
	)
	whole := recordOf("Whole",
		fieldOf("id", primType("long")),
		fieldOf("nothing", recordOf("Nothing")),
		fieldOf("color", namedType(ns+".Color")),
		fieldOf("digest", namedType(ns+".Digest")),
	)
	keyed := recordOf("Keyed",
		fieldOf("key", primType("bytes")),
		ignored("value", primType("bytes")),
	)

	return []*avrocpb.Schema{
		{Namespace: proto.String(ns), Type: sample},
		{Namespace: proto.String(ns), Type: whole},
		{Namespace: proto.String(ns), Type: keyed},
	}
}

// TestViewsAgreeWithUnmarshalOnEveryEncoding builds viewSampleSchemas with
// compare=true and views=true and runs testdata/views against them: Decode and
// every accessor against UnmarshalAvroBinary, every truncation and the Need
// bounds, the corrupt cases, the panics, View.Compare against
// Compare<Record>Avro, the allocation counts and the fuzz test's seed corpus.
func TestViewsAgreeWithUnmarshalOnEveryEncoding(t *testing.T) {
	testGeneratedPackageWith(t, "views", filepath.Join("testdata", "views", "sample_test.go"),
		[]string{"compare", "true", "views", "true"}, viewSampleSchemas()...)
}

func viewsRequest(views, compare string, schemas ...*avrocpb.Schema) *avrocpb.GenerateRequest {
	pairs := []string{"package_name", "gen"}
	if compare != "" {
		pairs = append(pairs, "compare", compare)
	}
	if views != "" {
		pairs = append(pairs, "views", views)
	}
	return &avrocpb.GenerateRequest{Options: options(pairs...), Schemas: schemas}
}

// generateViewsString is generateString under views=true.
func generateViewsString(t *testing.T, compare string, schemas ...*avrocpb.Schema) string {
	t.Helper()
	res, err := generateToDir(t, t.TempDir(), viewsRequest("true", compare, schemas...))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	var code strings.Builder
	for _, f := range res.OutputFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		validateGoSyntax(t, string(b))
		code.Write(b)
	}
	return code.String()
}

// TestTheViewsOptionIsExactlyTrueOrFalse is the option's spelling: true
// generates a view for every record, nested ones included, false and an absent
// option generate none, and anything else fails the run naming the value.
func TestTheViewsOptionIsExactlyTrueOrFalse(t *testing.T) {
	schema := &avrocpb.Schema{
		Namespace: proto.String(unionTestNamespace),
		Type: recordOf("Outer",
			fieldOf("id", primType("long")),
			fieldOf("inner", recordOf("Inner", fieldOf("name", primType("string")))),
		),
	}

	t.Run("true", func(t *testing.T) {
		code := generateViewsString(t, "", schema)
		for _, want := range []string{"type OuterView struct {", "type InnerView struct {", "func (v *OuterView) Parse(b []byte) error {", "func (v *OuterView) Inner() *InnerView {"} {
			if !strings.Contains(code, want) {
				t.Errorf("views=true did not generate %q", want)
			}
		}
	})
	for _, value := range []string{"false", ""} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			res, err := generateToDir(t, t.TempDir(), viewsRequest(value, "", schema))
			if err != nil {
				t.Fatalf("Generate failed: %v", err)
			}
			b, err := os.ReadFile(res.OutputFiles[0])
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "View") {
				t.Errorf("views=%q generated a view", value)
			}
		})
	}

	for _, value := range []string{"True", "1", "yes", "", " true"} {
		t.Run("refuses "+fmt.Sprintf("%q", value), func(t *testing.T) {
			req := &avrocpb.GenerateRequest{Options: options("package_name", "gen", "views", value), Schemas: []*avrocpb.Schema{schema}}
			_, err := generateToDir(t, t.TempDir(), req)
			if err == nil {
				t.Fatalf("views=%q was accepted", value)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("%q", value)) || !strings.Contains(err.Error(), "views") {
				t.Errorf("the diagnostic %q does not name the option and its value %q", err, value)
			}
		})
	}
}

// TestViewCompareIsGeneratedOnlyUnderCompare: View.Compare is built on
// Compare<Record>Avro, so it exists exactly when that does.
func TestViewCompareIsGeneratedOnlyUnderCompare(t *testing.T) {
	schema := schemaOf(recordOf("Rec", fieldOf("id", primType("long"))))
	if code := generateViewsString(t, "", schema); strings.Contains(code, ") Compare(") {
		t.Errorf("views=true alone generated View.Compare:\n%s", code)
	}
	if code := generateViewsString(t, "true", schema); !strings.Contains(code, "func (v *RecView) Compare(w *RecView) int {") {
		t.Errorf("views=true and compare=true did not generate View.Compare:\n%s", code)
	}
}

// TestViewsRefuseARecordOutsideTheirScope is the scope: every field a view
// indexes must be a primitive, an enum, a fixed or a record of those, with no
// exception for an ignored field — a view indexes every field. A record outside
// it fails the run, naming the record, the field and the field's type, before
// a single file is written.
func TestViewsRefuseARecordOutsideTheirScope(t *testing.T) {
	ns := unionTestNamespace
	testCases := []struct {
		name string
		root *avrocpb.Type
		// want is every substring the diagnostic must hold; nil means the
		// record is in scope.
		want []string
	}{
		{
			name: "a map",
			root: recordOf("Holder", fieldOf("id", primType("long")), fieldOf("tags", mapOf(primType("string")))),
			want: []string{"HolderView", "record com.example.Holder", "field tags", "type map"},
		},
		{
			name: "an array",
			root: recordOf("Holder", fieldOf("list", arrayOf(primType("long")))),
			want: []string{"record com.example.Holder", "field list", "type array"},
		},
		{
			name: "a union",
			root: recordOf("Holder", fieldOf("choice", unionOf(primType("null"), primType("long")))),
			want: []string{"record com.example.Holder", "field choice", "type union"},
		},
		{
			name: "an ignored trailing union",
			root: recordOf("Holder", fieldOf("id", primType("long")), ignored("choice", unionOf(primType("null"), primType("long")))),
			want: []string{"record com.example.Holder", "field choice", "type union"},
		},
		{
			name: "a nested record's map",
			root: recordOf("Holder",
				fieldOf("inner", recordOf("Inner", fieldOf("id", primType("long")), fieldOf("tags", mapOf(primType("long"))))),
			),
			want: []string{"HolderView", "record com.example.Holder", "field tags of record com.example.Inner", "inner.tags", "type map"},
		},
		{
			name: "a record containing itself",
			root: recordOf("Node", fieldOf("id", primType("long")), fieldOf("next", namedType(ns+".Node"))),
			want: []string{"record com.example.Node", "field next", "contains itself"},
		},
		{
			name: "every primitive, enum, fixed and record",
			root: recordOf("Holder",
				fieldOf("b", primType("boolean")), fieldOf("i", primType("int")), fieldOf("l", primType("long")),
				fieldOf("f", primType("float")), fieldOf("d", primType("double")), fieldOf("by", primType("bytes")),
				fieldOf("s", primType("string")), fieldOf("e", enumOf("E", "A")), fieldOf("x", fixedOf("X", 3)),
				fieldOf("r", recordOf("R", fieldOf("s", primType("string")))),
			),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// A first schema in scope, and a writer that refuses every file, so
			// that a check made inside the generation loop rather than before it
			// would return the write's error instead.
			first := &avrocpb.Schema{Namespace: proto.String(ns), Type: recordOf("First", fieldOf("id", primType("long")))}
			schema := &avrocpb.Schema{Namespace: proto.String(ns), Type: tc.root}
			req := viewsRequest("true", "", first, schema)
			req.Version = proto.Int32(ir.Version)

			err := Generate(t.Context(), req, failingWriter{})
			if tc.want == nil {
				if !errors.Is(err, errWriteRefused) {
					t.Fatalf("Generate returned %v, want the record accepted and the write refused", err)
				}
				return
			}
			if err == nil || errors.Is(err, errWriteRefused) {
				t.Fatalf("Generate returned %v, want the record refused before any file is written", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the diagnostic does not contain %q:\n%v", want, err)
				}
			}
		})
	}
}

// TestViewIdentifiersAreRegisteredWithTheCollisionCheck is #247 for the view:
// its type, its methods, its accessors and its offset constants are all
// identifiers the package declares, and each is refused where it collides —
// and only under views=true.
func TestViewIdentifiersAreRegisteredWithTheCollisionCheck(t *testing.T) {
	testCases := []struct {
		name string
		root *avrocpb.Type
		want []string
	}{
		{
			name: "a field named like a method",
			root: recordOf("Event", fieldOf("complete", primType("boolean"))),
			want: []string{"Complete collides in the fields and methods of type EventView", "method Complete", "accessor Complete for field complete"},
		},
		{
			name: "a fixed beside a field named for its bytes",
			root: recordOf("Event", fieldOf("hash", fixedOf("Hash", 8)), fieldOf("hashBytes", primType("bytes"))),
			want: []string{"HashBytes collides in the fields and methods of type EventView", "accessor HashBytes for field hash", "accessor HashBytes for field hashBytes"},
		},
		{
			name: "a field named for the tail's length",
			root: recordOf("Event", fieldOf("valueLen", primType("long")), fieldOf("value", primType("bytes"))),
			want: []string{"ValueLen collides", "accessor ValueLen for field valueLen", "accessor ValueLen for field value"},
		},
		{
			name: "a record named like a view",
			root: recordOf("Event", fieldOf("id", primType("long")), fieldOf("other", recordOf("EventView", fieldOf("id", primType("long"))))),
			want: []string{"EventView collides in the package", "view type generated for record com.example.Event", "record com.example.EventView"},
		},
		{
			name: "an enum symbol named like an offset constant",
			root: recordOf("Event", fieldOf("kind", primType("boolean")), fieldOf("e", enumOf("EventKind", "Offset"))),
			want: []string{"EventKindOffset collides in the package", "offset constant for field kind of record com.example.Event", "constant for symbol Offset"},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			schema := schemaOf(tc.root)
			if _, err := generateToDir(t, t.TempDir(), viewsRequest("", "", schema)); err != nil {
				t.Fatalf("without views, Generate failed: %v", err)
			}
			_, err := generateToDir(t, t.TempDir(), viewsRequest("true", "", schema))
			if err == nil {
				t.Fatal("views=true generated a package declaring an identifier twice")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the diagnostic does not contain %q:\n%v", want, err)
				}
			}
		})
	}
}

// TestViewParseDecidesTheOneByteCaseItself reads the generated source for the
// fast paths: a length and an index are decoded in place when they are one
// byte, and ConsumeLength and ConsumeIndex are called in the else branch only.
// ConsumeLong and ConsumeFixed inline, and a fixed-width field is a length
// check, so neither has one.
func TestViewParseDecidesTheOneByteCaseItself(t *testing.T) {
	schema := schemaOf(recordOf("Fast",
		fieldOf("h", fixedOf("H", 8)),
		fieldOf("l", primType("long")),
		fieldOf("b", primType("bytes")),
		fieldOf("e", enumOf("Kind", "A", "B")),
		fieldOf("tail", primType("string")),
	))
	code := generateViewsString(t, "", schema)
	body := between(t, code, "func (v *FastView) Parse(b []byte) error {", "\n}\n")

	for _, want := range []string{
		"if len(b) < 8 {\n\t\treturn v.fail(io.ErrUnexpectedEOF, 12)\n\t}\n\n\tpos := 8\n\n\t// l: long",
		"x, k := avro.ConsumeLong(b[pos:])",
		"if len(b) > pos && b[pos]&0x81 == 0 {\n\t\t\tl = int(b[pos] >> 1)\n\t\t} else {\n\t\t\tl, k = avro.ConsumeLength(b[pos:])",
		"if len(b) > pos && b[pos]&0x81 == 0 && int(b[pos]>>1) < 2 {\n\t\t\ti = int(b[pos] >> 1)\n\t\t} else {\n\t\t\ti, k = avro.ConsumeIndex(b[pos:], 2)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("FastView.Parse does not contain\n%s\n\nin:\n%s", want, body)
		}
	}
	if strings.Contains(body, "ConsumeBytes") {
		t.Errorf("FastView.Parse calls ConsumeBytes, whose one-byte case it should decide itself:\n%s", body)
	}
}

// TestOffsetConstantsAreTheFieldsBeforeTheFirstVariableSizeOne: a field gets
// a constant exactly when its offset is the same in every encoding and it
// comes before the first variable-size field, and a record of constant width
// counts as a field of constant width.
func TestOffsetConstantsAreTheFieldsBeforeTheFirstVariableSizeOne(t *testing.T) {
	schema := schemaOf(recordOf("Rec",
		fieldOf("flag", primType("boolean")),
		fieldOf("pair", recordOf("Pair", fieldOf("a", primType("float")), fieldOf("b", fixedOf("B", 3)))),
		fieldOf("score", primType("double")),
		fieldOf("count", primType("int")),
		fieldOf("after", primType("boolean")),
	))
	code := generateViewsString(t, "", schema)
	for _, want := range []string{"RecFlagOffset  = 0", "RecPairOffset  = 1", "RecScoreOffset = 8", "PairAOffset = 0", "PairBOffset = 4"} {
		if !strings.Contains(code, want) {
			t.Errorf("the generated code has no %q:\n%s", want, code)
		}
	}
	for _, never := range []string{"RecCountOffset", "RecAfterOffset"} {
		if strings.Contains(code, never) {
			t.Errorf("the generated code declares %s, whose offset varies", never)
		}
	}
}

// TestThePrefixEndsInsideTheLastVariableSizeField is PrefixLen's rule read
// from the plan: a last bytes or string field, or a last nested record with a
// tail of its own, recursively; anything else, and the prefix is the record.
func TestThePrefixEndsInsideTheLastVariableSizeField(t *testing.T) {
	testCases := []struct {
		name string
		r    *avrocpb.Type
		tail string
	}{
		{"a last bytes field", recordOf("R", fieldOf("id", primType("long")), fieldOf("v", primType("bytes"))), "v"},
		{"a last string field", recordOf("R", fieldOf("v", primType("string"))), "v"},
		{"a last nested record ending in bytes", recordOf("R", fieldOf("n", recordOf("N", fieldOf("id", primType("long")), fieldOf("v", primType("bytes"))))), "n.v"},
		{"a last nested record ending in a long", recordOf("R", fieldOf("v", primType("bytes")), fieldOf("n", recordOf("N", fieldOf("id", primType("long"))))), ""},
		{"a last long", recordOf("R", fieldOf("v", primType("bytes")), fieldOf("id", primType("long"))), ""},
		{"no fields", recordOf("R"), ""},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := namedTypes{}.viewPlanFor(tc.r.GetRecord())
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			if p.tail >= 0 {
				got = p.tailPath()
			}
			if got != tc.tail {
				t.Errorf("the prefix ends in %q, want %q", got, tc.tail)
			}
		})
	}
}
