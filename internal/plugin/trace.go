// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package plugin

import (
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
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

// The attributes those spans carry.
//
// Every one of them is spelled exactly as internal/avroc spells the same fact,
// for the reason that package gives: a person reading a generator's span beside
// avroc's own should not have to learn that exit_code is called something else in
// one of them.
const (
	attrGenerator = "generator"
	attrIRVersion = "ir_version"
	attrExitCode  = "exit_code"
)

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
