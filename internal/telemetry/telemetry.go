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
	"time"

	"github.com/z5labs/avroc/internal/cli"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// The environment variables a process learns its parent span from (#193, #196).
//
// This is the reading half of the convention internal/avroc writes: the W3C
// Trace Context header names upper-cased, carrying the header values unchanged.
// docs/plugin/SPEC.md's "Trace context" is normative, and it says there what
// this package cannot — that the pair is a convention rather than a ratified
// part of any specification, and that a process finding neither variable is in
// the ordinary case and not an exceptional one.
//
// They are exported because both halves in this repository take their spelling
// from here. One misspelling that only one side made is a child process whose
// spans silently start a trace of their own.
const (
	EnvTraceparent = "TRACEPARENT"
	EnvTracestate  = "TRACESTATE"
)

// Option adjusts what [Start] builds.
//
// Every one of them exists because this package is linked into two kinds of
// process: avroc, which nothing is waiting on, and a generator, which avroc
// forked and will not wait on forever. Nothing here is configuration in the
// operator's sense — that is the environment's, and the environment wins
// wherever the two meet.
type Option func(*settings)

// settings are the caller's contributions to a configuration, and they are all
// defaults: each is what the value would be had the environment not spoken, or
// a bound the environment has no variable for.
type settings struct {
	defaultServiceName string
	shutdownTimeout    time.Duration
	exportTimeout      time.Duration
}

func newSettings(opts []Option) settings {
	set := settings{
		defaultServiceName: DefaultServiceName,
		shutdownTimeout:    ShutdownTimeout,
		exportTimeout:      ExportTimeout,
	}
	for _, opt := range opts {
		opt(&set)
	}
	return set
}

// WithDefaultServiceName names the executable this provider is running inside,
// for the case where the operator has not named it themselves.
//
// A generator is a different service from the avroc that forked it, and
// docs/plugin/SPEC.md says so: avroc neither sets nor overrides
// OTEL_SERVICE_NAME, so "a user who sets it gets the whole build under one
// service name and a user who does not gets each executable's own default".
// This is that default, and passing it in rather than reading it here is what
// keeps this package from having to know what it was linked into.
func WithDefaultServiceName(name string) Option {
	return func(s *settings) {
		if name == "" {
			return
		}
		s.defaultServiceName = name
	}
}

// WithFlushBudget bounds the whole of the time [Provider.Shutdown] may take,
// for a process that is not free to take as long as it likes.
//
// avroc is free to: nothing kills it while it flushes. A generator is not — it
// is a child avroc waits on, and one still holding a stream open past avroc's
// wait delay is killed, so a generator that blocks on an export at exit is a
// generator killed mid-export, and the symptom reads as a hung generator rather
// than as an unreachable collector. internal/plugin holds the budget and the
// relationship to that delay; this is the knob it sets.
//
// One export request gets **half** the budget rather than all of it, so that a
// request begun as the flush begins can fail and be reported inside the flush
// rather than being cut off by it — the relationship [ExportTimeout] and
// [ShutdownTimeout] already have, written as a division so that shortening one
// cannot leave the other behind.
//
// A budget too small to halve — under two nanoseconds, which is nobody's
// intention and every division's edge — keeps the whole of it rather than
// rounding to zero. Zero is the one value that must not reach the exporter,
// because it is not "no time" there but "no timeout", and it would be answered
// with [ExportTimeout]: a bound longer than the budget it was meant to fit
// inside, which is the invariant inverted rather than merely relaxed.
func WithFlushBudget(d time.Duration) Option {
	return func(s *settings) {
		if d <= 0 {
			return
		}

		export := d / 2
		if export <= 0 {
			export = d
		}

		s.shutdownTimeout = d
		s.exportTimeout = export
	}
}

// Extract returns ctx carrying the span context the environment names, which is
// the parent of every span this process opens.
//
// It is the generator's half of #193 and it lives here, in the one package the
// determinism ban exempts, for the reason the ban exists: reading the
// environment is what a generator may not do on the path to a generated byte,
// and a lookup written in internal/plugin would be one the static check cannot
// see through [cli.Environment]. Nothing it returns reaches [FileWriter];
// docs/plugin/SPEC.md forbids that outright.
//
// Absence is the ordinary case and so is a value that will not parse: the
// propagator extracts nothing for either, ctx comes back as it went in, and the
// process starts a trace of its own or starts none at all. There is no error to
// report, because there is no configuration here to have got wrong.
func Extract(ctx context.Context, env cli.Environment) context.Context {
	if env == nil {
		return ctx
	}

	// Keyed by the header names, which are lower case; the variables are their
	// upper-case spellings, and there are exactly two of them — written out
	// rather than ranged over, so that nothing here depends on a map's order.
	carrier := propagation.MapCarrier{}
	if value, ok := env.LookupEnv(EnvTraceparent); ok {
		carrier.Set("traceparent", value)
	}
	if value, ok := env.LookupEnv(EnvTracestate); ok {
		carrier.Set("tracestate", value)
	}

	return propagation.TraceContext{}.Extract(ctx, carrier)
}

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

	// shutdownTimeout bounds that flush. Zero is [ShutdownTimeout], which is
	// what a Provider nobody configured — the disabled one — carries.
	shutdownTimeout time.Duration
}

// Enabled reports whether this provider exports anything.
func (p Provider) Enabled() bool {
	return p.shutdown != nil
}

// Shutdown flushes whatever the provider is still holding and releases it.
//
// The context it is given is used for its values and for nothing else: the
// deadline is this function's, derived with [context.WithoutCancel] and bounded
// by [ShutdownTimeout] or by whatever [WithFlushBudget] shortened it to. A run
// that was interrupted is the one whose trace is most worth having, and it is
// precisely the run whose context is already cancelled by the time anything gets
// here.
func (p Provider) Shutdown(ctx context.Context) error {
	if p.shutdown == nil {
		return nil
	}

	timeout := p.shutdownTimeout
	if timeout <= 0 {
		timeout = ShutdownTimeout
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
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
func Start(ctx context.Context, c cli.Context, opts ...Option) (Provider, error) {
	cfg, err := configFromEnv(c.Env, buildVersion(), opts...)
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

	return Provider{
		TracerProvider:  tp,
		shutdown:        tp.Shutdown,
		shutdownTimeout: cfg.shutdownTimeout,
	}, nil
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
