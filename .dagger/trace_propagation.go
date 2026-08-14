// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// This file checks the one claim the tracing stories make that no unit test can
// reach: that the trace a run opens is *one* trace, from the pipeline that
// started it, through avroc, into every generator avroc forks (#199).
//
// # Why it has to be a stage
//
// Trace propagation breaks silently. A generator whose span context was dropped
// generates correct output and exits zero; every test in this repository still
// passes, the pipeline is still green, and the only symptom is an orphan span in
// a backend nobody is watching. That is the same argument image-contract,
// generator-image-contract, tag-scheme and worked-example are each here for, and
// it applies with more force to this one, because the other four break something
// a person eventually notices.
//
// The unit tests either side of the fork are real and are not enough.
// internal/avroc's TestATracedRunPropagatesToEveryGeneratorProcess reads what a
// child was handed; internal/plugin's tests read what a child does with it. Both
// stop at the process boundary they are testing, and neither can say that the
// spans met again in a backend — which is the only form of the claim an operator
// ever consumes.
//
// # What it asserts, and why structure rather than counts
//
// The claim is about *parentage*. A span count would be satisfied by four
// generators reporting four orphan traces, which is precisely the failure a
// dropped TRACEPARENT produces, so this reads the edges instead:
//
//   - Every span the backend returns for the run carries one trace id.
//   - avroc's root span is a child of the Dagger exec's span, so the pipeline is
//     the top of it. Dagger sets TRACEPARENT on every exec, so this costs
//     nothing beyond recording what it set — see traceLauncher.
//   - Each generator's avroc.plugin.generate is a child of the
//     avroc.generator.run that forked it, and the two agree about which
//     generator they are. The handshake pair is held to the same rule, because
//     #193 propagates to it on the same terms.
//
// # Why there is a launcher inside the image
//
// **Dagger sets OTEL_EXPORTER_OTLP_ENDPOINT on every exec**, overriding whatever
// the container carried, so a tool inside reports into Dagger's own trace.
// .dagger/tls_egress.go measured it on v0.21.8 and .dagger/main.go's traced run
// is written around it. The consequence here is that the endpoint cannot be
// configured from the outside: it is set inside the exec, by a launcher that
// changes that one variable and re-execs avroc.
//
// The launcher deliberately does not touch TRACEPARENT. That value is Dagger's
// exec span, it is what the third assertion above is about, and a check that
// wrote its own would be checking that avroc parents to a value this file chose.
//
// # Two boundaries
//
// **No TLS is covered here.** The collector is reached over plaintext on the
// container network, which is the shape docs/container/SPEC.md supports and the
// only one a scratch image can take without a bundle supplied. What happens to
// an `https://` endpoint from these images is docs/container/SPEC.md's "No
// certificate authorities" and .dagger/tls_egress.go's, and nothing here should
// be read as covering it.
//
// **This is functional and not a benchmark.** It asserts structure and never
// duration. Span timings are in the trace it fetches and no assertion reads
// them: a pipeline that failed on how long a generator took would fail on the
// engine's load, and the claim being made has nothing to do with speed.
package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"dagger/avroc/internal/dagger"
)

