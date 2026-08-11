// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package telemetry

import (
	"testing"
	"time"

	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// TestTheCallersDefaultServiceNameIsADefaultAndNotAnOverride: a generator names
// itself, because it is a different service from the avroc that forked it — and
// an operator who set OTEL_SERVICE_NAME has named the whole build, which
// docs/plugin/SPEC.md says avroc does not get to contradict.
func TestTheCallersDefaultServiceNameIsADefaultAndNotAnOverride(t *testing.T) {
	t.Run("it is used when the environment says nothing", func(t *testing.T) {
		cfg, err := configFromEnv(
			environ(map[string]string{envOTLPEndpoint: "http://localhost:4318"}),
			"v0.0.0",
			WithDefaultServiceName("avroc-gen-go"),
		)
		if err != nil {
			t.Fatalf("configFromEnv returned %v, want nil", err)
		}
		if got, want := cfg.serviceName, "avroc-gen-go"; got != want {
			t.Errorf("serviceName = %q, want %q", got, want)
		}
	})

	t.Run("the environment wins over it", func(t *testing.T) {
		cfg, err := configFromEnv(
			environ(map[string]string{
				envOTLPEndpoint: "http://localhost:4318",
				envServiceName:  "the-whole-build",
			}),
			"v0.0.0",
			WithDefaultServiceName("avroc-gen-go"),
		)
		if err != nil {
			t.Fatalf("configFromEnv returned %v, want nil", err)
		}
		if got, want := cfg.serviceName, "the-whole-build"; got != want {
			t.Errorf("serviceName = %q, want %q", got, want)
		}
		if got, want := resourceAttributes(cfg)[string(semconv.ServiceNameKey)], "the-whole-build"; got != want {
			t.Errorf("the resource says service.name = %q, want %q", got, want)
		}
	})

	t.Run("an empty name is not a name", func(t *testing.T) {
		cfg, err := configFromEnv(
			environ(map[string]string{envOTLPEndpoint: "http://localhost:4318"}),
			"v0.0.0",
			WithDefaultServiceName(""),
		)
		if err != nil {
			t.Fatalf("configFromEnv returned %v, want nil", err)
		}
		if got, want := cfg.serviceName, DefaultServiceName; got != want {
			t.Errorf("serviceName = %q, want %q", got, want)
		}
	})
}

// TestTheFlushBudgetBoundsBothWaits is the property internal/plugin depends on:
// one budget, and one export request strictly inside it, so that a request begun
// as the flush begins can fail and be reported rather than being cut off.
func TestTheFlushBudgetBoundsBothWaits(t *testing.T) {
	const budget = 2 * time.Second

	cfg, err := configFromEnv(
		environ(map[string]string{envOTLPEndpoint: "http://localhost:4318"}),
		"v0.0.0",
		WithFlushBudget(budget),
	)
	if err != nil {
		t.Fatalf("configFromEnv returned %v, want nil", err)
	}

	if got, want := cfg.shutdownTimeout, budget; got != want {
		t.Errorf("shutdownTimeout = %s, want %s", got, want)
	}
	if cfg.exportTimeout >= cfg.shutdownTimeout {
		t.Errorf("exportTimeout (%s) must be strictly inside shutdownTimeout (%s)",
			cfg.exportTimeout, cfg.shutdownTimeout)
	}
	if cfg.exportTimeout <= 0 {
		t.Errorf("exportTimeout = %s, which is no timeout at all in net/http", cfg.exportTimeout)
	}

	// A budget of nothing is a mistake to ignore rather than a way to switch the
	// bounds off: an exporter with no timeout is the one that hangs a build.
	unbudgeted, err := configFromEnv(
		environ(map[string]string{envOTLPEndpoint: "http://localhost:4318"}),
		"v0.0.0",
		WithFlushBudget(0),
	)
	if err != nil {
		t.Fatalf("configFromEnv returned %v, want nil", err)
	}
	if got, want := unbudgeted.shutdownTimeout, ShutdownTimeout; got != want {
		t.Errorf("shutdownTimeout = %s for a zero budget, want the default %s", got, want)
	}
	if got, want := unbudgeted.exportTimeout, ExportTimeout; got != want {
		t.Errorf("exportTimeout = %s for a zero budget, want the default %s", got, want)
	}

	// A budget too small to halve is nobody's intention and is still not allowed
	// to invert the invariant: zero reaching the exporter is not "no time" but
	// "no timeout", and it would be answered with a bound longer than the whole
	// budget.
	tiny, err := configFromEnv(
		environ(map[string]string{envOTLPEndpoint: "http://localhost:4318"}),
		"v0.0.0",
		WithFlushBudget(time.Nanosecond),
	)
	if err != nil {
		t.Fatalf("configFromEnv returned %v, want nil", err)
	}
	if tiny.exportTimeout <= 0 {
		t.Errorf("exportTimeout = %s for a %s budget, which net/http reads as no timeout at all",
			tiny.exportTimeout, time.Nanosecond)
	}
	if tiny.exportTimeout > tiny.shutdownTimeout {
		t.Errorf("exportTimeout (%s) outlasts the whole flush budget (%s)", tiny.exportTimeout, tiny.shutdownTimeout)
	}
}

// TestShutdownHonoursTheBudgetItWasGiven: the bound is on the Provider, so a
// flush is bounded by what the caller asked for and not by the package default.
func TestShutdownHonoursTheBudgetItWasGiven(t *testing.T) {
	col := newCollector(t)
	log, _ := newLogger()

	tracing, err := Start(t.Context(), testContext(log, map[string]string{envOTLPEndpoint: col.endpoint()}),
		WithFlushBudget(time.Second))
	if err != nil {
		t.Fatalf("Start returned %v, want nil", err)
	}
	if got, want := tracing.shutdownTimeout, time.Second; got != want {
		t.Errorf("shutdownTimeout = %s, want %s", got, want)
	}
	if err := tracing.Shutdown(t.Context()); err != nil {
		t.Errorf("Shutdown returned %v, want nil", err)
	}
}

// TestExtractIsTheReadingHalfOfTheCarrier covers every case a generator meets,
// three of which are "there is no parent" wearing different clothes.
//
// docs/plugin/SPEC.md's "Trace context" is normative for all of them: absence is
// ordinary, and a process that finds nothing usable starts a trace of its own or
// starts none at all. There is no error to report anywhere here.
func TestExtractIsTheReadingHalfOfTheCarrier(t *testing.T) {
	const (
		traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
		spanID  = "00f067aa0ba902b7"
		header  = "00-" + traceID + "-" + spanID + "-01"
	)

	t.Run("a traceparent avroc wrote", func(t *testing.T) {
		ctx := Extract(t.Context(), environ(map[string]string{
			EnvTraceparent: header,
			EnvTracestate:  "vendor=value",
		}))

		sc := trace.SpanContextFromContext(ctx)
		if !sc.IsValid() {
			t.Fatal("no span context was extracted from a TRACEPARENT avroc would have written")
		}
		if got := sc.TraceID().String(); got != traceID {
			t.Errorf("trace id = %s, want %s", got, traceID)
		}
		if got := sc.SpanID().String(); got != spanID {
			t.Errorf("span id = %s, want %s", got, spanID)
		}
		if !sc.IsRemote() {
			t.Error("the extracted span context is not remote, so a span opened from it would not be a child")
		}
		if got, want := sc.TraceState().Get("vendor"), "value"; got != want {
			t.Errorf("tracestate vendor = %q, want %q", got, want)
		}
	})

	for name, env := range map[string]map[string]string{
		"no trace context at all": {},
		"a value that will not parse": {
			EnvTraceparent: "not a traceparent",
		},
		"a tracestate with no traceparent": {
			EnvTracestate: "vendor=value",
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := Extract(t.Context(), environ(env))
			if trace.SpanContextFromContext(ctx).IsValid() {
				t.Error("a span context was extracted where there was none to extract")
			}
		})
	}

	t.Run("no environment at all", func(t *testing.T) {
		if trace.SpanContextFromContext(Extract(t.Context(), nil)).IsValid() {
			t.Error("a span context was extracted from a nil environment")
		}
	})
}

// resourceAttributes reads a configuration's resource back as a map, so an assertion
// can name the one attribute it is about.
func resourceAttributes(cfg config) map[string]string {
	attrs := make(map[string]string)
	for _, attr := range cfg.resource.Attributes() {
		attrs[string(attr.Key)] = attr.Value.AsString()
	}
	return attrs
}
