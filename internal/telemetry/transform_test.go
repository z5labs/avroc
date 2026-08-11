// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package telemetry

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// recorder is a span exporter that keeps what it was given, so that a test can
// hand real ReadOnlySpans — the SDK's, not a hand-built stand-in — to the
// transform.
type recorder struct {
	mu    sync.Mutex
	spans []sdktrace.ReadOnlySpan
}

func (r *recorder) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spans = append(r.spans, spans...)
	return nil
}

func (r *recorder) Shutdown(context.Context) error { return nil }

func (r *recorder) recorded() []sdktrace.ReadOnlySpan {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sdktrace.ReadOnlySpan(nil), r.spans...)
}

// record produces spans through a real provider and returns them as the
// exporter saw them.
func record(t *testing.T, res *resource.Resource, produce func(tp trace.TracerProvider)) []sdktrace.ReadOnlySpan {
	t.Helper()

	rec := &recorder{}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(rec),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	produce(tp)
	if err := tp.Shutdown(t.Context()); err != nil {
		t.Fatalf("failed to shut the recording provider down: %v", err)
	}
	return rec.recorded()
}

// TestASpanSurvivesTheTransform covers everything OTLP carries about one span,
// because a field that is dropped here is a field nobody misses until they are
// looking at a trace and wondering where it went.
func TestASpanSurvivesTheTransform(t *testing.T) {
	linked := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		SpanID:  trace.SpanID{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18},
	})

	spans := record(t, resource.NewSchemaless(attribute.String("service.name", "avroc")), func(tp trace.TracerProvider) {
		tracer := tp.Tracer("avroc/test", trace.WithInstrumentationVersion("v1.2.3"))

		ctx, parent := tracer.Start(context.Background(), "generate", trace.WithSpanKind(trace.SpanKindInternal))
		_, child := tracer.Start(ctx, "run generator",
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithLinks(trace.Link{SpanContext: linked, Attributes: []attribute.KeyValue{attribute.String("why", "fan-in")}}),
		)
		child.SetAttributes(
			attribute.String("generator", "go"),
			attribute.Int64("schemas", 3),
			attribute.Bool("cached", false),
			attribute.Float64("ratio", 0.5),
			attribute.StringSlice("options", []string{"a", "b"}),
			attribute.Int64Slice("codes", []int64{1, 2}),
			attribute.BoolSlice("flags", []bool{true, false}),
			attribute.Float64Slice("ratios", []float64{0.25, 0.75}),
		)
		child.AddEvent("descriptor written", trace.WithAttributes(attribute.Int("bytes", 42)))
		child.SetStatus(codes.Error, "generator exited 1")
		child.End()
		parent.End()
	})

	if len(spans) != 2 {
		t.Fatalf("recorded %d spans, want 2", len(spans))
	}

	out := resourceSpans(spans)
	if len(out) != 1 {
		t.Fatalf("transformed into %d ResourceSpans, want 1 — every span shares one resource", len(out))
	}
	if len(out[0].GetScopeSpans()) != 1 {
		t.Fatalf("transformed into %d ScopeSpans, want 1 — every span shares one scope", len(out[0].GetScopeSpans()))
	}

	scope := out[0].GetScopeSpans()[0].GetScope()
	if got, want := scope.GetName(), "avroc/test"; got != want {
		t.Errorf("scope name = %q, want %q", got, want)
	}
	if got, want := scope.GetVersion(), "v1.2.3"; got != want {
		t.Errorf("scope version = %q, want %q", got, want)
	}

	child := findSpan(t, out, "run generator")

	if got := len(child.GetTraceId()); got != 16 {
		t.Errorf("trace id is %d bytes, want 16", got)
	}
	if got := len(child.GetSpanId()); got != 8 {
		t.Errorf("span id is %d bytes, want 8", got)
	}
	if len(child.GetParentSpanId()) != 8 {
		t.Errorf("the child span carries no parent span id")
	}
	if got, want := child.GetKind(), tracepb.Span_SPAN_KIND_CLIENT; got != want {
		t.Errorf("kind = %v, want %v", got, want)
	}
	if child.GetStartTimeUnixNano() == 0 || child.GetEndTimeUnixNano() < child.GetStartTimeUnixNano() {
		t.Errorf("times are [%d, %d], want an interval that goes forwards",
			child.GetStartTimeUnixNano(), child.GetEndTimeUnixNano())
	}
	if got, want := child.GetStatus().GetCode(), tracepb.Status_STATUS_CODE_ERROR; got != want {
		t.Errorf("status = %v, want %v", got, want)
	}
	if got, want := child.GetStatus().GetMessage(), "generator exited 1"; got != want {
		t.Errorf("status message = %q, want %q", got, want)
	}

	if len(child.GetEvents()) != 1 {
		t.Fatalf("%d events, want 1", len(child.GetEvents()))
	}
	if got, want := child.GetEvents()[0].GetName(), "descriptor written"; got != want {
		t.Errorf("event name = %q, want %q", got, want)
	}
	if child.GetEvents()[0].GetTimeUnixNano() == 0 {
		t.Error("the event carries no timestamp")
	}

	if len(child.GetLinks()) != 1 {
		t.Fatalf("%d links, want 1", len(child.GetLinks()))
	}
	if got := child.GetLinks()[0].GetTraceId(); len(got) != 16 {
		t.Errorf("the link's trace id is %d bytes, want 16", len(got))
	}

	attrs := attributesOf(child.GetAttributes())
	if got := attrs["generator"].GetStringValue(); got != "go" {
		t.Errorf("generator = %q, want %q", got, "go")
	}
	if got := attrs["schemas"].GetIntValue(); got != 3 {
		t.Errorf("schemas = %d, want 3", got)
	}
	if got := attrs["cached"].GetBoolValue(); got {
		t.Error("cached = true, want false")
	}
	if got := attrs["ratio"].GetDoubleValue(); got != 0.5 {
		t.Errorf("ratio = %v, want 0.5", got)
	}
	if got := len(attrs["options"].GetArrayValue().GetValues()); got != 2 {
		t.Errorf("options holds %d values, want 2", got)
	}
	if got := attrs["options"].GetArrayValue().GetValues()[1].GetStringValue(); got != "b" {
		t.Errorf("options[1] = %q, want %q", got, "b")
	}
	if got := attrs["codes"].GetArrayValue().GetValues()[0].GetIntValue(); got != 1 {
		t.Errorf("codes[0] = %d, want 1", got)
	}
	if got := attrs["flags"].GetArrayValue().GetValues()[0].GetBoolValue(); !got {
		t.Error("flags[0] = false, want true")
	}
	if got := attrs["ratios"].GetArrayValue().GetValues()[0].GetDoubleValue(); got != 0.25 {
		t.Errorf("ratios[0] = %v, want 0.25", got)
	}
}