const (
	// collectorHostname and tempoHostname are the names the containers resolve
	// the two services by. The collector is what avroc exports to; Tempo is
	// behind it and is what the assertions read from.
	//
	// They are separate from tls_egress.go's collectorHost on purpose: that
	// check's collector is a fixture of this repository answering on two
	// schemes, and this one is a real OpenTelemetry Collector. Sharing a
	// constant would tie two unrelated topologies together.
	collectorHostname = "otel-collector"
	tempoHostname     = "tempo"

	// collectorOtlpHTTPPort is the OTLP/HTTP port the devex otel module's
	// OtlpReceiver listens on, and tempoOtlpHTTPPort the one Tempo's receiver
	// does. tempoQueryPort is Tempo's HTTP query API, which is what the
	// assertions read the finished trace from.
	collectorOtlpHTTPPort = 4318
	tempoOtlpHTTPPort     = 4318
	tempoQueryPort        = 3200

	// traceLauncher is where the launcher lands inside the image under test —
	// the plugin directory, because that is the one directory
	// docs/container/SPEC.md promises a derived image may copy an executable
	// into, and a launcher is a derived image's file like any other. Its name
	// is deliberately not avroc-gen-anything: a file matching that convention
	// in that directory is a generator as far as avroc's discovery is
	// concerned.
	traceLauncher = pluginDir + "/trace-launch"

	// traceparentRecord is where the launcher writes the TRACEPARENT Dagger
	// handed the exec.
	//
	// It is in a directory this check adds to the image, at a path that is part
	// of nothing: the image carries no temporary directory since #218 and no
	// working directory since #219, and neither of the two directories this check
	// does have will do — the project mount is the tree checkOneConnectedTrace
	// byte-compares against the committed example/, so a record left in it would
	// fail that comparison, and the plugin directory is where a file is a
	// generator. A derived image adding a directory of its own is what any adopter
	// does, and it is not the image gaining a writable path to make avroc work:
	// avroc needs none, which is the whole of #218.
	//
	// The negative control gets it too, because it goes through the same
	// tracedGeneration — which is what keeps it able to reach a *fetched trace*
	// before the assertions reject it, rather than failing on a missing record and
	// checking nothing.
	//
	// Standard output would need no directory at all and was rejected: the
	// launcher re-execs avroc with its streams inherited, so the record would have
	// to be picked back out of avroc's own output, and a generator writing to
	// stdout (which the contract permits for --plugin-info) would corrupt it.
	traceRecordDir    = "/record"
	traceparentRecord = traceRecordDir + "/traceparent"

	// tracePropagationPkg is the fixture on both ends of this check.
	tracePropagationPkg = "./internal/tools/trace-propagation"
)

// TracePropagation runs example/'s generation through the published image with
// its spans going to a collector backed by Tempo, then reads the trace back and
// requires it to be one connected trace from the Dagger exec through avroc into
// every generator (#199).
//
// It makes the assertions twice. Once against this repository as committed,
// where they must all hold; and once against a build with TRACEPARENT
// propagation removed, where they must not — the same shape as
// workedExample.rules and TagScheme, and here for the same reason. "A change
// that breaks propagation fails CI" is a claim about what happens to a *broken*
// build, and the committed tree is by construction not one, so a check whose
// failure path has never run is a check nobody knows the state of.
//
// platform is one of the published platforms; empty is the engine's own. One
// platform, not every one: what is under test is the propagation code, which is
// the same source on both, and the executables that carry it are already built
// and run for each platform by Regeneration.
//
// +check
// +cache="session"
func (m *Avroc) TracePropagation(
	ctx context.Context,
	// Run the check against the image for this platform, as `GOOS/GOARCH` — one
	// of the published platforms. Empty is the engine's own.
	// +optional
	platform string,
) error {
	p, err := imagePlatform(ctx, platform)
	if err != nil {
		return err
	}

	// One backend for both runs. They are told apart by trace id, which is the
	// property the whole check is about, so a second Tempo would cost two more
	// containers to separate runs that are already separate.
	tempo := dag.GrafanaStack().Tempo()
	collector := dag.Otel().
		Core().
		WithServiceBinding(tempoHostname, tempo.Service()).
		WithPipeline(dag.Otel().
			Pipeline("traces", "avroc").
			WithReceiver(dag.Otel().OtlpReceiver("avroc")).
			WithExporter(dag.Otel().OtlpHTTPExporter(
				"tempo",
				fmt.Sprintf("http://%s:%d", tempoHostname, tempoOtlpHTTPPort),
			)))
	backend := &traceBackend{tempo: tempo, collector: collector.Service()}

	broken, err := m.propagationRemoved(ctx)
	if err != nil {
		return err
	}

	// The committed tree first. A failure there is a finding about this
	// repository; a failure in the negative control below is a finding about
	// the check, and reporting them in that order is what keeps the second from
	// being read as the first.
	return errors.Join(
		m.checkOneConnectedTrace(ctx, p, backend),
		broken.checkPropagationRemovedIsCaught(ctx, p, backend),
	)
}

