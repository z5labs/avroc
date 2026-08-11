// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/z5labs/avroc/internal/cli"
	"github.com/z5labs/avroc/internal/ir"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// noopTracer is what a test that is not about tracing hands runGenerate: the
// same thing an untraced run gets, which is the point — every test in this
// package other than these ones exercises the code path a laptop with no
// collector near it takes.
func noopTracer() oteltrace.Tracer {
	return noop.NewTracerProvider().Tracer(tracerScope)
}

// recordingTracer is a real SDK tracer whose spans a test reads back once they
// have ended.
//
// The SDK rather than a fake, because the thing being asserted on is what a
// collector would receive: the parentage, the attributes, the events and the
// status are all things the SDK computes, and a fake tracer would assert that
// the code called itself.
func recordingTracer() (oteltrace.Tracer, *tracetest.SpanRecorder) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	return provider.Tracer(tracerScope), recorder
}

// rootedContext is a context carrying a run's root span, which is what a phase
// called on its own would otherwise be without.
//
// startSpan takes its provider from the span already in the context, so a phase
// reached with no root span above it traces into a no-op — deliberately, and
// that is what every other test in this package relies on. A test about the
// spans has to supply the root the command would have started.
func rootedContext(t *testing.T) (context.Context, *tracetest.SpanRecorder) {
	t.Helper()

	tracer, recorder := recordingTracer()
	ctx, span := tracer.Start(t.Context(), spanRun)
	t.Cleanup(func() { span.End() })
	return ctx, recorder
}

// spansNamed is every recorded span of that name.
func spansNamed(spans []sdktrace.ReadOnlySpan, name string) []sdktrace.ReadOnlySpan {
	var found []sdktrace.ReadOnlySpan
	for _, s := range spans {
		if s.Name() == name {
			found = append(found, s)
		}
	}
	return found
}

// onlySpanNamed is the one recorded span of that name, and fails when a run
// produced none or several.
func onlySpanNamed(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()

	found := spansNamed(spans, name)
	if len(found) != 1 {
		t.Fatalf("the run produced %d %q spans, want 1: %v", len(found), name, recordedNames(spans))
	}
	return found[0]
}

// recordedNames is every span a run ended, for a failure message that shows
// what was there instead.
func recordedNames(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, 0, len(spans))
	for _, s := range spans {
		names = append(names, s.Name())
	}
	return names
}

// parentName names the span that started this one, so a failure reads like the
// tree it is about rather than like two span ids.
func parentName(spans []sdktrace.ReadOnlySpan, span sdktrace.ReadOnlySpan) string {
	for _, s := range spans {
		if s.SpanContext().SpanID() == span.Parent().SpanID() {
			return s.Name()
		}
	}
	return "(nothing recorded)"
}

