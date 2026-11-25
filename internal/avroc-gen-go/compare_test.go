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

func orderedField(name string, t *avrocpb.Type, order avrocpb.SortOrder) *avrocpb.Field {
	return &avrocpb.Field{Name: proto.String(name), Type: t, SortOrder: order.Enum()}
}

func descending(name string, t *avrocpb.Type) *avrocpb.Field {
	return orderedField(name, t, avrocpb.SortOrder_SORT_ORDER_DESC)
}

func ignored(name string, t *avrocpb.Type) *avrocpb.Field {
	return orderedField(name, t, avrocpb.SortOrder_SORT_ORDER_IGNORE)
}

// compareSampleSchema puts every type Compare reads in a record three times —
// ascending, descending and ignored — with a nested record in each of those
// three positions too, and ends in a trailing run of ignored fields of the
// three types it cannot read. testdata/compare/sample_test.go is the reference
// comparison it is checked against.
//
// Big has more than 63 symbols, so that an ordinal past the one-byte encoding
// reaches ConsumeIndex.
func compareSampleSchema() *avrocpb.Schema {
	ns := unionTestNamespace
	big := make([]string, 70)
	for i := range big {
		big[i] = fmt.Sprintf("S%d", i)
	}

	// No null field: a record field of type null does not generate a struct
	// that compiles today, whatever the options. TestANullFieldIsAlwaysEqual
	// is null's case instead.
	var fields []*avrocpb.Field
	triple := func(name string, def, ref *avrocpb.Type) {
		fields = append(fields, fieldOf(name, def), descending(name+"Down", ref), ignored(name+"Ignored", ref))
	}
	for _, p := range []string{"boolean", "int", "long", "float", "double", "bytes", "string"} {
		triple(p+"Value", primType(p), primType(p))
	}
	// Digest and Wide are compared as big-endian words — 4, then 8+8+2+1 —
	// and Huge, past maxWordwiseFixed, with bytes.Compare.
	triple("digest", fixedOf("Digest", 4), namedType(ns+".Digest"))
	triple("wide", fixedOf("Wide", 19), namedType(ns+".Wide"))
	triple("huge", fixedOf("Huge", 40), namedType(ns+".Huge"))
	triple("color", enumOf("Color", "RED", "GREEN", "BLUE"), namedType(ns+".Color"))
	triple("big", enumOf("Big", big...), namedType(ns+".Big"))
	triple("inner", recordOf("Inner",
		fieldOf("id", primType("long")),
		descending("label", primType("string")),
		ignored("blob", primType("bytes")),
		fieldOf("n", primType("int")),
	), namedType(ns+".Inner"))
	fields = append(fields,
		ignored("tags", mapOf(primType("string"))),
		ignored("list", arrayOf(primType("long"))),
		ignored("choice", unionOf(primType("null"), primType("string"))),
	)

	return &avrocpb.Schema{Namespace: proto.String(ns), Type: recordOf("Sample", fields...)}
}

// TestCompareAgreesWithTheReferenceOnEveryPair builds compareSampleSchema with
// compare=true and runs testdata/compare against it: the reference comparison
// over decoded values, forced ties, truncation at every position, the error
// codes, the allocation count and the fuzz test's seed corpus.
func TestCompareAgreesWithTheReferenceOnEveryPair(t *testing.T) {
	testGeneratedPackageWith(t, "compare", filepath.Join("testdata", "compare", "sample_test.go"),
		[]string{"compare", "true"}, compareSampleSchema())
}

func compareRequest(value string, schemas ...*avrocpb.Schema) *avrocpb.GenerateRequest {
	pairs := []string{"package_name", "gen"}
	if value != "" {
		pairs = append(pairs, "compare", value)
	}
	return &avrocpb.GenerateRequest{Options: options(pairs...), Schemas: schemas}
}