// TestSpansAreGroupedByResourceAndScope: OTLP's nesting is the transform's job,
// and getting it wrong shows up as one trace arriving as several.
func TestSpansAreGroupedByResourceAndScope(t *testing.T) {
	first := record(t, resource.NewSchemaless(attribute.String("service.name", "one")), func(tp trace.TracerProvider) {
		_, a := tp.Tracer("scope/a").Start(context.Background(), "a1")
		a.End()
		_, b := tp.Tracer("scope/b").Start(context.Background(), "b1")
		b.End()
	})
	second := record(t, resource.NewSchemaless(attribute.String("service.name", "two")), func(tp trace.TracerProvider) {
		_, c := tp.Tracer("scope/a").Start(context.Background(), "a2")
		c.End()
	})

	out := resourceSpans(append(first, second...))
	if len(out) != 2 {
		t.Fatalf("%d ResourceSpans, want 2 — the batch holds two resources", len(out))
	}
	if got := len(out[0].GetScopeSpans()); got != 2 {
		t.Errorf("the first resource has %d ScopeSpans, want 2", got)
	}
	if got := len(out[1].GetScopeSpans()); got != 1 {
		t.Errorf("the second resource has %d ScopeSpans, want 1", got)
	}
	if got, want := out[0].GetScopeSpans()[0].GetScope().GetName(), "scope/a"; got != want {
		t.Errorf("the scopes came out in the order %q, want the order the batch mentioned them (%q first)", got, want)
	}
}

// TestAnUnsetStatusCarriesNoMessage: the specification gives the description to
// the error case, so a description left on a span that succeeded is not shipped
// as an explanation of the success.
func TestAnUnsetStatusCarriesNoMessage(t *testing.T) {
	cases := map[codes.Code]struct {
		code    tracepb.Status_StatusCode
		message string
	}{
		codes.Unset: {tracepb.Status_STATUS_CODE_UNSET, ""},
		codes.Ok:    {tracepb.Status_STATUS_CODE_OK, ""},
		codes.Error: {tracepb.Status_STATUS_CODE_ERROR, "why"},
	}

	for code, want := range cases {
		got := status(sdktrace.Status{Code: code, Description: "why"})
		if got.GetCode() != want.code {
			t.Errorf("%v: code = %v, want %v", code, got.GetCode(), want.code)
		}
		if got.GetMessage() != want.message {
			t.Errorf("%v: message = %q, want %q", code, got.GetMessage(), want.message)
		}
	}
}

