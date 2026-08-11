// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// Package telemetry is avroc's tracer provider and nothing that uses it.
//
// It is configured from the OpenTelemetry environment variable specification —
// OTEL_SDK_DISABLED, OTEL_SERVICE_NAME, OTEL_RESOURCE_ATTRIBUTES,
// OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_EXPORTER_OTLP_PROTOCOL,
// OTEL_EXPORTER_OTLP_HEADERS and OTEL_TRACES_SAMPLER (with its
// OTEL_TRACES_SAMPLER_ARG) — because those are the names an operator already
// knows and already sets, and because docs/plugin/SPEC.md's "The environment"
// passes avroc's environment to a generator unchanged, so the same variables
// reach a generator with no further work.
//
// Every one of them is read through [cli.Context]'s [cli.Environment] rather
// than through os. The SDK will read the process environment itself if it is
// given the chance, and a configuration that comes half from an injected
// environment and half from the real one is one no test can describe.
//
// Three properties are the whole of the design.
//
// # Off is the default, and off costs nothing
//
// With no OTEL_EXPORTER_OTLP_ENDPOINT, or with OTEL_SDK_DISABLED=true, [Start]
// constructs no exporter, starts no goroutine, attempts no connection, writes
// nothing to either standard stream, and does not touch a single one of
// OpenTelemetry's global variables. avroc is a build tool that mostly runs on a
// laptop with no collector anywhere near it, and a code generator that prints
// export failures during `go generate` is worse than one with no tracing at all.
//
// A misconfiguration is treated the same way: an endpoint that is not a URL, a
// protocol this build cannot speak, a sampler it does not know. Each one is a
// single warning through [cli.Context.Log] and a run that goes untraced. avroc
// does not guess at a telemetry configuration it cannot read, and it does not
// fail a build over one either — the tracing is the thing that goes away.
//
// # A failed export must not become output
//
// The SDK reports its own errors through [otel.SetErrorHandler], whose default
// writes to standard error. [Start] replaces it with one logging through
// [cli.Context.Log], so a broken collector is a log record rather than a line in
// whatever is reading avroc's streams.
//
// # The flush is the part that gets skipped
//
// cmd/avroc calls os.Exit the moment avroc.Main returns, and os.Exit does not
// run deferred functions. So [Provider.Shutdown] is reached from inside Main, on
// every path including the failing ones, and it derives its own context with
// [context.WithoutCancel] and its own timeout: the signal-derived context is
// cancelled exactly when the trace is most worth having, which is when somebody
// pressed Ctrl-C.
package telemetry

import (
	"context"
	"log/slog"
	"runtime/debug"

	"github.com/z5labs/avroc/internal/cli"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Provider is the tracer provider a run traces through, and the shutdown that
// flushes it.
//
// It is a value rather than an interface, and [Start] never returns a nil one,
// because every caller is on the path that has to shut it down — a caller that
// has to check before deferring is a caller that will one day not check.
type Provider struct {
	trace.TracerProvider

	// shutdown is nil when tracing is off, which is the whole representation of
	// "there is nothing to flush": no exporter was constructed, so there is no
	// batch of spans in memory and no connection to close.
	shutdown func(context.Context) error
}

// Enabled reports whether this provider exports anything.
func (p Provider) Enabled() bool {
	return p.shutdown != nil
}

// Shutdown flushes whatever the provider is still holding and releases it.
//
// The context it is given is used for its values and for nothing else: the
// deadline is this function's, derived with [context.WithoutCancel] and bounded
// by [ShutdownTimeout]. A run that was interrupted is the one whose trace is
// most worth having, and it is precisely the run whose context is already
// cancelled by the time anything gets here.
func (p Provider) Shutdown(ctx context.Context) error {
	if p.shutdown == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ShutdownTimeout)
	defer cancel()

	return p.shutdown(ctx)
}

// disabled is the provider a run gets when tracing is off. Its tracers are the
// SDK's own no-op ones, so a caller starting a span pays a method call and
// nothing else.
func disabled() Provider {
	return Provider{TracerProvider: noop.NewTracerProvider()}
}

// Start reads the OpenTelemetry environment and returns the provider a run
// traces through.
//
// The returned Provider is always usable, error or no error: a configuration
// this build cannot honour disables tracing and is reported, because a build
// tool that refuses to generate code over a telemetry variable is worse than one
// that generates it untraced. The caller logs the error and carries on; it is a
// warning, not a failure.
//
// When tracing is on, and only then, the provider is installed as the global one
// ([otel.SetTracerProvider]) and the SDK's error handler is replaced
// ([otel.SetErrorHandler]). Leaving both alone in the off case is what makes
// "off costs nothing" true of the process and not merely of this package.
func Start(ctx context.Context, c cli.Context) (Provider, error) {
	cfg, err := configFromEnv(c.Env, buildVersion())
	if err != nil {
		return disabled(), err
	}
	if !cfg.enabled {
		return disabled(), nil
	}

	// Before the first span, so that an export failure has somewhere to go the
	// moment the batch processor's goroutine starts.
	otel.SetErrorHandler(errorHandler{ctx: ctx, log: c.Log})

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(newExporter(cfg)),
		sdktrace.WithResource(cfg.resource),
		sdktrace.WithSampler(cfg.sampler),
	)
	otel.SetTracerProvider(tp)

	c.Log.DebugContext(ctx, "tracing enabled",
		slog.String("endpoint", cfg.endpoint),
		slog.String("service_name", cfg.serviceName),
	)

	return Provider{TracerProvider: tp, shutdown: tp.Shutdown}, nil
}

// errorHandler is the whole of the second requirement: everything the SDK
// reports about itself becomes a log record attributed to the SDK, and nothing
// it reports reaches a standard stream directly.
//
// It holds the run's context so that a log record emitted from the batch
// processor's own goroutine carries the same context values as one emitted from
// the run. Cancellation of that context is not consulted — slog does not consult
// it either — which is deliberate: an export that failed because the run was
// interrupted is still worth a line.
type errorHandler struct {
	ctx context.Context
	log *slog.Logger
}

func (h errorHandler) Handle(err error) {
	h.log.ErrorContext(h.ctx, "opentelemetry sdk error", slog.Any("error", err))
}

// buildVersion is the version this build reports as service.version.
//
// Read out of the build rather than written down as a constant, for the same
// reason internal/plugin reads the generator's version out of it: a constant is
// a second place to remember on release day, and it goes stale silently.
func buildVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi.Main.Version == "" {
		return "(devel)"
	}
	return bi.Main.Version
}
