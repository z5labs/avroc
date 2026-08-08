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
	"github.com/z5labs/avroc/internal/ir"
	"github.com/z5labs/avroc/internal/plugin"

	"google.golang.org/protobuf/proto"
)

// eventRecord is a record this generator handles without complaint, so a
// failure below is the root type and nothing else.
func eventRecord() *avrocpb.Type {
	return &avrocpb.Type{
		Type: &avrocpb.Type_Record{
			Record: &avrocpb.Record{
				Name:      proto.String("Event"),
				Namespace: proto.String("com.example"),
				FullName:  proto.String("com.example.Event"),
				Fields: []*avrocpb.Field{
					{
						Name: proto.String("id"),
						Type: &avrocpb.Type{Type: &avrocpb.Type_Reference{Reference: primRef("string")}},
					},
				},
			},
		},
	}
}

// singleObjectRequest builds a request asking for single-object encoding over
// the schemas it is given.
func singleObjectRequest(schemas ...*avrocpb.Schema) *avrocpb.GenerateRequest {
	return &avrocpb.GenerateRequest{
		Version: proto.Int32(ir.Version),
		Options: []*avrocpb.Option{
			{Name: proto.String("package_name"), Value: proto.String("gen")},
			{Name: proto.String("encoding"), Value: proto.String("single_object")},
		},
		Schemas: schemas,
	}
}

// TestSingleObjectEncodingRefusesANonRecordRoot is #173: the fingerprint
// single-object encoding needs is published as a method on the Go type
// generated for the schema's root record, so a root that is not a record used
// to produce no Fingerprint at all — and the run exited zero, handing back
// ordinary binary encoding with nothing on stderr saying so. Silence is the
// bug; a diagnostic naming the root's type constructor is the fix.
func TestSingleObjectEncodingRefusesANonRecordRoot(t *testing.T) {
	testCases := []struct {
		name        string
		root        *avrocpb.Type
		constructor string
	}{
		{
			name:        "array root",
			root:        &avrocpb.Type{Type: &avrocpb.Type_Array{Array: &avrocpb.Array{Items: eventRecord()}}},
			constructor: "array",
		},
		{
			name:        "map root",
			root:        &avrocpb.Type{Type: &avrocpb.Type_MapType{MapType: &avrocpb.Map{Values: eventRecord()}}},
			constructor: "map",
		},
		{
			name: "union root",
			root: &avrocpb.Type{Type: &avrocpb.Type_Union{Union: &avrocpb.Union{
				Types: []*avrocpb.Type{
					{Type: &avrocpb.Type_Reference{Reference: primRef("null")}},
					eventRecord(),
				},
			}}},
			constructor: "union",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			w := plugin.NewOutputDir(dir)

			err := Generate(singleObjectRequest(&avrocpb.Schema{
				Namespace: proto.String("com.example"),
				Type:      tc.root,
			}), w)

			if err == nil {
				t.Fatal("Generate exited zero for a schema it cannot give single-object encoding to")
			}
			if !strings.Contains(err.Error(), tc.constructor) {
				t.Errorf("diagnostic %q does not name the root's type constructor %q", err.Error(), tc.constructor)
			}
			if !strings.Contains(err.Error(), "single_object") {
				t.Errorf("diagnostic %q does not name the option that was refused", err.Error())
			}
			assertNothingWritten(t, dir, w)
		})
	}
}

// TestSingleObjectEncodingRefusesBeforeWritingAnyFile is the reason the check
// runs over the whole descriptor before the generation loop rather than inside
// it: the loop writes as it goes, so a refusal from within it would leave the
// schemas before the offending one behind as half a package.
func TestSingleObjectEncodingRefusesBeforeWritingAnyFile(t *testing.T) {
	dir := t.TempDir()
	w := plugin.NewOutputDir(dir)

	err := Generate(singleObjectRequest(
		&avrocpb.Schema{Namespace: proto.String("com.example"), Type: eventRecord()},
		&avrocpb.Schema{
			Namespace: proto.String("com.example"),
			Type: &avrocpb.Type{Type: &avrocpb.Type_Array{
				Array: &avrocpb.Array{Items: eventRecord()},
			}},
		},
	), w)

	if err == nil {
		t.Fatal("Generate accepted a descriptor carrying a schema it cannot give single-object encoding to")
	}
	assertNothingWritten(t, dir, w)
}

