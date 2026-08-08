// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/ir"

	"google.golang.org/protobuf/proto"
)

// testSchema is a small resolved schema: enough structure for the encoding to
// have an order to get wrong, and no more.
func testSchema(record string) *avrocpb.Schema {
	return &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name:     proto.String(record),
					FullName: proto.String("com.example." + record),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("name"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Reference{
									Reference: &avrocpb.Reference{
										Name: proto.String("string"),
										Kind: avrocpb.TypeRefKind_TYPE_REF_KIND_PRIMITIVE.Enum(),
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func TestNewDescriptor(t *testing.T) {
	t.Run("stamps the IR version this build writes", func(t *testing.T) {
		desc := newDescriptor(nil, nil)
		if err := ir.CheckVersion(desc.GetVersion()); err != nil {
			t.Errorf("descriptor would be refused by this build's own generators: %v", err)
		}
	})

	t.Run("carries the invocation's options and schemas", func(t *testing.T) {
		options := GeneratorConfig{Options: map[string]string{"package": "models"}}.options()
		schemas := []*avrocpb.Schema{testSchema("User"), testSchema("Order")}

		desc := newDescriptor(options, schemas)

		if len(desc.GetOptions()) != 1 || desc.GetOptions()[0].GetName() != "package" {
			t.Errorf("options = %v", desc.GetOptions())
		}
		if len(desc.GetSchemas()) != 2 {
			t.Fatalf("schemas = %d, want 2", len(desc.GetSchemas()))
		}
		// Schemas keep the order they were handed in; nothing re-sorts them.
		if got := desc.GetSchemas()[0].GetType().GetRecord().GetName(); got != "User" {
			t.Errorf("first schema = %q, want User", got)
		}
	})
}

func TestWriteDescriptor(t *testing.T) {
	t.Run("writes the descriptor the generator was handed", func(t *testing.T) {
		dir := t.TempDir()
		desc := newDescriptor(
			GeneratorConfig{Options: map[string]string{"package": "models"}}.options(),
			[]*avrocpb.Schema{testSchema("User")},
		)

		path, err := writeDescriptor(dir, desc)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(dir, descriptorFilename); path != want {
			t.Errorf("path = %q, want %q", path, want)
		}

		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		// The bytes on disk must decode back to the value avroc sent, not to
		// something merely similar: this is the file a plugin will be handed.
		var got avrocpb.GenerateRequest
		if err := proto.Unmarshal(b, &got); err != nil {
			t.Fatalf("descriptor file does not decode: %v", err)
		}
		if !proto.Equal(&got, desc) {
			t.Errorf("decoded descriptor differs from the one written\n got: %v\nwant: %v", &got, desc)
		}
	})

	t.Run("creates the file read-only", func(t *testing.T) {
		dir := t.TempDir()

		path, err := writeDescriptor(dir, newDescriptor(nil, nil))
		if err != nil {
			t.Fatal(err)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		// docs/plugin/SPEC.md: a plugin MUST NOT write to the descriptor. The
		// mode does not enforce that, but it makes the accidental case fail
		// where the mistake is.
		if mode := info.Mode().Perm(); mode&0o222 != 0 {
			t.Errorf("mode = %v, want no write bits", mode)
		}
	})

	t.Run("refuses to overwrite an existing descriptor", func(t *testing.T) {
		dir := t.TempDir()

		if _, err := writeDescriptor(dir, newDescriptor(nil, nil)); err != nil {
			t.Fatal(err)
		}

		// One file per invocation is what makes a descriptor attributable, so a
		// directory that already holds one is a bug rather than a thing to
		// clobber.
		if _, err := writeDescriptor(dir, newDescriptor(nil, nil)); err == nil {
			t.Error("expected an error writing a second descriptor into the same directory, got nil")
		}
	})

	t.Run("errors when the directory does not exist", func(t *testing.T) {
		path, err := writeDescriptor(filepath.Join(t.TempDir(), "absent"), newDescriptor(nil, nil))
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
		if path != "" {
			t.Errorf("path = %q, want empty on error", path)
		}
	})
}

// TestWriteDescriptor_Deterministic is the acceptance criterion stated as a
// test: identical inputs, identical bytes. The options are built from a Go map
// on every iteration, so a producer that let map iteration order reach the
// encoding fails here — intermittently in principle, but with enough keys and
// enough iterations to make that a near certainty rather than a coin toss.
func TestWriteDescriptor_Deterministic(t *testing.T) {
	options := map[string]string{
		"package": "models",
		"module":  "github.com/z5labs/avroc",
		"prefix":  "Avro",
		"suffix":  "Message",
		"tags":    "json",
		"pointer": "true",
		"stream":  "false",
		"marshal": "true",
	}
	schemas := []*avrocpb.Schema{testSchema("User"), testSchema("Order"), testSchema("Item")}

	var want []byte
	for i := range 32 {
		dir := t.TempDir()
		desc := newDescriptor(GeneratorConfig{Options: options}.options(), schemas)

		path, err := writeDescriptor(dir, desc)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		if i == 0 {
			want = got
			continue
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("descriptor bytes differ on iteration %d: %d bytes vs %d", i, len(got), len(want))
		}
	}
}

// TestPlanGenerators_DescriptorsAreReproducible is the same criterion one level
// up, over the path a real run takes: parse the project's IDL, resolve it, plan
// the generators, encode each task's descriptor. Regenerating an unchanged
// project twice must produce the same bytes, so anything nondeterministic in
// parsing or resolution — a map walked in iteration order, a pointer formatted
// into a name — fails here rather than as an unexplained diff in somebody's
// generated code.
func TestPlanGenerators_DescriptorsAreReproducible(t *testing.T) {
	dir := t.TempDir()
	shared := writeIDL(t, dir, "shared.avdl")

	m := &Manifest{
		Inputs: []string{shared},
		Generators: []GeneratorConfig{
			{Name: "go", Out: "gen/go", Options: map[string]string{"package": "models", "module": "example.com/m"}},
			{Name: "json", Out: "gen/json"},
		},
	}
	generators := map[string]string{
		"avroc-gen-go":   "/fake/avroc-gen-go",
		"avroc-gen-json": "/fake/avroc-gen-json",
	}

	encode := func() [][]byte {
		t.Helper()
		tasks, err := planGenerators(m, generators, dir)
		if err != nil {
			t.Fatal(err)
		}
		out := make([][]byte, len(tasks))
		for i, task := range tasks {
			b, err := ir.MarshalDescriptor(newDescriptor(task.options, task.schemas))
			if err != nil {
				t.Fatal(err)
			}
			out[i] = b
		}
		return out
	}

	first := encode()
	if len(first) != 2 {
		t.Fatalf("descriptors = %d, want 2", len(first))
	}
	for i, b := range encode() {
		if !bytes.Equal(b, first[i]) {
			t.Errorf("descriptor %d differs between two runs over an unchanged project", i)
		}
	}
}
