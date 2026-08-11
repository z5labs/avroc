// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package plugin

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// tracerScope names the instrumentation a generator's span comes from. It is
// this package's import path, which is what the OpenTelemetry specification asks
// a tracer name to be.
//
// It is this package's and not the generator's because this is where the span is
// opened: every generator in this repository runs through [Main], so there is one
// implementation and one scope rather than three of each. Which generator it was
// is an attribute — see [attrGenerator].
const tracerScope = "github.com/z5labs/avroc/internal/plugin"

// The two invocations docs/plugin/SPEC.md defines, one span name each.
//
// They are named for what the *generator* does, where internal/avroc's
// avroc.generator.run and avroc.generator.handshake are named for what avroc
// does — running a process and waiting on it. The two are parent and child over
// the same interval, and naming them alike would leave a trace in which the only
// way to tell the fork from the work is the depth.
const (
	spanGenerate   = "avroc.plugin.generate"
	spanPluginInfo = "avroc.plugin.info"
)

// The phases a generator spends time inside a generation, one span name each
// (#197).
//
// An invocation being one span answers "which generator is slow"; it does not
// answer "which part of it". These are that second question, and they are the
// phases the code already had as functions — validating the descriptor, reading
// the options, rendering a schema, computing a fingerprint, writing a file —
// rather than a decomposition invented for the trace. It is the rule
// internal/avroc's phase spans are chosen by, applied on the other side of the
// fork.
//
// **Cardinality is what fixes the granularity.** A span per schema and a span
// per file are bounded by the descriptor, which avroc built from the manifest; a
// span per type or per field would be bounded by the user's IDL, and a schema
// with a few thousand fields would produce a trace nobody can open. So anything
// finer than a schema or a file is an attribute on one of these spans and never
// a span of its own, and
// internal/avroc-gen-go.TestSpanCountIsAFunctionOfTheDescriptorAndNotTheSchema
// is what holds that as the generators grow.
//
// They live here rather than in the three generators for the reason
// [tracerScope] gives: [Main] is the one place every generator in this
// repository runs through, so there is one set of names and one instrumentation
// scope instead of three of each. Which generator opened one is the invocation
// span's `generator` attribute, one level up.
//
// Not every generator opens all of them, and that is the point rather than an
// omission. avroc-gen-go is the only generator here with enough work in it to
// have parts, so it is the only one that opens the descriptor, option,
// fingerprint and file spans; avroc-gen-json and avroc-gen-pcf write one file
// per schema and have no rendering to speak of, so they open the per-schema span
// and nothing finer. Instrumentation heavier than the work it measures makes a
// trace harder to read, not easier.
const (
	spanDescriptorValidate = "avroc.plugin.descriptor.validate"
	spanOptionsParse       = "avroc.plugin.options.parse"
	spanSchemaGenerate     = "avroc.plugin.schema.generate"
	spanFingerprint        = "avroc.plugin.fingerprint"
	spanFileWrite          = "avroc.plugin.file.write"
)

// The attributes those spans carry.
//
// Every one of them is spelled exactly as internal/avroc spells the same fact,
// for the reason that package gives: a person reading a generator's span beside
// avroc's own should not have to learn that exit_code is called something else in
// one of them. attrPath is that reason exactly — it is the spelling
// internal/avroc's merge already reports a generated path by — and attrSchema is
// the one fact no span of avroc's carries, because avroc names an *input* and a
// generator names the schema that came out of it.
const (
	attrGenerator = "generator"
	attrIRVersion = "ir_version"
	attrExitCode  = "exit_code"
	attrSchema    = "schema"
	attrPath      = "path"
)

// StartDescriptorValidate starts the span covering everything a generator checks
// about the descriptor as a whole before it looks at a schema: the IR version it
// was handed, and whatever else is a property of the request rather than of one
// of the schemas in it.
func StartDescriptorValidate(ctx context.Context) (context.Context, trace.Span) {
	return startPhase(ctx, spanDescriptorValidate, "", "")
}

// StartOptionsParse starts the span covering a generator reading its --opt
// pairs, and refusing the ones it cannot honour.
func StartOptionsParse(ctx context.Context) (context.Context, trace.Span) {
	return startPhase(ctx, spanOptionsParse, "", "")
}

// StartSchemaGenerate starts the span covering everything a generator does for
// one schema, named by that schema's base name — [ir.SchemaBaseName], which is
// the name every generator's filename is built from and therefore the name a
// person reading the trace already has in front of them.
//
// Everything, which is to say the write as well as the rendering. A generator
// fine-grained enough to trace its writes opens [StartFileWrite] *inside* this
// one rather than beside it, so the rendering stays separately readable as the
// difference between the two; a generator that does not still has its write time
// inside the span for the schema it belongs to, rather than accounted only to
// the invocation. One span name meaning "this schema's work" in every generator
// is what lets a backend ask how long a schema took without knowing which
// generator answered.
func StartSchemaGenerate(ctx context.Context, schema string) (context.Context, trace.Span) {
	return startPhase(ctx, spanSchemaGenerate, attrSchema, schema)
}

// StartFingerprint starts the span covering a schema's canonical form and the
// fingerprint computed over it.
//
// It is a child of that schema's [StartSchemaGenerate] span, and it is the one
// place avroc-gen-go does real computation over the IR rather than walking it,
// which is why it is separated out of the rendering it happens inside.
func StartFingerprint(ctx context.Context) (context.Context, trace.Span) {
	return startPhase(ctx, spanFingerprint, "", "")
}

