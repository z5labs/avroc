// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package plugin

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/z5labs/avroc/internal/cli"
	"github.com/z5labs/avroc/internal/ir"
	"github.com/z5labs/avroc/internal/telemetry"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
	"go.opentelemetry.io/otel/trace/noop"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// The trace context a test hands a generator, standing in for the one avroc
// writes per invocation. It is a literal rather than a value produced by an SDK
// so that the parentage assertions below compare against something written down.
const (
	parentTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	parentSpanID  = "00f067aa0ba902b7"
	parentHeader  = "00-" + parentTraceID + "-" + parentSpanID + "-01"
)

// tracedCLI is one generator invocation with tracing configured, plus whatever
// else the test wants in the environment — a TRACEPARENT, most of the time.
func tracedCLI(t *testing.T, endpoint string, env map[string]string, args ...string) (cli.Context, *lockedBuffer) {
	t.Helper()

	environ := map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": endpoint}
	for key, value := range env {
		environ[key] = value
	}

	logs := &lockedBuffer{}
	return cli.Context{
		Log: slog.New(slog.NewTextHandler(logs, nil)),
		Env: cli.EnvironmentFunc(func(key string) (string, bool) {
			value, ok := environ[key]
			return value, ok
		}),
		Args: args,
	}, logs
}

// writeDescriptor writes a descriptor a generation invocation can be pointed at,
// and returns the path avroc would have passed as --descriptor.
func writeDescriptor(t *testing.T) string {
	t.Helper()

	b, err := proto.Marshal(testDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "descriptor.binpb")
	if err := os.WriteFile(path, b, 0o444); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestAGeneratorsSpanIsAChildOfTheOneAvrocOpened is the story: a generation
// invocation opens one span, parented from TRACEPARENT, carrying the generator's
// name and the IR version of the descriptor it was handed.
//
// The parentage is read out of the exported bytes rather than out of the SDK,
// because what is being checked is that the value crossed a process boundary as
// an environment variable and came back as a parent — and an in-process
// assertion would hold whether it did or not.
func TestAGeneratorsSpanIsAChildOfTheOneAvrocOpened(t *testing.T) {
	collector := newSpanCollector(t)
	c, _ := tracedCLI(t, collector.endpoint(), map[string]string{telemetry.EnvTraceparent: parentHeader},
		"--descriptor", writeDescriptor(t), "--out", t.TempDir())

	if code := Main(t.Context(), c, testInfo(), echoGenerate); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	span := collector.only(t)
	if got := span.GetName(); got != spanGenerate {
		t.Errorf("span name = %q, want %q", got, spanGenerate)
	}
	if got, want := hex.EncodeToString(span.GetTraceId()), parentTraceID; got != want {
		t.Errorf("trace id = %s, want %s — the generator did not join avroc's trace", got, want)
	}
	if got, want := hex.EncodeToString(span.GetParentSpanId()), parentSpanID; got != want {
		t.Errorf("parent span id = %s, want %s", got, want)
	}
	if got, want := stringAttr(t, span, attrGenerator), testInfo().Executable(); got != want {
		t.Errorf("%s = %q, want %q", attrGenerator, got, want)
	}
	if got, want := intAttr(t, span, attrIRVersion), int64(ir.Version); got != want {
		t.Errorf("%s = %d, want %d — the descriptor's version is not on the span", attrIRVersion, got, want)
	}
	if got, want := intAttr(t, span, attrExitCode), int64(0); got != want {
		t.Errorf("%s = %d, want %d", attrExitCode, got, want)
	}
	if got, want := span.GetStatus().GetCode(), tracepb.Status_STATUS_CODE_OK; got != want {
		t.Errorf("status = %s, want %s", got, want)
	}
}

// TestAPluginInfoInvocationIsTracedOnTheSameTerms: the handshake is a whole
// process and avroc propagates to it (#193), so it gets a span too. The IR
// version it carries is the one being declared, there being no descriptor.
func TestAPluginInfoInvocationIsTracedOnTheSameTerms(t *testing.T) {
	collector := newSpanCollector(t)
	c, _ := tracedCLI(t, collector.endpoint(), map[string]string{telemetry.EnvTraceparent: parentHeader}, PluginInfoFlag)

	var stdout bytes.Buffer
	if code := run(t.Context(), c, testInfo(), echoGenerate, &stdout); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stdout.Len() == 0 {
		t.Fatal("the handshake wrote no declaration, so this proves nothing about tracing one")
	}

	span := collector.only(t)
	if got := span.GetName(); got != spanPluginInfo {
		t.Errorf("span name = %q, want %q", got, spanPluginInfo)
	}
	if got, want := hex.EncodeToString(span.GetTraceId()), parentTraceID; got != want {
		t.Errorf("trace id = %s, want %s", got, want)
	}
	if got, want := intAttr(t, span, attrIRVersion), int64(ir.Version); got != want {
		t.Errorf("%s = %d, want %d", attrIRVersion, got, want)
	}
	if got, want := span.GetStatus().GetCode(), tracepb.Status_STATUS_CODE_OK; got != want {
		t.Errorf("status = %s, want %s", got, want)
	}
}

// TestAFailedInvocationsSpanCarriesTheStatusItReturned: the exit status is the
// whole of what avroc reads (#190), so it is the whole of what the span says —
// which is what makes a generator's view of its failure a child of avroc's view
// of the same failure rather than an unrelated record.
func TestAFailedInvocationsSpanCarriesTheStatusItReturned(t *testing.T) {
	collector := newSpanCollector(t)
	c, _ := tracedCLI(t, collector.endpoint(), map[string]string{telemetry.EnvTraceparent: parentHeader},
		"--descriptor", filepath.Join(t.TempDir(), "absent"), "--out", t.TempDir())

	if code := Main(t.Context(), c, testInfo(), echoGenerate); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}

	span := collector.only(t)
	if got, want := intAttr(t, span, attrExitCode), int64(1); got != want {
		t.Errorf("%s = %d, want %d", attrExitCode, got, want)
	}
	if got, want := span.GetStatus().GetCode(), tracepb.Status_STATUS_CODE_ERROR; got != want {
		t.Errorf("status = %s, want %s", got, want)
	}
}

