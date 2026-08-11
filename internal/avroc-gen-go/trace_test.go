// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

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

// The span names this generator's phases are expected to have, written out as
// literals rather than taken from internal/plugin's constants.
//
// It is the same discipline `dagger call tag-scheme` keeps: a check that reads
// the constant it is checking moves with it, so renaming a span would go on
// passing here while every dashboard built on the old name broke silently.
const (
	spanInvocation         = "avroc.plugin.generate"
	spanDescriptorValidate = "avroc.plugin.descriptor.validate"
	spanOptionsParse       = "avroc.plugin.options.parse"
	spanSchemaGenerate     = "avroc.plugin.schema.generate"
	spanFingerprint        = "avroc.plugin.fingerprint"
	spanFileWrite          = "avroc.plugin.file.write"
)

// The attributes they carry, on the same terms.
const (
	attrSchema = "schema"
	attrPath   = "path"
)

// recordingContext is what plugin.Main hands Generate on a traced invocation: a
// context carrying the invocation's own span, from a provider whose spans the
// test reads back once they have ended.
//
// The SDK rather than a fake, because what is being asserted is what a collector
// would receive — the parentage and the attributes are things the SDK computes,
// and a fake tracer would only assert that this package called itself.
//
// The invocation span is ended by a cleanup rather than here, so that Generate
// runs inside it exactly as it does under plugin.Main. Nothing this test reads
// includes it: [tracetest.SpanRecorder.Ended] holds the spans that have ended,
// and the invocation's ends after the assertions.
func recordingContext(t *testing.T) (context.Context, *tracetest.SpanRecorder) {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	ctx, span := provider.Tracer("test").Start(t.Context(), spanInvocation)
	t.Cleanup(func() { span.End() })
	return ctx, recorder
}

// spansNamed is every recorded span of that name.
func spansNamed(spans []sdktrace.ReadOnlySpan, name string) []sdktrace.ReadOnlySpan {
	var found []sdktrace.ReadOnlySpan
	for _, span := range spans {
		if span.Name() == name {
			found = append(found, span)
		}
	}
	return found
}

// recordedNames is every span a generation ended, in order, for a failure
// message that shows what was there instead.
func recordedNames(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.Name())
	}
	return names
}

// stringAttr reads one string attribute off a span, failing when it is absent:
// an attribute this generator promises to set and did not is the finding, not a
// zero value to compare against.
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

// TestAGenerationsPhasesAreSpansUnderTheInvocations is #197 for this generator:
// the invocation span answers which generator was slow, and these answer which
// part of it.
func TestAGenerationsPhasesAreSpansUnderTheInvocations(t *testing.T) {
	ctx, recorder := recordingContext(t)

	req := &avrocpb.GenerateRequest{
		Version: proto.Int32(ir.Version),
		Options: []*avrocpb.Option{
			{Name: proto.String("encoding"), Value: proto.String("single_object")},
			{Name: proto.String("package_name"), Value: proto.String("avro")},
		},
		Schemas: determinismSchemas(),
	}
	schemas := len(req.GetSchemas())

	var w recordingWriter
	if err := Generate(ctx, req, &w); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	spans := recorder.Ended()

	// The descriptor and the options are properties of the request, so they
	// happen once however many schemas it carries.
	for _, name := range []string{spanDescriptorValidate, spanOptionsParse} {
		if got := len(spansNamed(spans, name)); got != 1 {
			t.Errorf("the generation opened %d %q spans, want 1: %v", got, name, recordedNames(spans))
		}
	}

	// One per schema, named by the schema's base name — the same name the file
	// it produced is built from, which is what makes the trace readable beside
	// the output directory.
	schemaSpans := spansNamed(spans, spanSchemaGenerate)
	if len(schemaSpans) != schemas {
		t.Fatalf("the generation opened %d %q spans over %d schemas: %v",
			len(schemaSpans), spanSchemaGenerate, schemas, recordedNames(spans))
	}
	for i, span := range schemaSpans {
		want := ir.SchemaBaseName(req.GetSchemas()[i])
		if got := stringAttr(t, span, attrSchema); got != want {
			t.Errorf("%s = %q on span %d, want %q", attrSchema, got, i, want)
		}
	}

	// One per schema again, and a child of that schema's span rather than a
	// sibling: the fingerprint is part of rendering the schema and is separated
	// out only because it is the one place this generator computes over the IR.
	fingerprints := spansNamed(spans, spanFingerprint)
	if len(fingerprints) != schemas {
		t.Fatalf("the generation opened %d %q spans over %d schemas: %v",
			len(fingerprints), spanFingerprint, schemas, recordedNames(spans))
	}
	for i, span := range fingerprints {
		if span.Parent().SpanID() != schemaSpans[i].SpanContext().SpanID() {
			t.Errorf("the %q span is not a child of the %q span it belongs to", spanFingerprint, spanSchemaGenerate)
		}
	}

	// One per file, named by the path relative to --out, which is the only form
	// of the path that means anything: the absolute one is a scratch directory
	// avroc made for this invocation alone.
	//
	// A child of its schema's span rather than a sibling, so that the span for a
	// schema covers everything done for that schema — which is what the two
	// generators with no write span of their own rely on, and what keeps one span
	// name comparable across all three. The rendering is still separately
	// readable, as the difference between the two.
	writes := spansNamed(spans, spanFileWrite)
	if len(writes) != len(w.files) {
		t.Fatalf("the generation opened %d %q spans and wrote %d files: %v",
			len(writes), spanFileWrite, len(w.files), recordedNames(spans))
	}
	for i, span := range writes {
		if got, want := stringAttr(t, span, attrPath), w.files[i].path; got != want {
			t.Errorf("%s = %q on span %d, want %q", attrPath, got, i, want)
		}
		if span.Parent().SpanID() != schemaSpans[i].SpanContext().SpanID() {
			t.Errorf("the %q span for %q is not a child of the %q span it belongs to",
				spanFileWrite, w.files[i].path, spanSchemaGenerate)
		}
	}

	// Every phase is a child of the invocation, directly or through the schema
	// it belongs to, so a trace of a whole run nests generators under avroc.
	for _, span := range spans {
		if !span.Parent().IsValid() {
			t.Errorf("the %q span has no parent, so it started a trace of its own", span.Name())
		}
	}
}