// traceBackend is the collector and the Tempo behind it, bound once and used by
// both runs.
type traceBackend struct {
	tempo     *dagger.GrafanaStackTempo
	collector *dagger.Service
}

// checkOneConnectedTrace requires the committed tree to produce one connected
// trace, and to have generated the committed example while doing it.
func (m *Avroc) checkOneConnectedTrace(
	ctx context.Context,
	platform dagger.Platform,
	backend *traceBackend,
) error {
	run, trace, err := m.tracedRun(ctx, platform, backend)
	if err != nil {
		return err
	}

	// Tracing is an observation of a generation and never an input to it, and
	// this is that sentence made about a run whose spans actually left the
	// container — which is more than Regeneration's traced run can say, since
	// nothing is listening at the endpoint it configures.
	generated := run.Directory(projectMount)
	if err := m.diffTrees(ctx, m.Source.Directory("example"), generated, "/trace-propagation"); err != nil {
		return fmt.Errorf("a traced generation did not reproduce the committed example: %w", err)
	}

	return trace.check()
}

// checkPropagationRemovedIsCaught is the negative control: the same run against
// a build with TRACEPARENT propagation removed, whose trace the assertions must
// *reject*.
//
// The two halves are separated on purpose, and the separation is the whole value
// of the control. A broken tree that would not compile, or would not run, or
// whose spans never reached Tempo also fails checkOneConnectedTrace — so a
// control written as "the whole thing errors" would go green the day the edit
// stopped compiling, having checked nothing. Here the run is required to
// *succeed* as far as a fetched trace, and only the assertions over that trace
// are required to fail. There is no arrangement in which this passes without the
// assertions having been exercised.
func (m *Avroc) checkPropagationRemovedIsCaught(
	ctx context.Context,
	platform dagger.Platform,
	backend *traceBackend,
) error {
	_, trace, err := m.tracedRun(ctx, platform, backend)
	if err != nil {
		return fmt.Errorf(
			"the negative control's build did not get as far as a trace, so it exercised none of the assertions it exists to exercise. See propagationRemoved: %w", err)
	}

	if err := trace.check(); err == nil {
		return errors.New(
			"a build with TRACEPARENT propagation removed produced a trace these assertions accepted, so nothing here would notice propagation breaking. See propagationRemoved")
	}
	return nil
}

// tracedRun performs one traced generation and reads the trace it produced back
// out of Tempo. It makes no assertion — everything here either worked or is an
// error about the run rather than about the trace's shape, which is the
// distinction the negative control is built on.
//
// The order matters and is not incidental. The generation is forced first, by
// reading the traceparent the launcher recorded; only then is the trace
// fetched, because Dagger is lazy and a fetch built beside the generation would
// be free to run before it.
func (m *Avroc) tracedRun(
	ctx context.Context,
	platform dagger.Platform,
	backend *traceBackend,
) (*dagger.Container, tracedTrace, error) {
	run := m.tracedGeneration(platform, backend.collector)

	traceparent, err := run.File(traceparentRecord).Contents(ctx)
	if err != nil {
		return nil, tracedTrace{}, fmt.Errorf("running the traced generation: %w", err)
	}
	parent, err := parseTraceparent(traceparent)
	if err != nil {
		return nil, tracedTrace{}, err
	}

	raw, err := m.fetchTrace(ctx, backend.tempo, parent.traceID)
	if err != nil {
		return nil, tracedTrace{}, err
	}
	spans, err := decodeTrace(raw)
	if err != nil {
		return nil, tracedTrace{}, err
	}

	return run, tracedTrace{parent: parent, spans: spans}, nil
}

