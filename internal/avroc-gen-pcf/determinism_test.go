// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgenpcf

import (
	"bytes"
	"context"
	"testing"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/ir"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/protobuf/proto"
)

// generatedFile is one file from a generation, in the order the generator wrote
// it.
type generatedFile struct {
	path    string
	content []byte
}

// recordingWriter keeps what a generation wrote without touching the
// filesystem. Determinism is a property of the bytes and their order, and
// comparing sixteen runs in memory says nothing less about it than sixteen
// directories would — what the real writer adds to an assertion is covered by
// TestGeneratedFileIsExactlyTheCanonicalForm, which goes through the filesystem
// precisely because that is what it is about.
type recordingWriter struct {
	files []generatedFile
}

func (w *recordingWriter) WriteFile(path string, content []byte) error {
	w.files = append(w.files, generatedFile{path: path, content: content})
	return nil
}

// generateFiles runs one generation and returns the whole files it wrote, in
// the order it wrote them.
func generateFiles(t *testing.T, req *avrocpb.GenerateRequest) []generatedFile {
	t.Helper()

	return generateFilesIn(t.Context(), t, req)
}

// generateFilesIn is generateFiles with the context the generation runs under,
// which is the whole of the difference between a traced run and an untraced one.
//
// Tracing is an observation of a generation and never an input to it (#197), so
// the two have to produce the same bytes; making the context the only variable
// is what lets one assertion cover both.
func generateFilesIn(ctx context.Context, t *testing.T, req *avrocpb.GenerateRequest) []generatedFile {
	t.Helper()

	if req.Version == nil {
		req = proto.CloneOf(req)
		req.Version = proto.Int32(ir.Version)
	}

	w := &recordingWriter{}
	if err := Generate(ctx, req, w); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	return w.files
}

// tracedRun reports whether run number n is one of the traced ones.
//
// Half of them are, so that every repetition below compares a traced run's bytes
// against an untraced run's: the failure this is aimed at is a span opened
// between two writes changing what gets written, which no number of untraced
// repetitions would ever show.
func tracedRun(n int) bool { return n%2 == 1 }

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
// Determinism matters more here than anywhere else in the repository. A Parsing
// Canonical Form is what a schema fingerprint is computed over, so a byte that
// moves is not a diff — it is a different fingerprint, and a reader that can no
// longer identify a writer's messages.
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
		ctx := t.Context()
		var recorder *tracetest.SpanRecorder
		if tracedRun(run) {
			ctx, recorder = recordingContext(t)
		}

		again := generateFilesIn(ctx, t, req)
		if recorder != nil && len(recorder.Ended()) == 0 {
			t.Fatal("a traced run recorded no spans, so the comparison against an untraced one is vacuous")
		}
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
						{
							Name: proto.String("id"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Reference{Reference: primRef("long")},
							},
						},
						{
							Name: proto.String("settings"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_MapType{
									MapType: &avrocpb.Map{
										Values: &avrocpb.Type{
											Type: &avrocpb.Type_Reference{Reference: primRef("string")},
										},
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
