// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package telemetry

import (
	"context"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// TestTracingIsOffByDefaultAndCostsNothing is the first of the three
// requirements: a laptop with no collector near it must not be able to tell that
// avroc has a tracer provider at all.
func TestTracingIsOffByDefaultAndCostsNothing(t *testing.T) {
	offCases := map[string]map[string]string{
		"no environment at all": {},
		"an endpoint but the SDK disabled": {
			envSDKDisabled:  "true",
			envOTLPEndpoint: "http://127.0.0.1:1", // nothing listens here
		},
		"the SDK disabled in any case": {
			envSDKDisabled:  "TRUE",
			envOTLPEndpoint: "http://127.0.0.1:1",
		},
		"everything but an endpoint": {
			envServiceName:   "renamed",
			envResourceAttrs: "deployment.environment=ci",
			envTracesSampler: "always_on",
		},
	}

	for name, env := range offCases {
		t.Run(name, func(t *testing.T) {
			log, sink := newLogger()

			// The global provider is the observable form of "nothing was
			// installed": an off run must leave whatever the process already had.
			before := otel.GetTracerProvider()

			var tracing Provider
			var err error
			streams := captureStandardStreams(t, func() {
				tracing, err = Start(t.Context(), testContext(log, env))
				_, span := tracing.Tracer("avroc/test").Start(t.Context(), "generate")
				span.End()
				if shutdownErr := tracing.Shutdown(t.Context()); shutdownErr != nil {
					t.Errorf("Shutdown of a disabled provider returned %v, want nil", shutdownErr)
				}
			})
			if err != nil {
				t.Fatalf("Start returned %v, want nil", err)
			}

			if tracing.Enabled() {
				t.Error("tracing reports itself enabled with no endpoint configured")
			}
			if after := otel.GetTracerProvider(); after != before {
				t.Error("a disabled run replaced the global tracer provider")
			}
			if streams != "" {
				t.Errorf("a disabled run wrote to a standard stream:\n%s", streams)
			}
			if logged := sink.String(); strings.Contains(logged, "level=WARN") || strings.Contains(logged, "level=ERROR") {
				t.Errorf("a disabled run logged a problem:\n%s", logged)
			}
		})
	}
}

// TestTracingDisabledAttemptsNoConnection is the other half of the same
// requirement, and the half a global-variable check cannot see: with an endpoint
// configured and the SDK disabled, nothing reaches the collector.
func TestTracingDisabledAttemptsNoConnection(t *testing.T) {
	col := newCollector(t)
	log, _ := newLogger()

	tracing, err := Start(t.Context(), testContext(log, map[string]string{
		envSDKDisabled:  "true",
		envOTLPEndpoint: col.endpoint(),
	}))
	if err != nil {
		t.Fatalf("Start returned %v, want nil", err)
	}

	_, span := tracing.Tracer("avroc/test").Start(t.Context(), "generate")
	span.End()
	if err := tracing.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown returned %v, want nil", err)
	}

	if got := col.recorded(); len(got) != 0 {
		t.Errorf("a disabled run made %d request(s) to the collector", len(got))
	}
}

// TestASpanIsExportedAfterTheRunIsCancelled is the third requirement, and the
// one the whole shape of Provider.Shutdown exists for: the run that was
// interrupted is the run whose trace is worth having, and its context is
// cancelled by the time anything gets to the flush.
func TestASpanIsExportedAfterTheRunIsCancelled(t *testing.T) {
	col := newCollector(t)
	log, _ := newLogger()

	// The signal-derived context cmd/avroc builds, and the cancel a Ctrl-C
	// performs.
	ctx, cancel := context.WithCancel(t.Context())

	tracing, err := Start(ctx, testContext(log, map[string]string{
		envOTLPEndpoint: col.endpoint(),
		envServiceName:  "avroc-under-test",
	}))
	if err != nil {
		t.Fatalf("Start returned %v, want nil", err)
	}
	if !tracing.Enabled() {
		t.Fatal("tracing is not enabled with an endpoint configured")
	}

	_, span := tracing.Tracer("avroc/test").Start(ctx, "generate")
	span.End()

	cancel()

	if err := tracing.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown after cancellation returned %v, want nil", err)
	}

	if names := col.spanNames(t); !slices.Contains(names, "generate") {
		t.Fatalf("the span produced before the cancelled exit was not exported; the collector saw %v", names)
	}

	requests := col.recorded()
	if got, want := requests[0].path, tracesPath; got != want {
		t.Errorf("export posted to %q, want %q", got, want)
	}
	if got, want := requests[0].contentType, "application/x-protobuf"; got != want {
		t.Errorf("export content type = %q, want %q", got, want)
	}
}