// TestSingleObjectEncodingAcceptsARecordRoot is the guard on the two tests
// above: a check that refused every schema would pass both.
func TestSingleObjectEncodingAcceptsARecordRoot(t *testing.T) {
	dir := t.TempDir()
	w := plugin.NewOutputDir(dir)

	if err := Generate(singleObjectRequest(&avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type:      eventRecord(),
	}), w); err != nil {
		t.Fatalf("Generate refused a record root: %v", err)
	}

	if len(w.Written()) != 1 {
		t.Fatalf("generator wrote %d file(s), want 1", len(w.Written()))
	}

	code := readWritten(t, dir, w.Written()[0])
	validateGoSyntax(t, code)
	if !strings.Contains(code, "func (x *Event) Fingerprint() [8]byte {") {
		t.Errorf("generated code carries no Fingerprint method for the root record, got:\n%s", code)
	}
}

// TestANonRecordRootGeneratesWithoutSingleObjectEncoding scopes the refusal to
// the option that asked for the fingerprint. A schema rooted at an array is
// perfectly generatable as ordinary binary encoding, and nothing about #173
// makes it otherwise.
func TestANonRecordRootGeneratesWithoutSingleObjectEncoding(t *testing.T) {
	dir := t.TempDir()
	w := plugin.NewOutputDir(dir)

	err := Generate(&avrocpb.GenerateRequest{
		Version: proto.Int32(ir.Version),
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("gen")}},
		Schemas: []*avrocpb.Schema{{
			Namespace: proto.String("com.example"),
			Type: &avrocpb.Type{Type: &avrocpb.Type_Array{
				Array: &avrocpb.Array{Items: eventRecord()},
			}},
		}},
	}, w)
	if err != nil {
		t.Fatalf("Generate refused an array root that asked for no encoding: %v", err)
	}

	if len(w.Written()) != 1 {
		t.Fatalf("generator wrote %d file(s), want 1", len(w.Written()))
	}

	code := readWritten(t, dir, w.Written()[0])
	validateGoSyntax(t, code)
	if !strings.Contains(code, "type Event struct") {
		t.Errorf("generated code carries no type for the array's items, got:\n%s", code)
	}
	if strings.Contains(code, "Fingerprint()") {
		t.Errorf("generated code carries a Fingerprint method nobody asked for, got:\n%s", code)
	}
}

// TestTypeConstructorNamesEveryConstructor pins the vocabulary the diagnostic
// is written in: Avro's names for the six constructors, and for a reference the
// name of what it refers to, because "reference" is a fact about how the IR
// encodes a type rather than about the schema anybody wrote.
func TestTypeConstructorNamesEveryConstructor(t *testing.T) {
	testCases := []struct {
		typ  *avrocpb.Type
		want string
	}{
		{typ: eventRecord(), want: "record"},
		{
			typ:  &avrocpb.Type{Type: &avrocpb.Type_EnumType{EnumType: &avrocpb.Enum{Name: proto.String("Colour")}}},
			want: "enum",
		},
		{
			typ:  &avrocpb.Type{Type: &avrocpb.Type_Fixed{Fixed: &avrocpb.Fixed{Name: proto.String("MD5")}}},
			want: "fixed",
		},
		{
			typ:  &avrocpb.Type{Type: &avrocpb.Type_Array{Array: &avrocpb.Array{Items: eventRecord()}}},
			want: "array",
		},
		{
			typ:  &avrocpb.Type{Type: &avrocpb.Type_MapType{MapType: &avrocpb.Map{Values: eventRecord()}}},
			want: "map",
		},
		{
			typ:  &avrocpb.Type{Type: &avrocpb.Type_Union{Union: &avrocpb.Union{}}},
			want: "union",
		},
		{
			typ:  &avrocpb.Type{Type: &avrocpb.Type_Reference{Reference: primRef("string")}},
			want: "string",
		},
		{
			typ:  &avrocpb.Type{Type: &avrocpb.Type_Reference{Reference: namedRef("com.example.Event")}},
			want: "com.example.Event",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.want, func(t *testing.T) {
			if got := typeConstructor(tc.typ); got != tc.want {
				t.Errorf("typeConstructor() = %q, want %q", got, tc.want)
			}
		})
	}
}

// readWritten reads one of the files an invocation wrote, named the way
// OutputDir records it: a slash-separated path relative to the output
// directory.
func readWritten(t *testing.T, dir string, written string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(written)))
	if err != nil {
		t.Fatalf("failed to read the generated file: %v", err)
	}
	return string(content)
}

// assertNothingWritten requires a refused invocation to have left the output
// directory as it found it — empty. Both halves are checked because they can
// disagree: the writer's record is what avroc's merge is planned from, and the
// directory is what it actually merges.
func assertNothingWritten(t *testing.T, dir string, w *plugin.OutputDir) {
	t.Helper()

	if n := len(w.Written()); n != 0 {
		t.Errorf("generator recorded %d file(s) from an invocation it refused", n)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("failed to read the output directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("output directory holds %d entrie(s) from an invocation the generator refused", len(entries))
	}
}