// TestTheSpanIsFlushedBeforeMainReturns is the os.Exit requirement seen from the
// collector's side.
//
// Every cmd/avroc-gen-* calls os.Exit the moment Main returns and os.Exit runs no
// deferred function, so a flush anywhere but inside Main is a flush that never
// happens. Nothing here waits or retries: the span is either at the collector by
// the time Main has returned or the requirement is not met.
func TestTheSpanIsFlushedBeforeMainReturns(t *testing.T) {
	collector := newSpanCollector(t)
	c, _ := tracedCLI(t, collector.endpoint(), nil, "--descriptor", writeDescriptor(t), "--out", t.TempDir())

	if code := Main(t.Context(), c, testInfo(), echoGenerate); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	if names := collector.spanNames(t); len(names) != 1 || names[0] != spanGenerate {
		t.Errorf("the collector holds %q the instant Main returned, want exactly [%q]", names, spanGenerate)
	}
}

// TestAnUntracedInvocationDoesNoTelemetryWork is the default, and the default is
// what almost every invocation gets: no TRACEPARENT, no endpoint, nothing to
// export to. The generator behaves exactly as it did before any of this existed.
//
// "No connection attempted" is asserted through the global tracer provider, which
// internal/telemetry installs only when tracing is on: an untouched global is an
// SDK that was never constructed, and an SDK that was never constructed has no
// exporter, no goroutine and nothing to dial.
func TestAnUntracedInvocationDoesNoTelemetryWork(t *testing.T) {
	invocations := map[string][]string{
		"a generation": {"--descriptor", writeDescriptor(t), "--out", t.TempDir()},
		"a handshake":  {PluginInfoFlag},
	}

	for name, args := range invocations {
		t.Run(name, func(t *testing.T) {
			c, logs := newTestCLI(args...)

			before := otel.GetTracerProvider()
			var stdout bytes.Buffer
			if code := run(t.Context(), c, testInfo(), echoGenerate, &stdout); code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}

			if otel.GetTracerProvider() != before {
				t.Error("an untraced invocation installed a tracer provider, so it built an SDK it had no use for")
			}
			if strings.Contains(logs.String(), "tracing") {
				t.Errorf("an untraced invocation wrote about tracing:\n%s", logs.String())
			}
		})
	}
}