// TestTheResourceNamesTheServiceAndTheBuild covers the resource every span
// carries.
func TestTheResourceNamesTheServiceAndTheBuild(t *testing.T) {
	t.Run("defaults to avroc and this build's version", func(t *testing.T) {
		col := newCollector(t)
		exportOneSpan(t, col, map[string]string{envOTLPEndpoint: col.endpoint()})

		attrs := exportedResourceAttributes(t, col)
		if got, want := attrs[string(semconv.ServiceNameKey)], DefaultServiceName; got != want {
			t.Errorf("service.name = %q, want %q", got, want)
		}
		if got := attrs[string(semconv.ServiceVersionKey)]; got != buildVersion() {
			t.Errorf("service.version = %q, want %q", got, buildVersion())
		}
	})

	t.Run("takes the service name and the extra attributes from the environment", func(t *testing.T) {
		col := newCollector(t)
		exportOneSpan(t, col, map[string]string{
			envOTLPEndpoint:  col.endpoint(),
			envServiceName:   "avroc-ci",
			envResourceAttrs: "deployment.environment=ci,build.url=https%3A%2F%2Fexample.test%2Fbuild%2F1",
		})

		attrs := exportedResourceAttributes(t, col)
		if got, want := attrs[string(semconv.ServiceNameKey)], "avroc-ci"; got != want {
			t.Errorf("service.name = %q, want %q", got, want)
		}
		if got, want := attrs["deployment.environment"], "ci"; got != want {
			t.Errorf("deployment.environment = %q, want %q", got, want)
		}
		if got, want := attrs["build.url"], "https://example.test/build/1"; got != want {
			t.Errorf("build.url = %q, want %q — the value should be percent-decoded", got, want)
		}
	})
}

// TestConfiguredHeadersTravelWithTheExport covers OTEL_EXPORTER_OTLP_HEADERS,
// which is how a hosted collector is authenticated to.
func TestConfiguredHeadersTravelWithTheExport(t *testing.T) {
	col := newCollector(t)
	exportOneSpan(t, col, map[string]string{
		envOTLPEndpoint: col.endpoint(),
		envOTLPHeaders:  "api-key=secret,x-tenant=avroc",
	})

	requests := col.recorded()
	if len(requests) == 0 {
		t.Fatal("nothing was exported")
	}
	if got, want := requests[0].headers.Get("api-key"), "secret"; got != want {
		t.Errorf("api-key header = %q, want %q", got, want)
	}
	if got, want := requests[0].headers.Get("x-tenant"), "avroc"; got != want {
		t.Errorf("x-tenant header = %q, want %q", got, want)
	}
}

// TestAFailedExportIsALogRecordAndNotOutput is the second requirement. A
// collector that answers with an error is a fact about the operator's
// infrastructure, and it must not appear in whatever is reading avroc's streams.
func TestAFailedExportIsALogRecordAndNotOutput(t *testing.T) {
	col := newCollector(t)
	col.fail(http.StatusInternalServerError)
	log, sink := newLogger()

	streams := captureStandardStreams(t, func() {
		tracing, err := Start(t.Context(), testContext(log, map[string]string{
			envOTLPEndpoint: col.endpoint(),
		}))
		if err != nil {
			t.Fatalf("Start returned %v, want nil", err)
		}

		_, span := tracing.Tracer("avroc/test").Start(t.Context(), "generate")
		span.End()

		if err := tracing.Shutdown(t.Context()); err != nil {
			t.Fatalf("Shutdown returned %v, want nil", err)
		}
	})

	if streams != "" {
		t.Errorf("the SDK's own error reached a standard stream:\n%s", streams)
	}

	logged := sink.String()
	if !strings.Contains(logged, "opentelemetry sdk error") {
		t.Errorf("the failed export was not logged:\n%s", logged)
	}
	if !strings.Contains(logged, "500") {
		t.Errorf("the log record does not say what the collector answered:\n%s", logged)
	}
}

