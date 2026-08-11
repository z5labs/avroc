// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgenjson

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/ir"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/protobuf/proto"
)

// The span names this generator is expected to open, written out as literals
// rather than taken from internal/plugin's constants: a check that reads the
// constant it is checking moves with it, and a renamed span would go on passing
// here while every dashboard built on the old name broke silently.
const (
	spanInvocation     = "avroc.plugin.generate"
	spanSchemaGenerate = "avroc.plugin.schema.generate"
)

// attrSchema is the attribute the per-schema span carries, on the same terms.
const attrSchema = "schema"

// recordingContext is what plugin.Main hands Generate on a traced invocation: a
// context carrying the invocation's own span, from a provider whose spans the
// test reads back once they have ended.
//
// The invocation span is ended by a cleanup rather than here, so that Generate
// runs inside it exactly as it does under plugin.Main, and nothing read before
// the test returns includes it.
func recordingContext(t *testing.T) (context.Context, *tracetest.SpanRecorder) {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	ctx, span := provider.Tracer("test").Start(t.Context(), spanInvocation)
	t.Cleanup(func() { span.End() })
	return ctx, recorder
}

// recordedNames is every span a generation ended, in order.
func recordedNames(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.Name())
	}
	return names
}

// stringAttr reads one string attribute off a span, failing when it is absent.
func stringAttr(t *testing.T, span sdktrace.ReadOnlySpan, key string) string {
	t.Helper()

	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.AsString()
		}
	}
	t.Fatalf("span %q carries no %q attribute: %v", span.Name(), key, span.Attributes())
	return ""
}

// TestEverySchemaIsOneSpanAndNothingFiner is #197 for this generator, and the
// "nothing finer" is the substance of it.
//
// avroc-gen-json writes one file per schema and does no rendering to speak of,
// so a span for the descriptor check, for the options it does not have, or for a
// write it performs in one call would be more instrumentation than there is work
// — and a trace carrying it would be harder to read rather than easier.
// avroc-gen-go is where the finer phases belong.
func TestEverySchemaIsOneSpanAndNothingFiner(t *testing.T) {
	ctx, recorder := recordingContext(t)

	req := &avrocpb.GenerateRequest{
		Version: proto.Int32(ir.Version),
		Schemas: []*avrocpb.Schema{resolvedTestRecord(), determinismSecondSchema()},
	}

	var w recordingWriter
	if err := Generate(ctx, req, &w); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	spans := recorder.Ended()
	if len(spans) != len(req.GetSchemas()) {
		t.Fatalf("the generation opened %d spans over %d schemas: %v",
			len(spans), len(req.GetSchemas()), recordedNames(spans))
	}
	for i, span := range spans {
		if span.Name() != spanSchemaGenerate {
			t.Errorf("span %d is named %q, want %q", i, span.Name(), spanSchemaGenerate)
			continue
		}
		if got, want := stringAttr(t, span, attrSchema), ir.SchemaBaseName(req.GetSchemas()[i]); got != want {
			t.Errorf("%s = %q on span %d, want %q", attrSchema, got, i, want)
		}
		if !span.Parent().IsValid() {
			t.Errorf("span %d has no parent, so it started a trace of its own", i)
		}
	}
}

// TestSpanCountIsAFunctionOfTheDescriptorAndNotTheSchema is the cardinality
// rule: a span per schema is bounded by the manifest a person wrote, and a span
// per type or per field would be bounded by the user's IDL, where a few thousand
// fields would produce a trace nobody can open.
func TestSpanCountIsAFunctionOfTheDescriptorAndNotTheSchema(t *testing.T) {
	names := func(t *testing.T, fields int) []string {
		t.Helper()

		ctx, recorder := recordingContext(t)
		err := Generate(ctx, &avrocpb.GenerateRequest{
			Version: proto.Int32(ir.Version),
			Schemas: []*avrocpb.Schema{wideSchema(fields)},
		}, &recordingWriter{})
		if err != nil {
			t.Fatalf("Generate over a %d-field schema failed: %v", fields, err)
		}

		got := recordedNames(recorder.Ended())
		slices.Sort(got)
		return got
	}

	narrow := names(t, 1)
	wide := names(t, 500)

	if len(narrow) == 0 {
		t.Fatal("a generation opened no spans at all, so the comparison is vacuous")
	}
	if !slices.Equal(narrow, wide) {
		t.Errorf("a 500-field schema produced %v and a 1-field schema produced %v: the span count follows the IDL",
			wide, narrow)
	}
}