// TestAnEmptyBatchIsAnEmptyRequest: the exporter refuses an empty batch before
// it gets here, and the transform agrees rather than producing an envelope with
// nothing in it.
func TestAnEmptyBatchIsAnEmptyRequest(t *testing.T) {
	body, err := marshalRequest(resourceSpans(nil))
	if err != nil {
		t.Fatalf("marshalRequest returned %v, want nil", err)
	}
	if len(body) != 0 {
		t.Errorf("an empty batch encoded to %d bytes, want 0", len(body))
	}
}

// TestTheRequestEncodingIsDeterministic: two exports of the same spans produce
// the same bytes. Nothing downstream requires it, and a recorded request being
// comparable is worth the one option it costs.
func TestTheRequestEncodingIsDeterministic(t *testing.T) {
	spans := record(t, resource.NewSchemaless(attribute.String("service.name", "avroc")), func(tp trace.TracerProvider) {
		_, span := tp.Tracer("avroc/test").Start(context.Background(), "generate")
		span.SetAttributes(attribute.String("a", "1"), attribute.Int("b", 2))
		span.End()
	})

	first, err := marshalRequest(resourceSpans(spans))
	if err != nil {
		t.Fatalf("marshalRequest returned %v, want nil", err)
	}
	for range 32 {
		again, err := marshalRequest(resourceSpans(spans))
		if err != nil {
			t.Fatalf("marshalRequest returned %v, want nil", err)
		}
		if string(again) != string(first) {
			t.Fatal("two encodings of the same spans differ")
		}
	}
}

// TestAnExportAfterShutdownIsNotARequest: Shutdown is reachable from anywhere,
// and an exporter that answers "no" is cheaper to reason about than one that
// must not be asked.
func TestAnExportAfterShutdownIsNotARequest(t *testing.T) {
	col := newCollector(t)
	e := newExporter(config{endpoint: col.endpoint() + tracesPath})

	if err := e.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown returned %v, want nil", err)
	}

	spans := record(t, resource.NewSchemaless(), func(tp trace.TracerProvider) {
		_, span := tp.Tracer("avroc/test").Start(context.Background(), "generate")
		span.End()
	})
	if err := e.ExportSpans(t.Context(), spans); err != nil {
		t.Errorf("ExportSpans after Shutdown returned %v, want nil", err)
	}
	if got := len(col.recorded()); got != 0 {
		t.Errorf("an export after Shutdown made %d request(s)", got)
	}
}

// TestABatchThatCarriesNothingIsNotARequest: a batch is non-empty and still
// transforms to nothing when every element in it is nil. Posting the empty body
// that produces would be a request a collector is entitled to reject, and the
// rejection would arrive as a log record about a trace nobody sent.
func TestABatchThatCarriesNothingIsNotARequest(t *testing.T) {
	col := newCollector(t)
	e := newExporter(config{endpoint: col.endpoint() + tracesPath})

	if err := e.ExportSpans(t.Context(), []sdktrace.ReadOnlySpan{nil, nil}); err != nil {
		t.Errorf("ExportSpans over a batch of nothing returned %v, want nil", err)
	}
	if got := len(col.recorded()); got != 0 {
		t.Errorf("a batch of nothing made %d request(s)", got)
	}
}

// TestAnExportThatCannotBeDeliveredIsAnError: the batch processor hands this to
// the error handler, and a silent failure here is a trace nobody knows is
// missing.
func TestAnExportThatCannotBeDeliveredIsAnError(t *testing.T) {
	e := newExporter(config{endpoint: "http://127.0.0.1:1/v1/traces"})
	e.client.Timeout = 250 * time.Millisecond

	spans := record(t, resource.NewSchemaless(), func(tp trace.TracerProvider) {
		_, span := tp.Tracer("avroc/test").Start(context.Background(), "generate")
		span.End()
	})

	if err := e.ExportSpans(t.Context(), spans); err == nil {
		t.Error("an export to a port nothing listens on returned no error")
	}
}

func findSpan(t *testing.T, in []*tracepb.ResourceSpans, name string) *tracepb.Span {
	t.Helper()

	for _, rs := range in {
		for _, ss := range rs.GetScopeSpans() {
			for _, span := range ss.GetSpans() {
				if span.GetName() == name {
					return span
				}
			}
		}
	}

	t.Fatalf("no span named %q was transformed", name)
	return nil
}

func attributesOf(in []*commonpb.KeyValue) map[string]*commonpb.AnyValue {
	out := make(map[string]*commonpb.AnyValue, len(in))
	for _, kv := range in {
		out[kv.GetKey()] = kv.GetValue()
	}
	return out
}