// TestACollectorThatWillNotAnswerDoesNotHoldUpTheExitStatus is the failure mode
// the budget exists for: a generator that blocks on an export at exit is one
// avroc kills mid-export, and the symptom reads as a hung generator rather than
// as a collector that is not there.
//
// The collector accepts the connection and then says nothing, which is the case a
// refused connection does not cover — a refusal fails immediately whatever the
// timeout is, so it would pass this test with no bound at all.
func TestACollectorThatWillNotAnswerDoesNotHoldUpTheExitStatus(t *testing.T) {
	c, logs := tracedCLI(t, blackHoleEndpoint(t), map[string]string{telemetry.EnvTraceparent: parentHeader},
		"--descriptor", writeDescriptor(t), "--out", t.TempDir())

	started := time.Now()
	code := Main(t.Context(), c, testInfo(), echoGenerate)
	elapsed := time.Since(started)

	if code != 0 {
		t.Errorf("exit code = %d, want 0 — a collector that is not there is not a failed generation", code)
	}
	if elapsed >= FlushBudget*2 {
		t.Errorf("the invocation took %s against a flush budget of %s: avroc would have killed it", elapsed, FlushBudget)
	}
	if !strings.Contains(logs.String(), "opentelemetry sdk error") {
		t.Errorf("the export failure was not reported through the logger:\n%s", logs.String())
	}
}

// TestGeneratedBytesAreTheSameTracedOrNot is the determinism half. Tracing is an
// observation of an invocation and never an input to it, so the tree a traced
// generator leaves behind is the tree an untraced one leaves behind, byte for
// byte — and the collector is a decoding one, so a traced run that exported
// nothing cannot pass by having not been traced.
func TestGeneratedBytesAreTheSameTracedOrNot(t *testing.T) {
	collector := newSpanCollector(t)
	descriptor := writeDescriptor(t)

	generate := func(t *testing.T, traced bool) map[string]string {
		t.Helper()

		out := t.TempDir()
		c, _ := newTestCLI("--descriptor", descriptor, "--out", out)
		if traced {
			c, _ = tracedCLI(t, collector.endpoint(), map[string]string{telemetry.EnvTraceparent: parentHeader},
				"--descriptor", descriptor, "--out", out)
		}

		if code := Main(t.Context(), c, testInfo(), echoGenerate); code != 0 {
			t.Fatalf("exit code = %d (traced: %t)", code, traced)
		}
		return treeOf(t, out)
	}

	untraced := generate(t, false)
	traced := generate(t, true)

	if len(untraced) == 0 {
		t.Fatal("the untraced invocation generated nothing, so the comparison is vacuous")
	}
	if len(collector.spanNames(t)) == 0 {
		t.Fatal("the traced invocation exported no spans, so the comparison is vacuous")
	}
	if len(untraced) != len(traced) {
		t.Fatalf("the traced invocation produced %d files and the untraced one %d", len(traced), len(untraced))
	}
	for name, want := range untraced {
		got, ok := traced[name]
		if !ok {
			t.Errorf("the traced invocation did not produce %q", name)
			continue
		}
		if got != want {
			t.Errorf("%q differs between a traced and an untraced invocation:\ntraced:   %q\nuntraced: %q", name, got, want)
		}
	}
}

// TestMalformedTraceContextIsNotAnError: docs/plugin/SPEC.md says a plugin that
// finds no usable trace context starts a trace of its own or starts none, and
// never declines to generate. A value that will not parse is that case.
func TestMalformedTraceContextIsNotAnError(t *testing.T) {
	collector := newSpanCollector(t)
	c, _ := tracedCLI(t, collector.endpoint(), map[string]string{telemetry.EnvTraceparent: "not a traceparent"},
		"--descriptor", writeDescriptor(t), "--out", t.TempDir())

	if code := Main(t.Context(), c, testInfo(), echoGenerate); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	span := collector.only(t)
	if hex.EncodeToString(span.GetTraceId()) == parentTraceID {
		t.Error("a malformed TRACEPARENT was parsed as the trace it does not name")
	}
	if got := hex.EncodeToString(span.GetParentSpanId()); strings.Trim(got, "0") != "" {
		t.Errorf("parent span id = %s, want none: the span should be the root of its own trace", got)
	}
}

