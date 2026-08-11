// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// tracerScope names the instrumentation a run's spans come from. It is this
// package's import path, which is what the OpenTelemetry specification asks a
// tracer name to be.
const tracerScope = "github.com/z5labs/avroc/internal/avroc"

// The phases of a run, one span name each.
//
// They are the boundaries that already existed as functions — loading the
// manifest, parsing the IDL, resolving the IR, the handshake, each generator's
// invocation, the merge, the prune and the record — and not a second
// decomposition invented for the trace. A phase that is not one of these is a
// phase avroc does not have.
//
// The per-generator spans are named for the work and carry the generator as an
// attribute rather than in the name, so that a backend can aggregate "how long
// does the handshake take" across every generator without parsing a string.
const (
	spanRun                = "avroc.generate"
	spanManifestLoad       = "avroc.manifest.load"
	spanIDLParse           = "avroc.idl.parse"
	spanIRResolve          = "avroc.ir.resolve"
	spanHandshake          = "avroc.handshake"
	spanGeneratorHandshake = "avroc.generator.handshake"
	spanGeneratorRun       = "avroc.generator.run"
	spanMerge              = "avroc.merge"
	spanPrune              = "avroc.prune"
	spanRecord             = "avroc.record"
)

// The attributes those spans carry.
//
// Every one of them is spelled exactly as the log attribute that reports the
// same fact, because they are two renderings of one run: a person reading a
// trace beside avroc's own log should not have to learn that exit_code is called
// something else in one of them. docs/plugin/SPEC.md's reading of a non-zero
// status holds here too — the code is carried and nothing is concluded from its
// value.
const (
	attrGenerator    = "generator"
	attrGenerators   = "generators"
	attrInput        = "input"
	attrPath         = "path"
	attrExitCode     = "exit_code"
	attrSignal       = "signal"
	attrSignalNumber = "signal_number"
)

// eventCollision is the event a merge records when two generators produced the
// same destination (#118). It is an event on the merge span rather than an
// attribute of any generator's, because a collision is a function of the whole
// set of plans and of no single generator's output.
const eventCollision = "collision"

// statusCancelled is the description on the span of a phase that did not finish
// because the run was cancelled. It is told apart from a phase that failed on
// its own account, because a run somebody interrupted is not a run that went
// wrong.
const statusCancelled = "cancelled"

// startSpan starts a child of the span ctx already carries.
//
// The tracer comes from that span's own provider rather than from
// otel.GetTracerProvider: internal/telemetry hands Main a provider, Main starts
// the run's root span from it, and every phase beneath inherits the provider the
// run was actually configured with. A phase reached without a root span above it
// — a unit test calling one directly — gets the no-op tracer instead of whatever
// the process global happens to be holding, which is the same reason
// internal/telemetry reads its environment through cli.Context and never
// through os: a configuration half injected and half ambient is one no test can
// describe.
//
// It is also why nothing below Main grew a [trace.Tracer] parameter. The provider
// is already in the context, threaded by the spans themselves.
func startSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return trace.SpanFromContext(ctx).TracerProvider().Tracer(tracerScope).Start(ctx, name, opts...)
}

// endSpan ends span, recording err on it if there was one.
//
// It is the one place a status is set, and every phase ends its span through it.
// A phase that classified its own failure — [generator.reportFailure] is the one
// that does — contributes attributes and leaves the status here, so that a
// generator killed because the run was cancelled is marked cancelled rather than
// described twice in two vocabularies.
func endSpan(ctx context.Context, span trace.Span, err error) {
	defer span.End()

	if err == nil {
		return
	}

	span.RecordError(err)
	if cancelled(ctx, err) {
		span.SetStatus(codes.Error, statusCancelled)
		return
	}
	span.SetStatus(codes.Error, err.Error())
}

// cancelled reports whether err is the run having been interrupted rather than
// anything about the work.
//
// The context is consulted as well as the error because the two are not the same
// sentence: a generator killed by the signal exec.CommandContext sends on
// cancellation reports itself as terminated by SIGKILL, and only the context says
// who sent it.
func cancelled(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled)
}

// generatorAttr names the generator a span is about, as docs/plugin/SPEC.md
// names it: the executable, avroc-gen-<name>.
func generatorAttr(name string) trace.SpanStartEventOption {
	return trace.WithAttributes(attribute.String(attrGenerator, name))
}
