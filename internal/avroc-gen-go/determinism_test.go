// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

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
// directories would.
type recordingWriter struct {
	files []generatedFile
}

func (w *recordingWriter) WriteFile(path string, content []byte) error {
	w.files = append(w.files, generatedFile{path: path, content: content})
	return nil
}

// generateFiles runs one generation and returns the files it produced.
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

// assertRepeatedGenerationsAreIdentical runs the same generation repeatedly and
// requires every run to produce the same files with the same bytes.
//
// The repetition is what exercises map iteration order: Go randomises the
// starting point of every range over a map, so a generator ordering its output
// by one produces a different result within a handful of runs rather than
// reliably. This is the failure docs/plugin/SPEC.md calls the usual way the rule
// gets broken, and the one that breaks intermittently — which is worse than
// breaking every time, because it lands in somebody else's diff.
//
// What it cannot catch is a clock read, since two runs a millisecond apart agree
// on the date. internal/plugin.TestNoGeneratorReadsTheClock covers that half
// statically.
func assertRepeatedGenerationsAreIdentical(t *testing.T, req *avrocpb.GenerateRequest) {
	t.Helper()

	const runs = 16

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

// TestGenerateIsDeterministic is #120's dynamic half for this generator: the
// same descriptor and the same options produce the same set of paths with
// byte-identical contents, however many times they are run.
func TestGenerateIsDeterministic(t *testing.T) {
	testCases := []struct {
		name    string
		options []*avrocpb.Option
		schemas []*avrocpb.Schema
	}{
		{
			name: "plain",
			options: []*avrocpb.Option{
				{Name: proto.String("package_name"), Value: proto.String("avro")},
			},
			schemas: determinismSchemas(),
		},
		{
			// The fingerprint is computed rather than copied, so it is the part of
			// this generator's output most able to drift between two runs.
			name: "single object encoding",
			options: []*avrocpb.Option{
				{Name: proto.String("encoding"), Value: proto.String("single_object")},
				{Name: proto.String("package_name"), Value: proto.String("avro")},
			},
			schemas: determinismSchemas(),
		},
		{
			// An array root emits a whole file section the other roots do not —
			// the streaming reader, and the io and iter imports it pulls in — so
			// it is its own case rather than another schema in the list above.
			// It cannot be one anyway: single-object encoding refuses a
			// non-record root, and both option sets run over the same schemas.
			name: "array root",
			options: []*avrocpb.Option{
				{Name: proto.String("package_name"), Value: proto.String("stream")},
			},
			schemas: []*avrocpb.Schema{arrayRootSchema()},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assertRepeatedGenerationsAreIdentical(t, &avrocpb.GenerateRequest{
				Options: tc.options,
				Schemas: tc.schemas,
			})
		})
	}
}

// determinismSchemas is the widest resolved input this generator has a code
// path for: every type constructor, a named type referred to twice, and two
// schemas, so that a run has more than one file and more than one type to order.
func determinismSchemas() []*avrocpb.Schema {
	const ns = "com.example"

	return []*avrocpb.Schema{
		{
			Namespace: proto.String(ns),
			Type: &avrocpb.Type{
				Type: &avrocpb.Type_Record{
					Record: &avrocpb.Record{
						Name:      proto.String("Everything"),
						Namespace: proto.String(ns),
						FullName:  proto.String(ns + ".Everything"),
						Fields: []*avrocpb.Field{
							{
								Name: proto.String("name"),
								Type: primType("string"),
							},
							{
								Name: proto.String("kind"),
								Type: &avrocpb.Type{
									Type: &avrocpb.Type_EnumType{
										EnumType: &avrocpb.Enum{
											Name:      proto.String("Kind"),
											Namespace: proto.String(ns),
											FullName:  proto.String(ns + ".Kind"),
											Values: []*avrocpb.Ident{
												{Value: proto.String("FOO")},
												{Value: proto.String("BAR")},
												{Value: proto.String("BAZ")},
											},
										},
									},
								},
							},
							{
								Name: proto.String("hash"),
								Type: &avrocpb.Type{
									Type: &avrocpb.Type_Fixed{
										Fixed: &avrocpb.Fixed{
											Name:      proto.String("MD5"),
											Namespace: proto.String(ns),
											FullName:  proto.String(ns + ".MD5"),
											Size:      proto.Int32(16),
										},
									},
								},
							},
							{
								Name: proto.String("nullableHash"),
								Type: &avrocpb.Type{
									Type: &avrocpb.Type_Union{
										Union: &avrocpb.Union{
											Types: []*avrocpb.Type{
												primType("null"),
												{Type: &avrocpb.Type_Reference{Reference: namedRef(ns + ".MD5")}},
											},
										},
									},
								},
							},
							{
								Name: proto.String("scores"),
								Type: &avrocpb.Type{
									Type: &avrocpb.Type_Array{
										Array: &avrocpb.Array{Items: primType("double")},
									},
								},
							},
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
		},
		{
			Namespace: proto.String(ns),
			Type: &avrocpb.Type{
				Type: &avrocpb.Type_Record{
					Record: &avrocpb.Record{
						Name:      proto.String("Simple"),
						Namespace: proto.String(ns),
						FullName:  proto.String(ns + ".Simple"),
						Fields: []*avrocpb.Field{
							{Name: proto.String("id"), Type: primType("long")},
							{Name: proto.String("flag"), Type: primType("boolean")},
						},
					},
				},
			},
		},
	}
}