// TestAnUntracedPhaseStartsNothingAndEndsNothing is the gate every generator's
// phases go through, asserted where it is written rather than three times over
// (#197).
//
// Two properties, and the second is the one that would be a bug rather than a
// cost. A phase reached on an untraced invocation must start nothing — no tracer
// requested, no span opened — which is what makes the instrumentation free on
// the invocation almost every generator actually gets. And the span it hands
// back must not be the *invocation's*: the caller ends what it is given, so
// returning the parent would end the whole invocation somewhere in the middle of
// the first schema, and every span after it would be an orphan.
func TestAnUntracedPhaseStartsNothingAndEndsNothing(t *testing.T) {
	phases := map[string]func(context.Context) (context.Context, trace.Span){
		spanDescriptorValidate: StartDescriptorValidate,
		spanOptionsParse:       StartOptionsParse,
		spanFingerprint:        StartFingerprint,
		spanSchemaGenerate: func(ctx context.Context) (context.Context, trace.Span) {
			return StartSchemaGenerate(ctx, "event")
		},
		spanFileWrite: func(ctx context.Context) (context.Context, trace.Span) {
			return StartFileWrite(ctx, "event.go")
		},
	}

	for name, start := range phases {
		t.Run(name, func(t *testing.T) {
			counter := &spanCounter{}
			invocation := &untracedSpan{provider: counter}

			ctx, span := start(trace.ContextWithSpan(t.Context(), invocation))
			EndPhase(span, nil)
			EndPhase(span, io.ErrUnexpectedEOF)

			if counter.tracers != 0 || counter.starts != 0 {
				t.Errorf("an untraced %s asked for %d tracers and started %d spans, want 0 and 0",
					name, counter.tracers, counter.starts)
			}
			if invocation.ended {
				t.Errorf("ending an untraced %s ended the invocation's own span", name)
			}
			if trace.SpanFromContext(ctx) != trace.Span(invocation) {
				t.Errorf("an untraced %s replaced the span in the context, so the phase after it would be parented wrongly", name)
			}
		})
	}
}

// spanCounter is a tracer provider that starts no spans and counts every request
// for one, so that "nothing was started" is an assertion rather than an absence.
type spanCounter struct {
	embedded.TracerProvider

	tracers int
	starts  int
}

func (p *spanCounter) Tracer(string, ...trace.TracerOption) trace.Tracer {
	p.tracers++
	return countingTracer{provider: p}
}

type countingTracer struct {
	embedded.Tracer

	provider *spanCounter
}

func (t countingTracer) Start(ctx context.Context, _ string, _ ...trace.SpanStartOption) (context.Context, trace.Span) {
	t.provider.starts++
	return ctx, noop.Span{}
}

// untracedSpan is the span an untraced invocation carries: not recording, which
// is what a no-op provider hands run when there is no TRACEPARENT and no
// endpoint. It differs from noop.Span in naming a provider the test can count
// through and in noticing that it was ended.
type untracedSpan struct {
	noop.Span

	provider *spanCounter
	ended    bool
}

func (s *untracedSpan) TracerProvider() trace.TracerProvider { return s.provider }

func (s *untracedSpan) End(...trace.SpanEndOption) { s.ended = true }

// treeOf reads every regular file beneath dir, keyed by its path relative to it.
func treeOf(t *testing.T, dir string) map[string]string {
	t.Helper()

	tree := make(map[string]string)
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		tree[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("failed to read %s: %v", dir, err)
	}
	return tree
}

// blackHoleEndpoint accepts connections and answers none of them, which is what
// a collector that has stopped responding looks like from here.
func blackHoleEndpoint(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	done := make(chan struct{})
	t.Cleanup(func() { close(done) })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// Held open until the test is over: closing it would let the export
			// fail on a closed connection rather than on the timeout.
			go func() {
				<-done
				_ = conn.Close()
			}()
		}
	}()

	return "http://" + listener.Addr().String()
}