// tracedTrace is one run's finished trace: the context the Dagger exec handed
// it, and every span the backend returned for it.
type tracedTrace struct {
	parent traceparent
	spans  []span
}

// check is the whole of what this story asserts, and it is one method so that
// the committed tree and the negative control are held to exactly the same
// requirements rather than to two lists that could drift apart.
func (t tracedTrace) check() error {
	return errors.Join(
		checkOneTraceID(t.spans, t.parent.traceID),
		checkRootIsUnderTheDaggerExec(t.spans, t.parent),
		checkGeneratorSpansAreChildren(t.spans),
	)
}

// tracedGeneration is example/'s generation, run through the bundle image with
// its spans going to the collector.
//
// The bundle image rather than the base: the claim is about avroc *and its
// generators*, so the container has to hold all three of them, and the bundle is
// the image that does — composed the way an adopter composes one, by copying
// each executable out of the image that publishes it.
func (m *Avroc) tracedGeneration(platform dagger.Platform, collector *dagger.Service) *dagger.Container {
	launcher := dag.Go().Build(m.Source, dagger.GoBuildOpts{
		Pkg:        tracePropagationPkg,
		Platform:   string(platform),
		DisableCgo: true,
	}).File("trace-propagation")

	return m.generatorBundleImage(platform).
		WithFile(traceLauncher, launcher, dagger.ContainerWithFileOpts{
			Owner:       imageUser,
			Permissions: executableMode,
		}).
		WithServiceBinding(collectorHostname, collector).
		WithDirectory(projectMount, m.Source.Directory("example"), dagger.ContainerWithDirectoryOpts{
			Owner: imageUser,
		}).
		// The launcher's own scratch directory, owned by the image's user so it
		// can write the record into it. See traceparentRecord.
		WithDirectory(traceRecordDir, dag.Directory(), dagger.ContainerWithDirectoryOpts{
			Owner: imageUser,
		}).
		// WithWorkdir as well as mounting there: since #219 the image declares no
		// working directory, and the launcher re-execs avroc in place rather than
		// naming a project, so the exec's own working directory is the only thing
		// that puts avroc where avroc.json is. See projectMount.
		//
		// It is set **last**, immediately before the exec, and deliberately: every
		// path above is absolute today, but a relative one added after a WithWorkdir
		// would resolve inside the project mount — which is the one tree this check
		// byte-compares against the committed example/, so a file landing there is a
		// failure whose cause is nowhere near the line that caused it.
		WithWorkdir(projectMount).
		WithExec([]string{
			traceLauncher,
			"-launch",
			"-endpoint", fmt.Sprintf("http://%s:%d", collectorHostname, collectorOtlpHTTPPort),
			"-record", traceparentRecord,
			"--",
			// cliPath rather than the plugin directory: since #217 the archetype
			// puts an application's own binary in its own directory and names it
			// absolutely in the entrypoint, and the plugin directory is for what an
			// extension adds. The launcher execs avroc by path rather than through
			// the entrypoint because it *is* the entrypoint's replacement — it sets
			// one environment variable and re-execs — so it has to name what the
			// entrypoint would have run.
			cliPath(), "generate",
		})
}

