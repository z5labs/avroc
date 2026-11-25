// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/z5labs/avroc/avrocpb"
	"google.golang.org/protobuf/proto"
)

// fixedInlineSchema is the record #245's allocation criterion names: a fixed,
// an array of that fixed and a bytes.
func fixedInlineSchema() *avrocpb.Schema {
	return &avrocpb.Schema{
		Namespace: proto.String(unionTestNamespace),
		Type: recordOf("Packet",
			fieldOf("hash", fixedOf("Hash", 4)),
			fieldOf("hashes", arrayOf(namedType(unionTestNamespace+".Hash"))),
			fieldOf("blob", primType("bytes")),
		),
	}
}

func namedType(fullName string) *avrocpb.Type {
	return &avrocpb.Type{Type: &avrocpb.Type_Reference{Reference: namedRef(fullName)}}
}

// TestFixedValuesAreWrittenWithoutAllocating builds the generated code for
// fixedInlineSchema and runs testdata/fixedinline/fixedinline_test.go against
// it, which asserts with testing.AllocsPerRun that encoding the record into a
// writer over io.Discard makes no allocations, and pins the bytes it writes.
func TestFixedValuesAreWrittenWithoutAllocating(t *testing.T) {
	testGeneratedPackage(t, "fixedinline", filepath.Join("testdata", "fixedinline", "fixedinline_test.go"),
		fixedInlineSchema())
}

// fixedPositionsSchemas puts a fixed in every position #245 names: a record
// field (defined there or referenced), an array item at any depth — including
// beneath a map value and inside a union's array branch — a map value, and a
// union branch, beside records and enums that must keep their methods. The
// second schema refers to the fixed the first defines, which is the case that
// needs the whole descriptor's definitions rather than one schema's.
func fixedPositionsSchemas() []*avrocpb.Schema {
	hash := func() *avrocpb.Type { return namedType(unionTestNamespace + ".Hash") }
	inner := func() *avrocpb.Type { return namedType(unionTestNamespace + ".Inner") }

	return []*avrocpb.Schema{
		{
			Namespace: proto.String(unionTestNamespace),
			Type: recordOf("Holder",
				fieldOf("hash", fixedOf("Hash", 4)),
				fieldOf("again", hash()),
				fieldOf("hashes", arrayOf(hash())),
				fieldOf("grid", arrayOf(arrayOf(hash()))),
				fieldOf("listsByKey", mapOf(arrayOf(hash()))),
				fieldOf("byKey", mapOf(hash())),
				fieldOf("maybe", unionOf(primType("null"), hash())),
				fieldOf("listed", unionOf(primType("null"), arrayOf(hash()))),
				fieldOf("tone", enumOf("Tone", "LOW", "HIGH")),
				fieldOf("inner", recordOf("Inner", fieldOf("i", primType("int")))),
				fieldOf("innerAgain", inner()),
				fieldOf("inners", arrayOf(inner())),
			),
		},
		{
			Namespace: proto.String(unionTestNamespace),
			Type:      recordOf("Later", fieldOf("hash", hash())),
		},
	}
}

// TestEveryFixedPositionCompilesAndRoundTrips builds the generated code for
// fixedPositionsSchemas and runs testdata/fixedpositions/holder_test.go
// against it. The source assertions below say which form each position is
// written in; this is what says each of those forms type-checks — slicing is
// legal only on an addressable array — and encodes what it decodes.
func TestEveryFixedPositionCompilesAndRoundTrips(t *testing.T) {
	testGeneratedPackage(t, "fixedpositions", filepath.Join("testdata", "fixedpositions", "holder_test.go"),
		fixedPositionsSchemas()...)
}