// TestTheCompareOptionIsExactlyTrueOrFalse is the option's spelling: true
// generates a Compare for every record, false and an absent option generate
// none, and anything else fails the run naming the value it was given.
func TestTheCompareOptionIsExactlyTrueOrFalse(t *testing.T) {
	schema := &avrocpb.Schema{
		Namespace: proto.String(unionTestNamespace),
		Type: recordOf("Outer",
			fieldOf("id", primType("long")),
			fieldOf("inner", recordOf("Inner", fieldOf("name", primType("string")))),
		),
	}

	generated := func(t *testing.T, req *avrocpb.GenerateRequest) string {
		t.Helper()
		res, err := generateToDir(t, t.TempDir(), req)
		if err != nil {
			t.Fatalf("Generate failed: %v", err)
		}
		b, err := os.ReadFile(res.OutputFiles[0])
		if err != nil {
			t.Fatal(err)
		}
		validateGoSyntax(t, string(b))
		return string(b)
	}

	t.Run("true", func(t *testing.T) {
		code := generated(t, compareRequest("true", schema))
		for _, fn := range []string{"func CompareOuterAvro(a, b []byte) (c, na, nb int) {", "func CompareInnerAvro(a, b []byte) (c, na, nb int) {"} {
			if !strings.Contains(code, fn) {
				t.Errorf("compare=true did not generate %q", fn)
			}
		}
	})
	for _, value := range []string{"false", ""} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			if code := generated(t, compareRequest(value, schema)); strings.Contains(code, "Compare") {
				t.Errorf("compare=%q generated a Compare function", value)
			}
		})
	}

	for _, value := range []string{"True", "1", "yes", "", " true"} {
		t.Run("refuses "+fmt.Sprintf("%q", value), func(t *testing.T) {
			req := &avrocpb.GenerateRequest{Options: options("package_name", "gen", "compare", value), Schemas: []*avrocpb.Schema{schema}}
			_, err := generateToDir(t, t.TempDir(), req)
			if err == nil {
				t.Fatalf("compare=%q was accepted", value)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("%q", value)) || !strings.Contains(err.Error(), "compare") {
				t.Errorf("the diagnostic %q does not name the option and its value %q", err, value)
			}
		})
	}
}

