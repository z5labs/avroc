// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgenjson

import (
	"bytes"
	"context"
	"testing"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/ir"

	"google.golang.org/protobuf/proto"
)

// generatedFile is one reassembled file from a generation, in the order the
// generator emitted it.
type generatedFile struct {
	path    string
	content []byte
}

// generateFiles runs one generation and reassembles the chunk stream into whole
// files, so that a difference in chunk boundaries — which is not a difference in
// output — cannot be mistaken for one.
func generateFiles(t *testing.T, req *avrocpb.GenerateRequest) []generatedFile {
	t.Helper()

	if req.Version == nil {
		req = proto.CloneOf(req)
		req.Version = proto.Int32(ir.Version)
	}

	cs := &captureStream{ctx: context.Background()}
	if err := (&generatorService{}).Generate(req, cs); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	var files []generatedFile
	index := make(map[string]int)
	for _, msg := range cs.msgs {
		path := msg.GetPath()
		i, ok := index[path]
		if !ok {
			i = len(files)
			index[path] = i
			files = append(files, generatedFile{path: path})
		}
		files[i].content = append(files[i].content, msg.GetContent()...)
	}
	return files
}

// TestGenerateIsDeterministic is #120's dynamic half for this generator: the
// same descriptor produces the same set of paths with byte-identical contents,
// however many times it is run.
//
// The repetition is what exercises map iteration order: Go randomises the
// starting point of every range over a map, so a generator ordering its output
// by one produces a different result within a handful of runs rather than
// reliably. It cannot catch a clock read, since two runs a millisecond apart
// agree on the date; internal/plugin.TestNoGeneratorReadsTheClock covers that
// half statically.
//
// A JSON object is where this generator would break the rule if it were going
// to: an Avro schema is a mapping, and emitting one from a Go map rather than
// from the descriptor's own field order is the mistake the IR is shaped to make
// unnecessary.
func TestGenerateIsDeterministic(t *testing.T) {
	const runs = 16

	req := &avrocpb.GenerateRequest{
		Schemas: []*avrocpb.Schema{resolvedTestRecord(), determinismSecondSchema()},
	}

	first := generateFiles(t, req)
	if len(first) == 0 {
		t.Fatal("the generation produced no files: the case under test is empty")
	}

	for run := 1; run < runs; run++ {
		again := generateFiles(t, req)
		if len(again) != len(first) {
			t.Fatalf("run %d produced %d file(s), run 0 produced %d", run, len(again), len(first))
		}
		for i := range first {
			if again[i].path != first[i].path {
				t.Fatalf("run %d produced %q where run 0 produced %q", run, again[i].path, first[i].path)
			}
			if !bytes.Equal(again[i].content, first[i].content) {
				t.Fatalf("run %d produced different bytes for %q:\n--- run 0 ---\n%s\n--- run %d ---\n%s",
					run, first[i].path, first[i].content, run, again[i].content)
			}
		}
	}
}

// determinismSecondSchema is a second input, so that a run has more than one
// file to order as well as more than one field.
func determinismSecondSchema() *avrocpb.Schema {
	const ns = "com.example"

	return &avrocpb.Schema{
		Namespace: proto.String(ns),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name:      proto.String("Simple"),
					Namespace: proto.String(ns),
					FullName:  proto.String(ns + ".Simple"),
					Fields: []*avrocpb.Field{
						{Name: proto.String("id"), Type: primType("long")},
						{
							Name: proto.String("settings"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_MapType{
									MapType: &avrocpb.Map{Values: primType("string")},
								},
							},
						},
					},
				},
			},
		},
	}
}