// fetchTrace reads the finished trace out of Tempo's query API.
//
// It runs in a toolchain container rather than in this module because the
// backend is a Dagger service and a service is reachable from a container bound
// to it and from nowhere else. The polling is inside the fixture, for the reason
// that file gives: an exec is one unit to the engine, so a wait belongs in the
// command being waited on.
func (m *Avroc) fetchTrace(ctx context.Context, tempo *dagger.GrafanaStackTempo, traceID string) (string, error) {
	raw, err := dag.Go().
		Container(m.Source).
		WithServiceBinding(tempoHostname, tempo.Service()).
		WithExec([]string{
			"go", "run", tracePropagationPkg,
			"-fetch",
			"-tempo", fmt.Sprintf("http://%s:%d", tempoHostname, tempoQueryPort),
			"-trace", traceID,
		}).
		Stdout(ctx)
	if err != nil {
		// The diagnosis belongs here rather than in the fixture, which knows only
		// that a trace id it was given never turned up. There are two ways to get
		// here and they are different findings: nothing exported at all, or
		// something exported into a trace that is not this one — which is what an
		// avroc ignoring the TRACEPARENT it inherited produces, and which reads as
		// a bare 404 unless somebody says so.
		return "", fmt.Errorf(
			"the trace the Dagger exec started, %s, never reached Tempo. Either the run exported nothing, or it exported into a trace of its own instead of joining the one it was run inside — see internal/avroc.Main's telemetry.Extract. The fixture reported: %w",
			traceID, err)
	}
	return raw, nil
}

