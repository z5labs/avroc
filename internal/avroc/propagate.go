// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/propagation"
)

// The environment variables a generator learns its parent span from (#193).
//
// This is a convention rather than a ratified part of any specification: W3C
// Trace Context defines HTTP headers, and OpenTelemetry's environment variable
// specification covers SDK configuration and not context propagation. It is
// nonetheless what the ecosystem converged on — otel-cli, the Jenkins
// OpenTelemetry plugin, Buildkite's tracing, and Dagger, which sets TRACEPARENT
// on every container exec and is therefore why avroc running under `dagger call`
// already has one to be a child of.
//
// The names are the header names upper-cased, which is the whole of the
// convention: the values are the header values, unchanged, so a generator using
// any OpenTelemetry SDK parses them with the propagator it already has.
const (
	envTraceparent = "TRACEPARENT"
	envTracestate  = "TRACESTATE"
)

// generatorEnv is the environment one generator process runs with: avroc's,
// with this invocation's span context written over whatever trace context it
// already carried.
//
// docs/plugin/SPEC.md's "The environment" is normative, and everything below
// follows from its one sentence — avroc passes its own environment through
// unchanged, except for the trace context, which is per-invocation and therefore
// cannot be inherited. Every SDK *configuration* variable an operator set —
// endpoint, protocol, headers, sampler, resource attributes — reaches the child
// because it is in environ and nothing here removes it, OTEL_SERVICE_NAME
// included: a user who sets it gets avroc and its generators under one name, and
// a user who does not gets each executable's own SDK default, which is not a
// choice avroc is entitled to make on their behalf.
//
// Three things it does are decisions rather than mechanics.
//
// It **overwrites**. avroc may itself have been started with a TRACEPARENT — by
// CI, or by the Dagger exec running it — and that one is avroc's own parent. The
// child gets the span context of the invocation that is starting, which replaces
// the inherited value rather than accompanying it; two TRACEPARENT entries in
// one environment is a child parented by whichever the reader happened to take.
//
// It **removes when there is nothing to write**. A run avroc is not tracing has
// no span context to hand over, and passing an inherited TRACEPARENT through
// would parent the generator's spans to a trace avroc is not part of — a child
// of a span nobody ever exported. Off is therefore actively unset rather than
// merely unmodified, and it falls out of the same code path: the propagator
// injects nothing for an invalid span context, and the two variables were
// already stripped.
//
// It **strips case-insensitively**. The convention is the upper-case spelling
// and that is the only one written, but a lower-case traceparent left in place
// beside it is trace context avroc did not set and cannot vouch for, reachable by
// anything that looks the name up the way a header would be.
func generatorEnv(ctx context.Context, environ []string) []string {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)

	env := make([]string, 0, len(environ)+len(carrier))
	for _, entry := range environ {
		if isTraceContextVar(entry) {
			continue
		}
		env = append(env, entry)
	}

	// The carrier is keyed by the header names, which are lower case; the
	// variables are their upper-case spellings, and there are exactly two of
	// them. Appended in a fixed order after an environment whose order is
	// preserved, so that the vector a child is handed is a function of avroc's
	// environment and not of a map iteration.
	if traceparent := carrier.Get("traceparent"); traceparent != "" {
		env = append(env, envTraceparent+"="+traceparent)
	}
	if tracestate := carrier.Get("tracestate"); tracestate != "" {
		env = append(env, envTracestate+"="+tracestate)
	}
	return env
}

// isTraceContextVar reports whether an environment entry — a NAME=VALUE string,
// as os.Environ writes them — carries trace context.
//
// An entry with no "=" in it is not a variable this recognises and is passed
// through, like everything else avroc has no opinion about.
func isTraceContextVar(entry string) bool {
	name, _, found := strings.Cut(entry, "=")
	if !found {
		return false
	}
	return strings.EqualFold(name, envTraceparent) || strings.EqualFold(name, envTracestate)
}