// TestAnUntracedGenerationStartsNoSpans: almost every invocation of this
// generator has no collector anywhere near it, and it must pay nothing per
// schema for instrumentation nobody is reading.
//
// The provider counts rather than records, because "no span was exported" is
// what a non-recording provider gives for free — what is asserted here is that
// none was started, and that no tracer was even asked for.
func TestAnUntracedGenerationStartsNoSpans(t *testing.T) {
	counter := &spanCounter{}
	ctx := oteltrace.ContextWithSpan(t.Context(), untracedSpan{provider: counter})

	var w recordingWriter
	err := Generate(ctx, &avrocpb.GenerateRequest{
		Version: proto.Int32(ir.Version),
		Schemas: []*avrocpb.Schema{resolvedTestRecord(), determinismSecondSchema()},
	}, &w)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if len(w.files) == 0 {
		t.Fatal("the generation wrote no files, so it proves nothing about the work per schema")
	}

	if counter.tracers != 0 {
		t.Errorf("an untraced generation asked for %d tracers, want 0", counter.tracers)
	}
	if counter.starts != 0 {
		t.Errorf("an untraced generation started %d spans, want 0", counter.starts)
	}
}

// TestAFailedWriteFailsTheSchemasSpan is why the write is inside the schema's
// span rather than after it.
//
// This generator opens no span of its own for the write, so a write left outside
// would put both the filesystem time and the failure on the invocation rather
// than on the schema they belong to — and a schema whose file was never written
// would read as a schema that went fine.
func TestAFailedWriteFailsTheSchemasSpan(t *testing.T) {
	ctx, recorder := recordingContext(t)

	err := Generate(ctx, &avrocpb.GenerateRequest{
		Version: proto.Int32(ir.Version),
		Schemas: []*avrocpb.Schema{resolvedTestRecord()},
	}, failingWriter{})
	if err == nil {
		t.Fatal("Generate ignored a writer that refuses every file")
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("the generation opened %d spans, want 1: %v", len(spans), recordedNames(spans))
	}
	if spans[0].Status().Code != codes.Error {
		t.Errorf("the %q span is not marked failed: %v", spans[0].Name(), spans[0].Status())
	}
}

// wideSchema is one record with as many fields as asked for, which is the input
// the cardinality rule is about: the user's IDL decides this number, and nothing
// bounded by it may become a span.
func wideSchema(fields int) *avrocpb.Schema {
	const ns = "com.example"

	record := &avrocpb.Record{
		Name:      proto.String("Wide"),
		Namespace: proto.String(ns),
		FullName:  proto.String(ns + ".Wide"),
	}
	for i := range fields {
		record.Fields = append(record.Fields, &avrocpb.Field{
			Name: proto.String(fmt.Sprintf("field%d", i)),
			Type: primType("string"),
		})
	}

	return &avrocpb.Schema{
		Namespace: proto.String(ns),
		Type:      &avrocpb.Type{Type: &avrocpb.Type_Record{Record: record}},
	}
}

// spanCounter is a tracer provider that starts no spans and counts every
// request for one.
type spanCounter struct {
	embedded.TracerProvider

	tracers int
	starts  int
}

func (p *spanCounter) Tracer(string, ...oteltrace.TracerOption) oteltrace.Tracer {
	p.tracers++
	return countingTracer{provider: p}
}

type countingTracer struct {
	embedded.Tracer

	provider *spanCounter
}

func (t countingTracer) Start(ctx context.Context, _ string, _ ...oteltrace.SpanStartOption) (context.Context, oteltrace.Span) {
	t.provider.starts++
	return ctx, noop.Span{}
}

// untracedSpan is the span an untraced invocation carries: not recording, which
// is what a no-op provider hands plugin.Main when there is no TRACEPARENT and no
// endpoint. It differs from noop.Span in naming a provider the test can count
// through, so a generator that asked for a tracer anyway is caught rather than
// silently satisfied.
type untracedSpan struct {
	noop.Span

	provider *spanCounter
}

func (s untracedSpan) TracerProvider() oteltrace.TracerProvider { return s.provider }