// attrOf reads one attribute off a span.
func attrOf(span sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

// stringAttr is attrOf for the usual case, reported rather than returned.
func stringAttr(t *testing.T, span sdktrace.ReadOnlySpan, key string) string {
	t.Helper()

	value, ok := attrOf(span, key)
	if !ok {
		t.Fatalf("span %q carries no %q attribute: %v", span.Name(), key, span.Attributes())
	}
	return value.AsString()
}

// tracedProject is a project a generator produces one file from: the manifest,
// the schema, and a conforming plugin on PATH.
func tracedProject(t *testing.T, env map[string]string, args ...string) (cli.Context, string) {
	t.Helper()

	projectRoot := t.TempDir()
	generatorPath := nameFromDescriptorGenerator(t)

	if err := os.WriteFile(filepath.Join(projectRoot, manifestFilename), []byte(`{
  "inputs": ["schema.avdl"],
  "generators": [{"name": "test", "out": "gen"}]
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "schema.avdl"), []byte(recordIDL("User")), 0o644); err != nil {
		t.Fatal(err)
	}

	if env == nil {
		env = make(map[string]string)
	}
	env["PATH"] = filepath.Dir(generatorPath)

	return cli.Context{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Env: cli.EnvironmentFunc(func(key string) (string, bool) {
			value, ok := env[key]
			return value, ok
		}),
		OpenDir:    func(dir string) fs.FS { return os.DirFS(dir) },
		WorkingDir: projectRoot,
		Args:       args,
	}, projectRoot
}

// TestEveryPhaseOfARunIsASpan is the story's first requirement: a run is a
// trace, and the phases it is made of are the ones that already exist as
// functions — nothing invented for the trace, and nothing left out of it.
//
// The parentage is asserted as well as the names, because a flat list of spans
// with the right names is not a trace: it is what you get when a phase starts
// its span from the wrong context, and a backend renders it as eight unrelated
// operations.
func TestEveryPhaseOfARunIsASpan(t *testing.T) {
	tracer, recorder := recordingTracer()
	c, _ := tracedProject(t, nil, "generate")

	if code := runGenerate(t.Context(), c, tracer); code != 0 {
		t.Fatalf("avroc generate exited %d", code)
	}

	spans := recorder.Ended()
	root := onlySpanNamed(t, spans, spanRun)

	// Every phase the story names, once, directly under the run.
	for _, name := range []string{
		spanManifestLoad,
		spanIDLParse,
		spanIRResolve,
		spanHandshake,
		spanGeneratorRun,
		spanMerge,
		spanPrune,
		spanRecord,
	} {
		if got := parentName(spans, onlySpanNamed(t, spans, name)); got != spanRun {
			t.Errorf("span %q hangs off %q, want %q", name, got, spanRun)
		}
	}

	// The one that is a phase within a phase: the handshake is sequential over
	// the generators, and each generator's is inside it.
	if got := parentName(spans, onlySpanNamed(t, spans, spanGeneratorHandshake)); got != spanHandshake {
		t.Errorf("span %q hangs off %q, want %q", spanGeneratorHandshake, got, spanHandshake)
	}

	// One run is one trace, and a run that worked says so.
	for _, s := range spans {
		if s.SpanContext().TraceID() != root.SpanContext().TraceID() {
			t.Errorf("span %q is in a different trace from the run", s.Name())
		}
		if s.Status().Code == codes.Error {
			t.Errorf("span %q is marked failed on a run that succeeded: %q", s.Name(), s.Status().Description)
		}
	}
}

// TestTheHandshakeAndTheInvocationNameTheirGenerator: the two things avroc does
// per generator are separately visible, and each says which generator it was.
//
// The generator is an attribute rather than part of the span name so that "how
// long does the handshake take" is a question a backend can answer across every
// generator without parsing a string.
func TestTheHandshakeAndTheInvocationNameTheirGenerator(t *testing.T) {
	tracer, recorder := recordingTracer()
	c, _ := tracedProject(t, nil, "generate")

	if code := runGenerate(t.Context(), c, tracer); code != 0 {
		t.Fatalf("avroc generate exited %d", code)
	}

	spans := recorder.Ended()
	for _, name := range []string{spanGeneratorHandshake, spanGeneratorRun} {
		span := onlySpanNamed(t, spans, name)
		if got, want := stringAttr(t, span, attrGenerator), "avroc-gen-test"; got != want {
			t.Errorf("span %q names generator %q, want %q", name, got, want)
		}
	}
}

// TestAFailedGeneratorIsClassifiedOnItsSpan is docs/plugin/SPEC.md's three
// failures on the invocation's span, and it is the same classification
// reportFailure already made for the log rather than a fourth description of the
// distinction.
//
// Nothing is concluded from the value of a non-zero code: it is carried, and
// that is all.
func TestAFailedGeneratorIsClassifiedOnItsSpan(t *testing.T) {
	t.Run("a generator that exited non-zero carries its exit code", func(t *testing.T) {
		ctx, recorder := rootedContext(t)
		g := testGenerator(t, writeShellGenerator(t, "exit 3\n"))

		projectRoot, output := newProject(t)
		if err := generateOne(ctx, g, projectRoot, output, nil, testSchema("User")); err == nil {
			t.Fatal("generate accepted a generator that exited non-zero")
		}

		span := onlySpanNamed(t, recorder.Ended(), spanGeneratorRun)
		if span.Status().Code != codes.Error {
			t.Errorf("span status = %v, want %v", span.Status().Code, codes.Error)
		}
		code, ok := attrOf(span, attrExitCode)
		if !ok {
			t.Fatalf("the span carries no %q: %v", attrExitCode, span.Attributes())
		}
		if code.AsInt64() != 3 {
			t.Errorf("%s = %d, want 3", attrExitCode, code.AsInt64())
		}
		if _, ok := attrOf(span, attrSignal); ok {
			t.Error("a generator that exited was described as one that was signalled")
		}
	})

	t.Run("a generator terminated by a signal carries the signal", func(t *testing.T) {
		ctx, recorder := rootedContext(t)
		g := testGenerator(t, writeShellGenerator(t, "kill -TERM $$\n"))

		projectRoot, output := newProject(t)
		if err := generateOne(ctx, g, projectRoot, output, nil, testSchema("User")); err == nil {
			t.Fatal("generate accepted a generator killed by a signal")
		}

		span := onlySpanNamed(t, recorder.Ended(), spanGeneratorRun)
		if span.Status().Code != codes.Error {
			t.Errorf("span status = %v, want %v", span.Status().Code, codes.Error)
		}
		if got, want := stringAttr(t, span, attrSignal), syscall.SIGTERM.String(); got != want {
			t.Errorf("%s = %q, want %q", attrSignal, got, want)
		}
		number, ok := attrOf(span, attrSignalNumber)
		if !ok {
			t.Fatalf("the span carries no %q: %v", attrSignalNumber, span.Attributes())
		}
		if number.AsInt64() != int64(syscall.SIGTERM) {
			t.Errorf("%s = %d, want %d", attrSignalNumber, number.AsInt64(), int64(syscall.SIGTERM))
		}
		if _, ok := attrOf(span, attrExitCode); ok {
			t.Error("a generator killed by a signal was described with an exit code")
		}
	})

	t.Run("a generator that never ran carries neither", func(t *testing.T) {
		ctx, recorder := rootedContext(t)
		executablePath := writeShellGenerator(t, "exit 0\n")
		if err := os.Chmod(executablePath, 0o644); err != nil {
			t.Fatal(err)
		}
		g := testGenerator(t, executablePath)

		projectRoot, output := newProject(t)
		if err := generateOne(ctx, g, projectRoot, output, nil, testSchema("User")); err == nil {
			t.Fatal("generate accepted a generator that never ran")
		}

		span := onlySpanNamed(t, recorder.Ended(), spanGeneratorRun)
		if span.Status().Code != codes.Error {
			t.Errorf("span status = %v, want %v", span.Status().Code, codes.Error)
		}
		for _, key := range []string{attrExitCode, attrSignal, attrSignalNumber} {
			if _, ok := attrOf(span, key); ok {
				t.Errorf("a generator that never ran was described with %q: there was no process to produce one", key)
			}
		}
	})
}

// TestACollisionIsAnEventOnTheMergeSpan: two generators producing one path is a
// fact about the whole set of plans, so it is recorded where the whole set is
// decided rather than on either generator's span.
//
// The events and the refusal are two renderings of one value, and this is what
// requires them not to disagree: the same paths, in the same order, each naming
// the same generators.
func TestACollisionIsAnEventOnTheMergeSpan(t *testing.T) {
	ctx, recorder := rootedContext(t)
	output := t.TempDir()

	json := scratchPlan(t, output, "avroc-gen-json", map[string]string{
		"order.avsc": `{"produced_by":"json"}`,
		"user.avsc":  `{"produced_by":"json"}`,
	})
	pcf := scratchPlan(t, output, "avroc-gen-pcf", map[string]string{
		"order.avsc": `{"produced_by":"pcf"}`,
		"user.avsc":  `{"produced_by":"pcf"}`,
	})

	err := mergeOutputs(ctx, output, []*generatorOutput{json, pcf})
	if err == nil {
		t.Fatal("the merge accepted two generators producing the same files")
	}

	span := onlySpanNamed(t, recorder.Ended(), spanMerge)
	if span.Status().Code != codes.Error {
		t.Errorf("the merge span is not marked failed: %v", span.Status())
	}

	var paths []string
	for _, event := range span.Events() {
		if event.Name != eventCollision {
			continue
		}

		var path string
		var claimants []string
		for _, kv := range event.Attributes {
			switch string(kv.Key) {
			case attrPath:
				path = kv.Value.AsString()
			case attrGenerators:
				claimants = kv.Value.AsStringSlice()
			}
		}
		paths = append(paths, path)

		if want := []string{"avroc-gen-json", "avroc-gen-pcf"}; !slices.Equal(claimants, want) {
			t.Errorf("the event for %q names %q, want %q", path, claimants, want)
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("the event names %q, which the refusal does not: %v", path, err)
		}
	}

	// Sorted by path, and the same order the refusal reports: one value renders
	// both, so a trace read beside the message a user was given agrees with it.
	want := []string{filepath.Join(output, "order.avsc"), filepath.Join(output, "user.avsc")}
	if !slices.Equal(paths, want) {
		t.Errorf("the merge span records %q, want %q", paths, want)
	}
	if strings.Index(err.Error(), want[0]) > strings.Index(err.Error(), want[1]) {
		t.Errorf("the refusal reports the collisions in a different order from the span: %v", err)
	}
}

// TestACancelledRunKeepsTheSpansItGotThrough is the trace of the run somebody
// pressed Ctrl-C on, which is the run whose trace is most worth having and the
// one whose context is already cancelled by the time anything is written.
//
// Three things are required of it. The phases that finished are in it, so the
// question "where had it got to" has an answer at all; the phase that did not
// finish is marked cancelled rather than broken, because a generator killed by
// the kill exec.CommandContext sends is not a generator that went wrong; and the
// phases the run never reached are not in it, invented from work that did not
// happen.
func TestACancelledRunKeepsTheSpansItGotThrough(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	projectRoot := t.TempDir()
	started := filepath.Join(t.TempDir(), "started")

	// It answers the handshake at once and then blocks, so every phase before the
	// invocation completes and the invocation is the one the cancellation lands
	// on. exec, so that the process the cancellation kills is the one that sleeps:
	// a shell that forked it would leave it orphaned holding the inherited
	// streams.
	generatorPath := writeNamedShellGenerator(t, "slow", fmt.Sprintf(`if [ "$1" = "--plugin-info" ]; then
  printf '{"name":"slow","version":"9.9.9","ir_version":%d,"options":[]}\n'
  exit 0
fi
: > '%s'
exec sleep 300
`, ir.Version, started))

	if err := os.WriteFile(filepath.Join(projectRoot, manifestFilename), []byte(`{
  "inputs": ["schema.avdl"],
  "generators": [{"name": "slow", "out": "gen"}]
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "schema.avdl"), []byte(recordIDL("User")), 0o644); err != nil {
		t.Fatal(err)
	}

	c := cli.Context{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Env: cli.EnvironmentFunc(func(key string) (string, bool) {
			if key == "PATH" {
				return filepath.Dir(generatorPath), true
			}
			return "", false
		}),
		OpenDir:    func(dir string) fs.FS { return os.DirFS(dir) },
		WorkingDir: projectRoot,
		Args:       []string{"generate"},
	}

	tracer, recorder := recordingTracer()
	done := make(chan int, 1)
	go func() {
		done <- runGenerate(ctx, c, tracer)
	}()

	// Cancelled only once the child is definitely running, so this is about a
	// cancellation reaching a live process rather than a context that was already
	// done before the fork.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the generator never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case code := <-done:
		if code == 0 {
			t.Fatal("a cancelled run reported success")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the run did not return after its context was cancelled")
	}

	spans := recorder.Ended()

	// Everything it got through is in the trace, and none of it is described as
	// having failed: they did not.
	for _, name := range []string{spanManifestLoad, spanIDLParse, spanIRResolve, spanHandshake, spanGeneratorHandshake} {
		span := onlySpanNamed(t, spans, name)
		if span.Status().Code == codes.Error {
			t.Errorf("phase %q completed before the cancellation but is marked failed: %v", name, span.Status())
		}
	}

	// The one it was in the middle of, and the run itself.
	for _, name := range []string{spanGeneratorRun, spanRun} {
		span := onlySpanNamed(t, spans, name)
		if span.Status().Code != codes.Error {
			t.Errorf("span %q is not marked failed on a cancelled run: %v", name, span.Status())
		}
		if got := span.Status().Description; got != statusCancelled {
			t.Errorf("span %q is described as %q, want %q", name, got, statusCancelled)
		}
	}

	// And the phases it never reached are not in it.
	for _, name := range []string{spanMerge, spanPrune, spanRecord} {
		if len(spansNamed(spans, name)) != 0 {
			t.Errorf("the cancelled run recorded a %q span for a phase it never reached", name)
		}
	}
}

// TestInstrumentationDidNotChangeTheIteration is the story's other half: the
// merge, the record and the collision report are the manifest's order, and a
// span per generator must not become the reason any of them follows the order
// the generators finished in.
//
// The generators finish in the reverse of the manifest's order, so an
// implementation that iterated over completions rather than over tasks would
// produce the reverse of every assertion here — and unlike the same check on an
// untraced run, this one is running the instrumentation while it does it.
func TestInstrumentationDidNotChangeTheIteration(t *testing.T) {
	ctx, recorder := rootedContext(t)

	names := []string{"a", "b", "c", "d"}
	pauses := []string{"0.4", "0.3", "0.2", "0.1"}

	log, records := recordingLogger()
	projectRoot, output := newProject(t)
	events := filepath.Join(t.TempDir(), "trace")

	tasks := make([]genTask, 0, len(names))
	for i, name := range names {
		tasks = append(tasks, tracedTask(t, name, output, events, pauses[i]))
	}
	if err := generateAll(ctx, log, projectRoot, tasks); err != nil {
		t.Fatal(err)
	}

	// The premise: they really did finish in some order other than the manifest's,
	// so nothing below is vacuously true of a run in which the two coincided.
	want := []string{"avroc-gen-a", "avroc-gen-b", "avroc-gen-c", "avroc-gen-d"}
	var finished []string
	for _, e := range readTrace(t, events) {
		if e.event == "end" {
			finished = append(finished, e.generator)
		}
	}
	if slices.Equal(finished, want) {
		t.Skipf("the generators finished in the manifest's order %q, so this run proves nothing", finished)
	}

	if got := generatorsReported(records()); !slices.Equal(got, want) {
		t.Errorf("the run reported %q, want the manifest's %q", got, want)
	}

	// The record is the manifest's too, and every generator is in it.
	record := readRecord(t, projectRoot)
	for _, name := range names {
		if !strings.Contains(record, name+".txt") {
			t.Errorf("the record does not name %q:\n%s", name+".txt", record)
		}
	}

	// And every generator got a span, whichever order they finished in.
	invocations := spansNamed(recorder.Ended(), spanGeneratorRun)
	if len(invocations) != len(names) {
		t.Fatalf("the run recorded %d invocation spans, want %d", len(invocations), len(names))
	}
	var traced []string
	for _, span := range invocations {
		traced = append(traced, stringAttr(t, span, attrGenerator))
	}
	slices.Sort(traced)
	if !slices.Equal(traced, want) {
		t.Errorf("the invocation spans name %q, want %q", traced, want)
	}
}

// TestOnlyGenerateIsTraced is the last of the story's requirements, and it is
// asserted through Main over a real exporter rather than through a tracer a test
// injected: the question is what a collector receives from each of avroc's
// commands, and only the command deciding whether to trace at all can answer it.
//
// A run is worth a trace — it forks processes, walks a project tree and takes
// seconds. init writes the one file it was asked for and inspect renders the one
// it was handed, and a span apiece would be an export, a connection and a flush
// bought for nothing.
func TestOnlyGenerateIsTraced(t *testing.T) {
	t.Run("generate exports the run", func(t *testing.T) {
		collector := newSpanCollector(t)
		c, _ := tracedProject(t, map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": collector.endpoint()}, "generate")

		if code := Main(t.Context(), c); code != 0 {
			t.Fatalf("avroc generate exited %d", code)
		}

		names := collector.spanNames(t)
		if !slices.Contains(names, spanRun) {
			t.Errorf("the collector received %q, which does not include the run itself", names)
		}
	})

	t.Run("init exports nothing", func(t *testing.T) {
		collector := newSpanCollector(t)
		c := commandContext(t, collector, t.TempDir(), "init")

		if code := Main(t.Context(), c); code != 0 {
			t.Fatalf("avroc init exited %d", code)
		}

		if names := collector.spanNames(t); len(names) != 0 {
			t.Errorf("avroc init exported %q", names)
		}
	})

	t.Run("inspect exports nothing", func(t *testing.T) {
		descriptor, _ := writeInspectFixture(t)

		collector := newSpanCollector(t)
		c := commandContext(t, collector, t.TempDir(), "inspect", descriptor)

		if code := Main(t.Context(), c); code != 0 {
			t.Fatalf("avroc inspect exited %d", code)
		}

		if names := collector.spanNames(t); len(names) != 0 {
			t.Errorf("avroc inspect exported %q", names)
		}
	})
}

// commandContext is one avroc command with tracing configured and nothing else
// in its environment.
func commandContext(t *testing.T, collector *spanCollector, workingDir string, args ...string) cli.Context {
	t.Helper()

	env := map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": collector.endpoint()}

	return cli.Context{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Env: cli.EnvironmentFunc(func(key string) (string, bool) {
			value, ok := env[key]
			return value, ok
		}),
		OpenDir:    func(dir string) fs.FS { return os.DirFS(dir) },
		WorkingDir: workingDir,
		Args:       args,
	}
}

// resourceSpansField is ExportTraceServiceRequest's one field, which is as much
// of that message as anything here needs to know.
//
// The envelope is peeled by hand rather than by importing the collector's
// generated Go package, for the reason internal/telemetry writes the exporter
// itself: that package carries the OTLP gRPC service with it, and a test that
// pulled grpc-go into the module graph would be a test that broke
// TestTheTracingStackCarriesNoGRPCTransport.
const resourceSpansField = 1

// spanCollector is an OTLP/HTTP receiver a whole command can be pointed at.
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

// spanNames is every span the collector was sent, in the order it received them.
func (c *spanCollector) spanNames(t *testing.T) []string {
	t.Helper()

	c.mu.Lock()
	bodies := append([][]byte(nil), c.bodies...)
	c.mu.Unlock()

	var names []string
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
				for _, span := range ss.GetSpans() {
					names = append(names, span.GetName())
				}
			}
		}
	}
	return names
}