// TestCompareRefusesARecordOutsideItsScope is the scope: a field Compare would
// have to read must be a primitive, an enum, a fixed or a record of those, and
// anything else is allowed only in the trailing run of ignored fields. A
// record outside it fails the run, naming the record, the field and the
// field's type, before a single file is written.
func TestCompareRefusesARecordOutsideItsScope(t *testing.T) {
	ns := unionTestNamespace
	testCases := []struct {
		name string
		root *avrocpb.Type
		// want is every substring the diagnostic must hold; nil means the
		// record is in scope.
		want []string
	}{
		{
			name: "a compared map",
			root: recordOf("Holder", fieldOf("id", primType("long")), fieldOf("tags", mapOf(primType("string")))),
			want: []string{"CompareHolderAvro", "record com.example.Holder", "field tags", "type map"},
		},
		{
			name: "a compared union",
			root: recordOf("Holder", fieldOf("choice", unionOf(primType("null"), primType("long")))),
			want: []string{"record com.example.Holder", "field choice", "type union"},
		},
		{
			name: "an ignored array before a compared field",
			root: recordOf("Holder", ignored("list", arrayOf(primType("long"))), fieldOf("id", primType("long"))),
			want: []string{"record com.example.Holder", "field list", "type array"},
		},
		{
			name: "a nested record's ignored map before a compared field",
			root: recordOf("Holder",
				fieldOf("inner", recordOf("Inner", fieldOf("id", primType("long")), ignored("tags", mapOf(primType("long"))))),
				fieldOf("after", primType("long")),
			),
			want: []string{"CompareHolderAvro", "record com.example.Holder", "field tags of record com.example.Inner", "inner.tags", "type map"},
		},
		{
			name: "a record containing itself",
			root: recordOf("Node", fieldOf("id", primType("long")), fieldOf("next", namedType(ns+".Node"))),
			want: []string{"record com.example.Node", "field next", "contains itself"},
		},
		{
			name: "a trailing run of ignored fields of any type",
			root: recordOf("Holder",
				fieldOf("id", primType("long")),
				ignored("tags", mapOf(primType("string"))),
				ignored("list", arrayOf(primType("long"))),
				ignored("choice", unionOf(primType("null"), primType("long"))),
			),
		},
		{
			name: "a nested record's trailing ignored map, when nothing after it is compared",
			root: recordOf("Holder",
				fieldOf("inner", recordOf("Inner", fieldOf("id", primType("long")), ignored("tags", mapOf(primType("long"))))),
				ignored("after", arrayOf(primType("long"))),
			),
		},
		{
			name: "an ignored nested record of any type, last",
			root: recordOf("Holder",
				fieldOf("id", primType("long")),
				ignored("inner", recordOf("Inner", fieldOf("tags", mapOf(primType("long"))))),
			),
			// Inner gets a Compare of its own, and a map is the first thing
			// it would read.
			want: []string{"CompareInnerAvro", "field tags of record com.example.Inner", "type map"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// A first schema that is in scope, so that a check made inside the
			// generation loop rather than before it would have written a file
			// — and the writer refuses every file, so that write would be the
			// error returned instead.
			first := &avrocpb.Schema{Namespace: proto.String(ns), Type: recordOf("First", fieldOf("id", primType("long")))}
			schema := &avrocpb.Schema{Namespace: proto.String(ns), Type: tc.root}
			req := compareRequest("true", first, schema)
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

// TestCompareIsRegisteredWithTheCollisionCheck is #247 for the new function:
// a record named CompareFooAvro beside a record Foo is a package that does not
// compile under compare=true, and an ordinary one without it.
func TestCompareIsRegisteredWithTheCollisionCheck(t *testing.T) {
	schema := &avrocpb.Schema{
		Namespace: proto.String(unionTestNamespace),
		Type: recordOf("Foo",
			fieldOf("id", primType("long")),
			fieldOf("other", recordOf("CompareFooAvro", fieldOf("id", primType("long")))),
		),
	}

	if _, err := generateToDir(t, t.TempDir(), compareRequest("", schema)); err != nil {
		t.Fatalf("without compare, Generate failed: %v", err)
	}

	_, err := generateToDir(t, t.TempDir(), compareRequest("true", schema))
	if err == nil {
		t.Fatal("compare=true generated a package declaring CompareFooAvro twice")
	}
	for _, want := range []string{"CompareFooAvro collides", "Compare function generated for record com.example.Foo", "record com.example.CompareFooAvro"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the diagnostic does not contain %q:\n%v", want, err)
		}
	}
}

// TestCompareDecidesTheOneByteCaseItself reads the generated source for the
// fast paths: each varint, length and index is decoded in place when it is one
// byte, and avro-go is called in the else branch only. A Compare built from
// avro-go's functions alone took 17.3 ns on a hash tie in #244, against 7.1 ns
// for this shape.
func TestCompareDecidesTheOneByteCaseItself(t *testing.T) {
	ns := unionTestNamespace
	schema := &avrocpb.Schema{
		Namespace: proto.String(ns),
		Type: recordOf("Fast",
			fieldOf("l", primType("long")),
			fieldOf("i", primType("int")),
			fieldOf("b", primType("bytes")),
			fieldOf("e", enumOf("Kind", "A", "B")),
			ignored("skipL", primType("long")),
			ignored("skipB", primType("string")),
			ignored("skipE", namedType(ns+".Kind")),
			fieldOf("last", primType("boolean")),
		),
	}
	code := generateCompareString(t, schema)
	body := between(t, code, "func CompareFastAvro(a, b []byte) (c, na, nb int) {", "\n}\n")

	for _, want := range []string{
		// long: both one byte, else CompareLong.
		"if len(a) > na && len(b) > nb && a[na] < 0x80 && b[nb] < 0x80 {\n\t\tka, kb = 1, 1\n\t\tc = cmp.Compare(int8(a[na]>>1)^-int8(a[na]&1), int8(b[nb]>>1)^-int8(b[nb]&1))\n\t} else {\n\t\tc, ka, kb = avro.CompareLong(a[na:], b[nb:])",
		// int: the same fast path, and ConsumeInt for the overflow check.
		"} else {\n\t\tvar va, vb int32\n\t\tva, ka = avro.ConsumeInt(a[na:])",
		// bytes: both one-byte lengths, else CompareBytes.
		"if len(a) > na && len(b) > nb && a[na]&0x81 == 0 && b[nb]&0x81 == 0 {\n\t\tka, kb = 1+int(a[na]>>1), 1+int(b[nb]>>1)",
		"} else {\n\t\tc, ka, kb = avro.CompareBytes(a[na:], b[nb:])",
		// enum: a one-byte index in range, else ConsumeIndex.
		"if len(a) > na && a[na]&0x81 == 0 && int(a[na]>>1) < 2 {\n\t\t\toa, ka = int(a[na]>>1), 1\n\t\t} else {\n\t\t\toa, ka = avro.ConsumeIndex(a[na:], 2)",
		// The skips have the same fast paths, per side.
		"if len(a) > na && a[na] < 0x80 {\n\t\tka = 1\n\t} else {\n\t\t_, ka = avro.ConsumeLong(a[na:])",
		"if len(b) > nb && b[nb]&0x81 == 0 {\n\t\tkb = 1 + int(b[nb]>>1)",
		"if len(a) > na && a[na]&0x81 == 0 && int(a[na]>>1) < 2 {\n\t\tka = 1\n\t} else {\n\t\t_, ka = avro.ConsumeIndex(a[na:], 2)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("CompareFastAvro does not contain\n%s\n\nin:\n%s", want, body)
		}
	}
}

// TestCompareNeverReadsATrailingIgnoredField reads the generated source for
// where reading stops: nothing of the trailing run is emitted, and the
// comment says how much of a record a and b must hold.
func TestCompareNeverReadsATrailingIgnoredField(t *testing.T) {
	schema := &avrocpb.Schema{
		Namespace: proto.String(unionTestNamespace),
		Type: recordOf("Rec",
			fieldOf("id", primType("long")),
			ignored("tail", primType("bytes")),
			ignored("tags", mapOf(primType("string"))),
		),
	}
	code := generateCompareString(t, schema)

	if !strings.Contains(code, "// Nothing after field id is compared, so a and b need hold the\n// encoding only through it.") {
		t.Errorf("the comment does not say reading stops after id:\n%s", code)
	}
	body := between(t, code, "func CompareRecAvro(a, b []byte) (c, na, nb int) {", "\n}\n")
	for _, never := range []string{"tail", "tags", "ConsumeBytes", "bytes.Compare"} {
		if strings.Contains(body, never) {
			t.Errorf("CompareRecAvro mentions %q, which it never reads:\n%s", never, body)
		}
	}
}

// TestANullFieldIsAlwaysEqual is null's row of the ordering table: it is
// always equal and occupies no bytes, so it contributes nothing to the plan,
// compared, descending or ignored — as does a zero-size fixed.
func TestANullFieldIsAlwaysEqual(t *testing.T) {
	r := recordOf("Rec",
		fieldOf("a", primType("null")),
		descending("b", primType("null")),
		fieldOf("empty", fixedOf("Empty", 0)),
		fieldOf("id", primType("long")),
		ignored("c", primType("null")),
	).GetRecord()

	plan, err := namedTypes{}.comparePlanFor(r)
	if err != nil {
		t.Fatalf("comparePlanFor: %v", err)
	}
	if len(plan.ops) != 1 || plan.ops[0].path != "id" {
		t.Errorf("plan is %+v, want the long id alone", plan.ops)
	}
}

// TestAFixedIsComparedAsBigEndianWords is the shape of a fixed's comparison:
// the widest big-endian word that fits, each compared only while the ones
// before it were equal, and bytes.Compare past maxWordwiseFixed.
func TestAFixedIsComparedAsBigEndianWords(t *testing.T) {
	testCases := map[int]string{
		1: "\tc = cmp.Compare(a[na], b[nb])\n",
		3: "\tc = cmp.Compare(binary.BigEndian.Uint16(a[na:]), binary.BigEndian.Uint16(b[nb:]))\n" +
			"\tif c == 0 {\n\t\tc = cmp.Compare(a[na+2], b[nb+2])\n\t}\n",
		8: "\tc = cmp.Compare(binary.BigEndian.Uint64(a[na:]), binary.BigEndian.Uint64(b[nb:]))\n",
		12: "\tc = cmp.Compare(binary.BigEndian.Uint64(a[na:]), binary.BigEndian.Uint64(b[nb:]))\n" +
			"\tif c == 0 {\n\t\tc = cmp.Compare(binary.BigEndian.Uint32(a[na+8:]), binary.BigEndian.Uint32(b[nb+8:]))\n\t}\n",
		maxWordwiseFixed + 1: fmt.Sprintf("\tc = bytes.Compare(a[na:na+%d], b[nb:nb+%d])\n", maxWordwiseFixed+1, maxWordwiseFixed+1),
	}
	for size, want := range testCases {
		if got := fixedCompare(size); got != want {
			t.Errorf("fixedCompare(%d) =\n%s\nwant\n%s", size, got, want)
		}
	}
}

// generateCompareString is generateString under compare=true.
func generateCompareString(t *testing.T, schemas ...*avrocpb.Schema) string {
	t.Helper()
	res, err := generateToDir(t, t.TempDir(), compareRequest("true", schemas...))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	b, err := os.ReadFile(res.OutputFiles[0])
	if err != nil {
		t.Fatalf("read %s: %v", res.OutputFiles[0], err)
	}
	validateGoSyntax(t, string(b))
	return string(b)
}