// stringAttr reads one string attribute off a span, failing when it is absent:
// an attribute this package promises to set and did not is the finding, not a
// zero value to compare against.
func stringAttr(t *testing.T, span *tracepb.Span, key string) string {
	t.Helper()

	for _, attr := range span.GetAttributes() {
		if attr.GetKey() == key {
			return attr.GetValue().GetStringValue()
		}
	}
	t.Fatalf("span %q carries no %q attribute", span.GetName(), key)
	return ""
}

// intAttr reads one integer attribute off a span, on the same terms.
func intAttr(t *testing.T, span *tracepb.Span, key string) int64 {
	t.Helper()

	for _, attr := range span.GetAttributes() {
		if attr.GetKey() == key {
			return attr.GetValue().GetIntValue()
		}
	}
	t.Fatalf("span %q carries no %q attribute", span.GetName(), key)
	return 0
}

// resourceSpansField is ExportTraceServiceRequest's one field, which is as much
// of that message as anything here needs to know.
//
// The envelope is peeled by hand rather than by importing the collector's
// generated Go package, for the reason internal/telemetry writes the exporter
// itself: that package carries the OTLP gRPC service with it, and a test that
// pulled grpc-go into the module graph would be a test that broke
// internal/telemetry.TestTheTracingStackCarriesNoGRPCTransport.
const resourceSpansField = 1

// spanCollector is an OTLP/HTTP receiver a whole invocation can be pointed at.
type spanCollector struct {
	server *httptest.Server

	mu     sync.Mutex
	bodies [][]byte
}

func newSpanCollector(t *testing.T) *spanCollector {
	t.Helper()

	c := &spanCollector{}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		c.mu.Lock()
		c.bodies = append(c.bodies, body)
		c.mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(c.server.Close)

	return c
}

// endpoint is what OTEL_EXPORTER_OTLP_ENDPOINT is set to: the base URL, with no
// signal path on it.
func (c *spanCollector) endpoint() string {
	return c.server.URL
}

// only is the one span an invocation exported. One is the whole of what an
// invocation opens, so more than one is as much a finding as none.
func (c *spanCollector) only(t *testing.T) *tracepb.Span {
	t.Helper()

	spans := c.spans(t)
	if len(spans) != 1 {
		t.Fatalf("the collector received %d spans, want exactly 1: %q", len(spans), c.spanNames(t))
	}
	return spans[0]
}

// spanNames is every span the collector was sent, in the order it received them.
func (c *spanCollector) spanNames(t *testing.T) []string {
	t.Helper()

	var names []string
	for _, span := range c.spans(t) {
		names = append(names, span.GetName())
	}
	return names
}

// spans is every span the collector was sent, decoded, in the order it received
// them.
func (c *spanCollector) spans(t *testing.T) []*tracepb.Span {
	t.Helper()

	c.mu.Lock()
	bodies := append([][]byte(nil), c.bodies...)
	c.mu.Unlock()

	var spans []*tracepb.Span
	for _, body := range bodies {
		for len(body) > 0 {
			number, typ, read := protowire.ConsumeTag(body)
			if read < 0 {
				t.Fatalf("export body is not protobuf: %v", protowire.ParseError(read))
			}
			body = body[read:]
			if number != resourceSpansField || typ != protowire.BytesType {
				t.Fatalf("export body carries field %d of type %d, want field %d of type %d",
					number, typ, resourceSpansField, protowire.BytesType)
			}

			encoded, read := protowire.ConsumeBytes(body)
			if read < 0 {
				t.Fatalf("export body is not protobuf: %v", protowire.ParseError(read))
			}
			body = body[read:]

			var rs tracepb.ResourceSpans
			if err := proto.Unmarshal(encoded, &rs); err != nil {
				t.Fatalf("export body does not hold a ResourceSpans: %v", err)
			}
			for _, ss := range rs.GetScopeSpans() {
				spans = append(spans, ss.GetSpans()...)
			}
		}
	}
	return spans
}

// lockedBuffer is a log sink safe against the SDK's own goroutine writing to it
// while the test reads it back.
type lockedBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