// TestAFailedPhaseCarriesItsFailure: a phase that failed is the phase the
// invocation failed in, and the span is where that is recorded — the exit status
// on the invocation's own span says only that something went wrong.
func TestAFailedPhaseCarriesItsFailure(t *testing.T) {
	ctx, recorder := recordingContext(t)

	err := Generate(ctx, &avrocpb.GenerateRequest{
		Version: proto.Int32(ir.Version),
		Schemas: determinismSchemas(),
	}, &recordingWriter{})
	if err == nil {
		t.Fatal("Generate accepted a descriptor with no package_name option")
	}

	spans := spansNamed(recorder.Ended(), spanOptionsParse)
	if len(spans) != 1 {
		t.Fatalf("the generation opened %d %q spans, want 1", len(spans), spanOptionsParse)
	}
	if spans[0].Status().Code != codes.Error {
		t.Errorf("the %q span is not marked failed: %v", spanOptionsParse, spans[0].Status())
	}
	if len(spans[0].Events()) == 0 {
		t.Errorf("the %q span records no error", spanOptionsParse)
	}
}

// TestSpanCountIsAFunctionOfTheDescriptorAndNotTheSchema is the cardinality
// rule, and it is the reason the granularity stops where it does.
//
// A span per schema and a span per file are bounded by the manifest, which a
// person wrote. A span per type or per field would be bounded by the IDL, and a
// schema with a few thousand fields would produce a trace nobody can open — so
// anything finer than a schema or a file is an attribute rather than a span, and
// this is what notices the day that stops being true.
func TestSpanCountIsAFunctionOfTheDescriptorAndNotTheSchema(t *testing.T) {
	names := func(t *testing.T, fields int) []string {
		t.Helper()

		ctx, recorder := recordingContext(t)
		err := Generate(ctx, &avrocpb.GenerateRequest{
			Version: proto.Int32(ir.Version),
			Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("avro")}},
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

// TestAnUntracedGenerationStartsNoSpans is the other half of the cardinality
// rule and the one that costs nothing to get wrong quietly: almost every
// invocation of this generator has no collector anywhere near it, and it must
// pay nothing per schema and nothing per file for instrumentation nobody is
// reading.
//
// The provider counts rather than records, because "no span was exported" is
// what a non-recording provider gives you for free — what is being asserted is
// that none was *started*, and that no tracer was even asked for.
func TestAnUntracedGenerationStartsNoSpans(t *testing.T) {
	counter := &spanCounter{}
	ctx := oteltrace.ContextWithSpan(t.Context(), untracedSpan{provider: counter})

	var w recordingWriter
	err := Generate(ctx, &avrocpb.GenerateRequest{
		Version: proto.Int32(ir.Version),
		Options: []*avrocpb.Option{
			{Name: proto.String("encoding"), Value: proto.String("single_object")},
			{Name: proto.String("package_name"), Value: proto.String("avro")},
		},
		Schemas: determinismSchemas(),
	}, &w)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if len(w.files) == 0 {
		t.Fatal("the generation wrote no files, so it proves nothing about the work per file")
	}

	if counter.tracers != 0 {
		t.Errorf("an untraced generation asked for %d tracers, want 0", counter.tracers)
	}
	if counter.starts != 0 {
		t.Errorf("an untraced generation started %d spans, want 0", counter.starts)
	}
}

// TestAFailedWriteFailsTheSchemasSpanToo is why the write is inside the
// schema's span rather than beside it.
//
// A file this generator could not write is a schema it did not produce, so the
// failure belongs on the span for that schema as well as on the span for the
// write — a reader who has collapsed the children, or a backend that only counts
// failed spans per schema, would otherwise see a schema that went fine.
func TestAFailedWriteFailsTheSchemasSpanToo(t *testing.T) {
	ctx, recorder := recordingContext(t)

	err := Generate(ctx, &avrocpb.GenerateRequest{
		Version: proto.Int32(ir.Version),
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("avro")}},
		Schemas: determinismSchemas(),
	}, failingWriter{})
	if err == nil {
		t.Fatal("Generate ignored a writer that refuses every file")
	}

	for _, name := range []string{spanFileWrite, spanSchemaGenerate} {
		spans := spansNamed(recorder.Ended(), name)
		if len(spans) == 0 {
			t.Fatalf("the generation opened no %q span", name)
		}
		if spans[0].Status().Code != codes.Error {
			t.Errorf("the %q span is not marked failed: %v", name, spans[0].Status())
		}
	}
}

// wideSchema is one record with as many fields as asked for, which is the input
// the cardinality rule is about: it is the user's IDL that decides this number,
// and nothing bounded by it may become a span.
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
// is exactly what a no-op provider hands plugin.Main when there is no
// TRACEPARENT and no endpoint. It differs from noop.Span in one way, which is
// that it names a provider the test can count through — so a generator that
// asked for a tracer anyway is caught rather than silently satisfied.
type untracedSpan struct {
	noop.Span

	provider *spanCounter
}

func (s untracedSpan) TracerProvider() oteltrace.TracerProvider { return s.provider }