// propagationRemoved returns this module bound to a copy of the source with
// TRACEPARENT propagation taken out of it, and nothing else changed.
//
// The edit is a replacement in the committed file rather than a file written
// from scratch, so the negative control stays a tree that differs from the real
// one in exactly one way — workedExample.rules' rule, and for its reason. One
// line is removed: the injection that writes this invocation's span context into
// the carrier. Everything else in generatorEnv survives, including the strip, so
// a generator is left with no trace context at all, which is exactly what an
// avroc that had never learned to propagate would hand it.
//
// The replacement is required to have changed something. An edit that matched
// nothing would rebuild the committed source, the assertions would pass, and the
// negative control would report that the check does not work — which is the one
// failure mode a negative control must not have.
func (m *Avroc) propagationRemoved(ctx context.Context) (*Avroc, error) {
	const (
		path    = "internal/avroc/propagate.go"
		inject  = "propagation.TraceContext{}.Inject(ctx, carrier)"
		removed = "_ = ctx // #199: propagation removed for .dagger/trace_propagation.go's negative control"
	)

	source, err := m.Source.File(path).Contents(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	edited := strings.Replace(source, inject, removed, 1)
	if edited == source {
		return nil, fmt.Errorf(
			"the negative control found no %q in %s to remove, so it would rebuild the committed source and check nothing",
			inject, path)
	}

	// A copy of the receiver with one file replaced, rather than a fresh Avroc
	// naming the fields it wants. Naming them is how this broke once: #217 added
	// GitDir, a literal listing Source and LintConfig kept compiling, and the
	// control's first call panicked on a nil directory a long way from here. The
	// control differs from the committed tree in exactly one file, so saying that
	// and nothing else is both shorter and the thing that stays true.
	broken := *m
	broken.Source = m.Source.WithNewFile(path, edited)
	return &broken, nil
}

// traceparent is the W3C trace context the Dagger exec handed the container:
// the trace the whole run belongs to, and the span avroc's root must be a child
// of.
type traceparent struct {
	traceID string
	spanID  string
}

// parseTraceparent reads the `00-<trace>-<span>-<flags>` form.
//
// An empty record is called out separately from a malformed one. It means
// Dagger stopped setting TRACEPARENT on execs, which is a finding about the
// engine and not about avroc, and it would otherwise be reported as every
// assertion below failing at once.
func parseTraceparent(record string) (traceparent, error) {
	value := strings.TrimSpace(record)
	if value == "" {
		return traceparent{}, errors.New(
			"the Dagger exec set no TRACEPARENT, so there is no pipeline span for avroc's root to be a child of and nothing this check can assert")
	}

	parts := strings.Split(value, "-")
	if len(parts) != 4 || len(parts[1]) != 32 || len(parts[2]) != 16 {
		return traceparent{}, fmt.Errorf("TRACEPARENT %q is not a W3C trace context", value)
	}
	return traceparent{traceID: strings.ToLower(parts[1]), spanID: strings.ToLower(parts[2])}, nil
}

// span is the part of an OTLP span these assertions read.
type span struct {
	TraceID      string `json:"traceId"`
	SpanID       string `json:"spanId"`
	ParentSpanID string `json:"parentSpanId"`
	Name         string `json:"name"`
	Attributes   []struct {
		Key   string `json:"key"`
		Value struct {
			StringValue string `json:"stringValue"`
		} `json:"value"`
	} `json:"attributes"`
}

// attr returns a string attribute, or "" when the span does not carry it.
func (s span) attr(key string) string {
	for _, a := range s.Attributes {
		if a.Key == key {
			return a.Value.StringValue
		}
	}
	return ""
}

// decodeTrace flattens Tempo's answer into the spans it holds.
//
// Tempo returns a tempopb.Trace as JSON: a `batches` array of OTLP
// ResourceSpans. The identifiers inside are protobuf `bytes` fields, so whether
// they arrive as hex or as base64 is a property of the marshaller rather than of
// the trace — normalizeID reads either, so a Tempo that changes its mind about
// that is not a red pipeline.
func decodeTrace(raw string) ([]span, error) {
	var doc struct {
		Batches []struct {
			ScopeSpans []struct {
				Spans []span `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"batches"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, fmt.Errorf("decoding the trace Tempo returned: %w", err)
	}

	var spans []span
	for _, batch := range doc.Batches {
		for _, scope := range batch.ScopeSpans {
			for _, s := range scope.Spans {
				s.TraceID = normalizeID(s.TraceID)
				s.SpanID = normalizeID(s.SpanID)
				s.ParentSpanID = normalizeID(s.ParentSpanID)
				spans = append(spans, s)
			}
		}
	}
	if len(spans) == 0 {
		return nil, errors.New("the trace Tempo returned holds no spans")
	}
	return spans, nil
}

// normalizeID renders an OTLP identifier as lower-case hex, whichever of the two
// encodings it arrived in.
func normalizeID(id string) string {
	if id == "" {
		return ""
	}
	if _, err := hex.DecodeString(id); err == nil {
		return strings.ToLower(id)
	}
	if raw, err := base64.StdEncoding.DecodeString(id); err == nil {
		return hex.EncodeToString(raw)
	}
	return id
}

// The span names this check reads. They are literals rather than a constant
// shared with internal/avroc and internal/plugin, for tls_egress.go's reason and
// TagScheme's: a check that moved with the thing it is checking would keep
// passing through a rename that broke every dashboard and every saved query
// pointing at these names. A rename is therefore a change here too, on purpose.
const (
	spanAvrocRoot          = "avroc.generate"
	spanGeneratorRun       = "avroc.generator.run"
	spanPluginGenerate     = "avroc.plugin.generate"
	spanGeneratorHandshake = "avroc.generator.handshake"
	spanPluginInfo         = "avroc.plugin.info"

	// attrGeneratorName is spelled the same by internal/avroc and
	// internal/plugin, which is what lets the two ends of a fork be required to
	// agree about which generator they are.
	attrGeneratorName = "generator"
)

// checkOneTraceID requires every span the backend returned to belong to the
// trace the Dagger exec started.
func checkOneTraceID(spans []span, traceID string) error {
	var wrong []string
	for _, s := range spans {
		if s.TraceID != traceID {
			wrong = append(wrong, fmt.Sprintf("%s is in trace %s", s.Name, s.TraceID))
		}
	}
	if len(wrong) == 0 {
		return nil
	}
	sort.Strings(wrong)
	return fmt.Errorf(
		"the run is meant to be one trace, %s, and %d span(s) are not in it: %s",
		traceID, len(wrong), strings.Join(wrong, "; "))
}

// checkRootIsUnderTheDaggerExec requires avroc's root span to be a child of the
// span Dagger's exec opened.
//
// This is the end the other two assertions hang from. Without it the run could
// be one internally consistent trace of its own that the pipeline knows nothing
// about, which is what avroc ignoring an inherited TRACEPARENT would produce.
func checkRootIsUnderTheDaggerExec(spans []span, parent traceparent) error {
	roots := named(spans, spanAvrocRoot)
	if len(roots) != 1 {
		return fmt.Errorf("want exactly one %s span in the trace, got %d", spanAvrocRoot, len(roots))
	}
	if got := roots[0].ParentSpanID; got != parent.spanID {
		return fmt.Errorf(
			"%s is parented to %q and the Dagger exec's span is %s, so avroc did not make itself a child of the pipeline that ran it",
			spanAvrocRoot, got, parent.spanID)
	}
	return nil
}

// checkGeneratorSpansAreChildren requires every span a generator process opened
// to be a child of the avroc span that forked that process.
//
// Both pairs are held to it. The generation is the one the story is about; the
// handshake is propagated to on the same terms (#193) and runs a whole process
// too, so a check that covered only the first would go on passing through a
// handshake that had stopped being part of the trace.
//
// The pairing is by parent and by the `generator` attribute together. Parentage
// alone would be satisfied by four plugin spans all hanging off one avroc span,
// which is not a shape anything here can produce but is exactly the kind of
// thing an assertion should not have to trust.
func checkGeneratorSpansAreChildren(spans []span) error {
	pairs := []struct{ parent, child string }{
		{spanGeneratorRun, spanPluginGenerate},
		{spanGeneratorHandshake, spanPluginInfo},
	}

	byID := map[string]span{}
	for _, s := range spans {
		byID[s.SpanID] = s
	}

	var errs []error
	for _, pair := range pairs {
		parents := named(spans, pair.parent)
		children := named(spans, pair.child)

		switch {
		case len(parents) == 0:
			errs = append(errs, fmt.Errorf("the trace holds no %s span at all", pair.parent))
			continue
		// The two directions are different findings and are reported as such.
		// Fewer children than parents is the failure this check is about: a
		// generator process reported somewhere other than this trace. More
		// children than parents is not that at all — it is a span appearing
		// under a fork avroc did not make — and a single subtraction would
		// describe it as a negative number of lost processes.
		case len(children) < len(parents):
			errs = append(errs, fmt.Errorf(
				"avroc opened %d %s span(s) and the trace holds %d %s span(s), so %d generator process(es) reported somewhere other than this trace",
				len(parents), pair.parent, len(children), pair.child, len(parents)-len(children)))
		case len(children) > len(parents):
			errs = append(errs, fmt.Errorf(
				"the trace holds %d %s span(s) against %d %s span(s), so %d generator span(s) are in it that no avroc invocation accounts for",
				len(children), pair.child, len(parents), pair.parent, len(children)-len(parents)))
		}

		for _, child := range children {
			got, ok := byID[child.ParentSpanID]
			switch {
			case !ok:
				errs = append(errs, fmt.Errorf(
					"%s for generator %q names parent %q, which is not a span in this trace",
					pair.child, child.attr(attrGeneratorName), child.ParentSpanID))
			case got.Name != pair.parent:
				errs = append(errs, fmt.Errorf(
					"%s for generator %q is a child of %s, want %s",
					pair.child, child.attr(attrGeneratorName), got.Name, pair.parent))
			case got.attr(attrGeneratorName) != child.attr(attrGeneratorName):
				errs = append(errs, fmt.Errorf(
					"%s says it is generator %q and its parent %s says %q",
					pair.child, child.attr(attrGeneratorName), pair.parent, got.attr(attrGeneratorName)))
			}
		}
	}
	return errors.Join(errs...)
}

// named is every span in the trace with one name.
func named(spans []span, name string) []span {
	var out []span
	for _, s := range spans {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out
}