// StartFileWrite starts the span covering one whole file being written through
// a [FileWriter], named by the path it was written at — relative to --out, which
// is the only form of the path that means anything: docs/plugin/SPEC.md makes
// the absolute one a temporary location avroc chose for this invocation alone.
func StartFileWrite(ctx context.Context, path string) (context.Context, trace.Span) {
	return startPhase(ctx, spanFileWrite, attrPath, path)
}

// EndPhase ends a phase's span, recording err on it if there was one.
//
// It is the only place a phase's status is set, so a phase records its failure
// once — the same discipline internal/avroc's endSpan keeps. Nothing here
// classifies a cancelled run separately, for [endSpan]'s reason: a generator
// killed by that cancellation never reaches this function, and one that merely
// noticed goes on to an exit status like any other, so the classification stays
// avroc's, which can see the signal it sent.
//
// A generator returns the first failure it meets, so the phase that failed is
// the phase the invocation failed in, and the exit status that followed from it
// is on the invocation's own span.
func EndPhase(span trace.Span, err error) {
	defer span.End()

	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

// startPhase starts a child of the span the invocation opened, and does nothing
// whatsoever when the invocation is not being traced.
//
// That early return is the whole of "with tracing disabled a generator does no
// additional work per schema or per file": no tracer is asked for, no attribute
// is built and no span is started, so what the instrumentation costs an untraced
// invocation is one interface call per phase and nothing per type and nothing
// per field. It is also why every helper above takes its attribute as a plain
// string rather than an [attribute.KeyValue] — a value built at the call site
// would be built whether or not anything was ever going to read it.
//
// "Not being traced" is read off the invocation's own span rather than off a
// flag of our own, because there is nothing else that could answer it: a
// generator with no TRACEPARENT and no endpoint has a no-op provider, which is
// the ordinary case rather than a fallback. A parent that is recording but
// unsampled is the other case it catches, and under the SDK's ParentBased
// default a child of an unsampled parent is unsampled too — so a span started
// here would be one nothing exports either way.
//
// The span handed back for an untraced phase is a no-op one and deliberately not
// the parent. The caller ends what it is given, and giving it the invocation's
// span would end the invocation in the middle of the first schema.
func startPhase(ctx context.Context, name, key, value string) (context.Context, trace.Span) {
	parent := trace.SpanFromContext(ctx)
	if !parent.IsRecording() {
		return ctx, noop.Span{}
	}

	if key == "" {
		return parent.TracerProvider().Tracer(tracerScope).Start(ctx, name)
	}
	return parent.TracerProvider().Tracer(tracerScope).Start(ctx, name,
		trace.WithAttributes(attribute.String(key, value)),
	)
}

// FlushBudget is the whole of the time a generator may spend flushing its spans
// before it returns its exit status.
//
// It exists because a generator is a **child process**, which is the one way its
// telemetry differs from avroc's. Nothing kills avroc while it flushes, so
// internal/telemetry's own [telemetry.ShutdownTimeout] is free to be as long as a
// person's patience. A generator is killed by the avroc that forked it once it
// outlives avroc's wait delay, and a generator blocking on an export at exit is
// therefore a generator killed mid-export — a symptom that reads as a hung
// generator rather than as a collector that is not there.
//
// So the budget is bounded **below that delay**, and the relationship is stated
// rather than left to two constants that happen to differ:
// internal/avroc.TestAGeneratorFlushesWellInsideTheDelayAvrocAllowsIt is the
// assertion, and it is in that package because the delay is that package's
// (handshakeWaitDelay). The two are not one shared constant for the reason
// nothing else in this contract is either — internal/plugin is the generator's
// half and internal/avroc is avroc's, they sit either side of a process boundary,
// and a third-party generator implements this contract importing nothing from
// here. What keeps them honest is a test, as it is for --plugin-info itself.
//
// One export request gets half of it; see [telemetry.WithFlushBudget], which is
// where that division is made.
//
// #190 retired the wait delay the generation invocation used to have — its
// standard error is inherited now, so there is no pipe for a grandchild to hold
// open — which leaves the handshake as the one invocation avroc still bounds.
// That is the tighter of the two bounds, since the other one is no bound at all,
// so a budget inside it is inside both.
const FlushBudget = 2 * time.Second

// endSpan ends an invocation's span with the exit status the process is about to
// return.
//
// The status is the whole of what a generator says about how it went, and it is
// the whole of what avroc reads (#190), so it is also the whole of what the span
// says: an exit_code attribute always, and a span status derived from it. A
// generator's own view of its failure becomes a child of avroc's view of the
// same failure rather than a second, differently worded record of it.
//
// Nothing here classifies a cancelled run separately, though internal/avroc does.
// A generator killed by that cancellation never reaches this function at all, and
// one that merely noticed the context was done has an exit status like any other
// — the classification is avroc's, which can see the signal it sent, and a second
// one made here would be the two vocabularies internal/avroc's endSpan exists to
// avoid.
func endSpan(span trace.Span, code int) {
	defer span.End()

	span.SetAttributes(attribute.Int(attrExitCode, code))
	if code == 0 {
		span.SetStatus(codes.Ok, "")
		return
	}
	span.SetStatus(codes.Error, fmt.Sprintf("exit status %d", code))
}