// TestAConfigurationAvrocCannotHonourDisablesTracingRatherThanFailing: the
// provider is always usable, and a misconfiguration is one error for the caller
// to log.
func TestAConfigurationAvrocCannotHonourDisablesTracingRatherThanFailing(t *testing.T) {
	misconfigurations := map[string]map[string]string{
		"a protocol this build does not speak": {
			envOTLPEndpoint: "http://127.0.0.1:4318",
			envOTLPProtocol: "grpc",
		},
		"an endpoint that is not a URL": {
			envOTLPEndpoint: "not a url",
		},
		"a sampler this build does not know": {
			envOTLPEndpoint:  "http://127.0.0.1:4318",
			envTracesSampler: "jaeger_remote",
		},
	}

	for name, env := range misconfigurations {
		t.Run(name, func(t *testing.T) {
			log, _ := newLogger()

			tracing, err := Start(t.Context(), testContext(log, env))
			if err == nil {
				t.Fatal("Start returned no error for a configuration it cannot honour")
			}
			if tracing.Enabled() {
				t.Error("tracing is enabled despite a configuration error")
			}

			// Usable anyway: the caller defers Shutdown before it has looked at
			// the error, and must not have to check.
			_, span := tracing.Tracer("avroc/test").Start(t.Context(), "generate")
			span.End()
			if err := tracing.Shutdown(t.Context()); err != nil {
				t.Errorf("Shutdown returned %v, want nil", err)
			}
		})
	}
}

// TestShutdownIsIdempotentAndBounded covers the two properties Main depends on:
// the deferred flush can run whatever happened, and it cannot hang a build
// behind a collector that stopped answering.
func TestShutdownIsIdempotentAndBounded(t *testing.T) {
	col := newCollector(t)
	log, _ := newLogger()

	tracing, err := Start(t.Context(), testContext(log, map[string]string{envOTLPEndpoint: col.endpoint()}))
	if err != nil {
		t.Fatalf("Start returned %v, want nil", err)
	}

	if err := tracing.Shutdown(t.Context()); err != nil {
		t.Fatalf("the first Shutdown returned %v, want nil", err)
	}
	if err := tracing.Shutdown(t.Context()); err != nil {
		t.Errorf("the second Shutdown returned %v, want nil", err)
	}

	if ShutdownTimeout <= ExportTimeout {
		t.Errorf("ShutdownTimeout (%s) must outlast ExportTimeout (%s), or a flush cannot report the export it began",
			ShutdownTimeout, ExportTimeout)
	}
	if ShutdownTimeout > 30*time.Second {
		t.Errorf("ShutdownTimeout is %s: long enough for a person to notice a build that has finished", ShutdownTimeout)
	}
}

// exportOneSpan starts a provider from env, produces a span through it, and
// flushes.
func exportOneSpan(t *testing.T, col *collector, env map[string]string) {
	t.Helper()

	log, _ := newLogger()
	tracing, err := Start(t.Context(), testContext(log, env))
	if err != nil {
		t.Fatalf("Start returned %v, want nil", err)
	}
	if !tracing.Enabled() {
		t.Fatal("tracing is not enabled")
	}

	_, span := tracing.Tracer("avroc/test").Start(t.Context(), "generate")
	span.SetAttributes(attribute.String("generator", "go"))
	span.End()

	if err := tracing.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown returned %v, want nil", err)
	}
	if len(col.recorded()) == 0 {
		t.Fatal("nothing was exported")
	}
}

// exportedResourceAttributes reads the resource off the first export.
func exportedResourceAttributes(t *testing.T, col *collector) map[string]string {
	t.Helper()

	spans := col.resourceSpans(t)
	if len(spans) == 0 {
		t.Fatal("no ResourceSpans were exported")
	}

	attrs := make(map[string]string)
	for _, kv := range spans[0].GetResource().GetAttributes() {
		attrs[kv.GetKey()] = kv.GetValue().GetStringValue()
	}
	return attrs
}

// captureStandardStreams runs f with both standard streams redirected and
// returns everything written to either of them.
//
// The assertion it serves is "nothing extra is written to either standard
// stream", which is a property of the process and not of an injected writer —
// the default OpenTelemetry error handler writes to os.Stderr directly, and an
// injected writer would not see it.
func captureStandardStreams(t *testing.T, f func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create a pipe: %v", err)
	}

	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w

	func() {
		defer func() {
			os.Stdout, os.Stderr = stdout, stderr
			_ = w.Close()
		}()
		f()
	}()

	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("failed to read the captured streams: %v", err)
	}
	_ = r.Close()

	return string(captured)
}