// TestFixedValuesAreWrittenInlineWhereTheyAreAddressable reads the generated
// source for fixedPositionsSchemas, for where a fixed is written inline and
// where it still goes through its method.
func TestFixedValuesAreWrittenInlineWhereTheyAreAddressable(t *testing.T) {
	res, err := generateToDir(t, t.TempDir(), &avrocpb.GenerateRequest{
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("fixedinline")}},
		Schemas: fixedPositionsSchemas(),
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if len(res.OutputFiles) != 2 {
		t.Fatalf("generated %v, want one file per schema", res.OutputFiles)
	}

	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		validateGoSyntax(t, string(b))
		return string(b)
	}
	holder := read(res.OutputFiles[0])
	later := read(res.OutputFiles[1])

	testCases := []struct {
		name    string
		code    string
		want    string
		notWant string
	}{
		{name: "a fixed field defined in place", code: holder,
			want: "err = w.WriteFixed(x.Hash[:])", notWant: "x.Hash.MarshalAvroBinary(w)"},
		{name: "a fixed field by reference", code: holder,
			want: "err = w.WriteFixed(x.Again[:])", notWant: "x.Again.MarshalAvroBinary(w)"},
		{name: "a fixed array item", code: holder,
			want: "err = w.WriteFixed(x.Hashes[i][:])", notWant: "x.Hashes[i].MarshalAvroBinary(w)"},
		{name: "a fixed item of a nested array", code: holder,
			want: "err = w.WriteFixed(x.Grid[i][i1][:])"},
		{name: "a fixed item of an array that is a map value", code: holder,
			want: "err = w.WriteFixed(x.ListsByKey[k][i1][:])"},
		{name: "a fixed item of a union's array branch", code: holder,
			want: "err = w.WriteFixed(x.Value[i][:])"},
		{name: "a fixed map value still goes through its method", code: holder,
			want: "tmp := x.ByKey[k]\n\t\t\t\terr = tmp.MarshalAvroBinary(w)"},
		{name: "an enum field still goes through its method", code: holder,
			want: "err = x.Tone.MarshalAvroBinary(w)"},
		{name: "a record field still goes through its method", code: holder,
			want: "err = x.Inner.MarshalAvroBinary(w)"},
		{name: "a referenced record field still goes through its method", code: holder,
			want: "err = x.InnerAgain.MarshalAvroBinary(w)", notWant: "x.InnerAgain[:]"},
		{name: "a referenced record array item still goes through its method", code: holder,
			want: "tmp := x.Inners[i]\n\t\t\t\terr = tmp.MarshalAvroBinary(w)", notWant: "x.Inners[i][:]"},
		{name: "the fixed type keeps its own method", code: holder,
			want: "func (x Hash) MarshalAvroBinary(w *avro.BinaryWriter) error {\n\treturn w.WriteFixed(x[:])\n}"},
		{name: "a fixed another schema defines", code: later,
			want: "err = w.WriteFixed(x.Hash[:])", notWant: "x.Hash.MarshalAvroBinary(w)"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.code, tc.want) {
				t.Errorf("generated code does not contain %q:\n%s", tc.want, tc.code)
			}
			if tc.notWant != "" && strings.Contains(tc.code, tc.notWant) {
				t.Errorf("generated code contains %q:\n%s", tc.notWant, tc.code)
			}
		})
	}

	// The union's fixed branch: its member's receiver is a copy, so it is
	// written through the fixed's method exactly as before.
	member := between(t, holder, "func (x HolderMaybeUnionHash) MarshalAvroBinary", "\n}\n")
	if !strings.Contains(member, "err = x.Value.MarshalAvroBinary(w)") {
		t.Errorf("a fixed union branch is not written through its method:\n%s", member)
	}
}

// between is the text of code from the first occurrence of start up to the
// next occurrence of end after it.
func between(t *testing.T, code, start, end string) string {
	t.Helper()
	i := strings.Index(code, start)
	if i < 0 {
		t.Fatalf("generated code does not contain %q:\n%s", start, code)
	}
	j := strings.Index(code[i:], end)
	if j < 0 {
		t.Fatalf("no %q after %q in:\n%s", end, start, code)
	}
	return code[i : i+j]
}
