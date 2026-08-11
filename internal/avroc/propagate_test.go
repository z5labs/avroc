// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/z5labs/avroc/internal/cli"
	"github.com/z5labs/avroc/internal/ir"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// TestGeneratorEnv is the unit half of #193: what one child's environment is,
// given avroc's and the span the invocation is running under.
func TestGeneratorEnv(t *testing.T) {
	t.Run("carries the invocation's span context", func(t *testing.T) {
		ctx, span := recordedSpan(t)
		defer span.End()

		env := generatorEnv(ctx, nil)

		sc := span.SpanContext()
		want := fmt.Sprintf("00-%s-%s-%s", sc.TraceID(), sc.SpanID(), sc.TraceFlags())
		if got := onlyValue(t, env, envTraceparent); got != want {
			t.Errorf("%s = %q, want %q", envTraceparent, got, want)
		}
	})

	t.Run("replaces a traceparent inherited from avroc's own environment", func(t *testing.T) {
		ctx, span := recordedSpan(t)
		defer span.End()

		inherited := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
		env := generatorEnv(ctx, []string{envTraceparent + "=" + inherited})

		got := onlyValue(t, env, envTraceparent)
		if got == inherited {
			t.Errorf("the child inherited avroc's own %s: a generator's spans would be children of avroc's parent", envTraceparent)
		}
		if want := span.SpanContext().SpanID().String(); !strings.Contains(got, want) {
			t.Errorf("%s = %q, which does not name the invocation's span %s", envTraceparent, got, want)
		}
	})

	t.Run("propagates a tracestate when the span context carries one", func(t *testing.T) {
		state, err := oteltrace.ParseTraceState("vendor=value")
		if err != nil {
			t.Fatal(err)
		}
		sc := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
			TraceID:    oteltrace.TraceID{0x01},
			SpanID:     oteltrace.SpanID{0x02},
			TraceFlags: oteltrace.FlagsSampled,
			TraceState: state,
		})
		ctx := oteltrace.ContextWithSpanContext(t.Context(), sc)

		env := generatorEnv(ctx, []string{envTracestate + "=stale=value"})

		if got, want := onlyValue(t, env, envTracestate), "vendor=value"; got != want {
			t.Errorf("%s = %q, want %q", envTracestate, got, want)
		}
	})

	t.Run("writes no tracestate when there is none", func(t *testing.T) {
		ctx, span := recordedSpan(t)
		defer span.End()

		env := generatorEnv(ctx, nil)

		if values := valuesOf(env, envTracestate); len(values) != 0 {
			t.Errorf("%s = %q, want it absent", envTracestate, values)
		}
	})

	t.Run("removes both when the run is not traced", func(t *testing.T) {
		// The whole of the case: avroc is not tracing, its own environment carries
		// trace context from something upstream, and passing it through would
		// parent the generator's spans to a trace avroc is not part of.
		environs := map[string][]string{
			"no span at all": {
				envTraceparent + "=00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
				envTracestate + "=vendor=value",
			},
			"the lower-case spelling": {
				"traceparent=00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
				"tracestate=vendor=value",
			},
		}

		for name, environ := range environs {
			t.Run(name, func(t *testing.T) {
				for _, ctx := range []context.Context{
					t.Context(),
					noopSpanContext(t),
				} {
					env := generatorEnv(ctx, environ)

					if len(env) != 0 {
						t.Errorf("the child's environment is %q, want the trace context gone", env)
					}
				}
			})
		}
	})

	t.Run("passes every other variable through unchanged", func(t *testing.T) {
		// Setting cmd.Env at all is what makes this worth asserting: a child whose
		// environment avroc builds is one avroc can silently truncate, and
		// SOURCE_DATE_EPOCH is the variable docs/plugin/SPEC.md promises a
		// generator by name.
		environ := []string{
			"PATH=/usr/local/bin",
			"SOURCE_DATE_EPOCH=1700000000",
			"OTEL_SERVICE_NAME=chosen-by-the-user",
			"OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4318",
			"HOME=/home/nobody",
			"AN_ENTRY_WITH_NO_EQUALS_SIGN",
		}
		ctx, span := recordedSpan(t)
		defer span.End()

		env := generatorEnv(ctx, environ)

		if got := env[:len(environ)]; !slices.Equal(got, environ) {
			t.Errorf("the environment reaching the child is %q, want %q with the trace context appended", got, environ)
		}
		if got, want := len(env), len(environ)+1; got != want {
			t.Errorf("the child's environment holds %d entries, want %d", got, want)
		}
	})

	t.Run("neither sets nor overrides OTEL_SERVICE_NAME", func(t *testing.T) {
		// A generator's identity is the SDK's per-executable default, or whatever
		// the user set for the whole build. It is not something avroc invents.
		ctx, span := recordedSpan(t)
		defer span.End()

		for name, environ := range map[string][]string{
			"absent": nil,
			"set":    {"OTEL_SERVICE_NAME=chosen-by-the-user"},
		} {
			t.Run(name, func(t *testing.T) {
				env := generatorEnv(ctx, environ)

				if got, want := valuesOf(env, "OTEL_SERVICE_NAME"), valuesOf(environ, "OTEL_SERVICE_NAME"); !slices.Equal(got, want) {
					t.Errorf("OTEL_SERVICE_NAME = %q, want %q", got, want)
				}
			})
		}
	})
}

// TestATracedRunPropagatesToEveryGeneratorProcess is the story end to end,
// through the CLI entry point and a real generator process.
//
// The generator is a shell script that writes down the environment it was
// handed, which is the only place the answer actually is: what avroc put in
// cmd.Env is not the question, and a test asserting on that would pass with the
// process never having run.
func TestATracedRunPropagatesToEveryGeneratorProcess(t *testing.T) {
	// An inherited value on avroc's own process, because that is the case that
	// distinguishes propagation from passing something through: this one is
	// avroc's parent and must reach no child.
	const inherited = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	t.Setenv(envTraceparent, inherited)
	t.Setenv(envTracestate, "upstream=value")

	collector := newSpanCollector(t)
	run := generateWithEnvDump(t, collector)

	spans := collector.spans(t)
	if len(spans) == 0 {
		t.Fatal("the run exported no spans, so there is nothing for a child to be parented to")
	}

	invocations := map[string]struct {
		env  childEnvironment
		span string
	}{
		"the generation invocation": {run.generate, spanGeneratorRun},
		"the handshake invocation":  {run.handshake, spanGeneratorHandshake},
	}

	for name, invocation := range invocations {
		t.Run(name, func(t *testing.T) {
			if len(invocation.env) == 0 {
				t.Fatal("the generator did not run, so it recorded no environment")
			}

			traceparent := onlyValue(t, invocation.env, envTraceparent)
			if traceparent == inherited {
				t.Fatalf("the child was handed avroc's own %s", envTraceparent)
			}

			parent := spanNamed(t, spans, invocation.span)
			want := fmt.Sprintf("00-%s-%s-01",
				hex.EncodeToString(parent.GetTraceId()), hex.EncodeToString(parent.GetSpanId()))
			if traceparent != want {
				t.Errorf("%s = %q, want %q — the span it names is not the %s span avroc exported",
					envTraceparent, traceparent, want, invocation.span)
			}
		})
	}

	t.Run("the inherited tracestate is replaced too", func(t *testing.T) {
		// avroc's own span context carries no trace state, so there is none to
		// hand on — and the upstream one is not avroc's to forward.
		if values := valuesOf(run.generate, envTracestate); len(values) != 0 {
			t.Errorf("%s = %q, want it absent", envTracestate, values)
		}
	})
}

// TestAnUntracedRunUnsetsTheTraceContext is the other half: tracing off means
// actively unset, not merely unmodified.
func TestAnUntracedRunUnsetsTheTraceContext(t *testing.T) {
	t.Setenv(envTraceparent, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	t.Setenv(envTracestate, "upstream=value")

	run := generateWithEnvDump(t, nil)

	for name, env := range map[string]childEnvironment{
		"the generation invocation": run.generate,
		"the handshake invocation":  run.handshake,
	} {
		t.Run(name, func(t *testing.T) {
			if len(env) == 0 {
				t.Fatal("the generator did not run, so it recorded no environment")
			}
			for _, variable := range []string{envTraceparent, envTracestate} {
				if values := valuesOf(env, variable); len(values) != 0 {
					t.Errorf("%s = %q, want it absent: avroc is not tracing, so a child of that trace would have no parent", variable, values)
				}
			}
		})
	}
}

// TestAGeneratorStillGetsEverythingElseFromAvrocsEnvironment is the same
// assertion as the unit test above, made where it can actually go wrong: avroc
// now builds cmd.Env, and a child whose environment is built is one that can
// silently lose the rest of it.
func TestAGeneratorStillGetsEverythingElseFromAvrocsEnvironment(t *testing.T) {
	t.Setenv("AVROC_TEST_UNRELATED", "reached the child")
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")

	run := generateWithEnvDump(t, newSpanCollector(t))

	for name, want := range map[string]string{
		"AVROC_TEST_UNRELATED": "reached the child",
		"SOURCE_DATE_EPOCH":    "1700000000",
	} {
		if got := onlyValue(t, run.generate, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// TestGeneratedBytesAreTheSameWithATraceparentOrWithout: the trace context is an
// input to nothing a generator writes. A run under CI, which has a TRACEPARENT,
// and the same run on a laptop, which does not, leave the same tree behind.
func TestGeneratedBytesAreTheSameWithATraceparentOrWithout(t *testing.T) {
	without := generateWithEnvDump(t, nil).tree

	t.Setenv(envTraceparent, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	t.Setenv(envTracestate, "upstream=value")
	with := generateWithEnvDump(t, newSpanCollector(t)).tree

	if len(without) == 0 {
		t.Fatal("the run generated nothing, so the comparison is vacuous")
	}
	if len(with) != len(without) {
		t.Fatalf("the traced run produced %d files and the untraced one %d", len(with), len(without))
	}
	for name, want := range without {
		if got := with[name]; got != want {
			t.Errorf("%q differs:\nwith a traceparent: %q\nwithout:            %q", name, got, want)
		}
	}
}

// envDumpRun is what one run of generateWithEnvDump observed: the environment
// each of the two invocations was handed, and the tree the run left behind.
type envDumpRun struct {
	handshake childEnvironment
	generate  childEnvironment
	tree      map[string]string
}

// generateWithEnvDump runs avroc generate over a one-schema project with a
// generator that writes down its environment.
//
// A nil collector is an untraced run, which is what an operator with no
// collector configured gets and is the case where the variables have to be
// removed rather than merely not set.
func generateWithEnvDump(t *testing.T, collector *spanCollector) envDumpRun {
	t.Helper()

	projectRoot := t.TempDir()
	dumpDir := t.TempDir()
	handshakeDump := filepath.Join(dumpDir, "handshake.env")
	generateDump := filepath.Join(dumpDir, "generate.env")

	generatorPath := envDumpingGenerator(t, handshakeDump, generateDump)

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

	env := map[string]string{"PATH": filepath.Dir(generatorPath)}
	if collector != nil {
		env["OTEL_EXPORTER_OTLP_ENDPOINT"] = collector.endpoint()
	}

	c := cli.Context{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Env: cli.EnvironmentFunc(func(key string) (string, bool) {
			value, ok := env[key]
			return value, ok
		}),
		OpenDir:    func(dir string) fs.FS { return os.DirFS(dir) },
		WorkingDir: projectRoot,
		Args:       []string{"generate"},
	}

	if code := Main(t.Context(), c); code != 0 {
		t.Fatalf("avroc generate exited %d", code)
	}

	return envDumpRun{
		handshake: readChildEnvironment(t, handshakeDump),
		generate:  readChildEnvironment(t, generateDump),
		tree:      treeOf(t, filepath.Join(projectRoot, "gen")),
	}
}

// envDumpingGenerator is a conforming plugin that writes its environment to the
// file naming the invocation it is in, and is otherwise the smallest generator
// that produces output.
//
// It ignores both variables, which is the acceptance criterion it is standing in
// for as well as the one it asserts: a generator that never reads TRACEPARENT
// behaves exactly as it did before #193.
func envDumpingGenerator(t *testing.T, handshakeDump, generateDump string) string {
	t.Helper()

	return writeNamedShellGenerator(t, "test", fmt.Sprintf(`set -e
if [ "$1" = "--plugin-info" ]; then
  env > '%s'
  printf '{"name":"test","version":"9.9.9","ir_version":%d,"options":[]}\n'
  exit 0
fi

env > '%s'

while [ $# -gt 0 ]; do
  case "$1" in
    --descriptor) shift 2 ;;
    --out) out=$2; shift 2 ;;
    --opt) shift 2 ;;
    *) echo "error: unexpected argument $1" >&2; exit 1 ;;
  esac
done

printf 'package avro\n' > "$out/generated.go"
`, handshakeDump, ir.Version, generateDump))
}

// childEnvironment is one process's environment as env(1) wrote it: NAME=VALUE
// per line, in whatever order the process received them.
type childEnvironment []string

func readChildEnvironment(t *testing.T, path string) childEnvironment {
	t.Helper()

	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return childEnvironment(strings.Split(strings.TrimSuffix(string(b), "\n"), "\n"))
}

// valuesOf is every value an environment holds for a variable, matched the way
// generatorEnv strips them — case-insensitively, so that a lower-case
// traceparent left behind is a failure rather than an invisible pass.
//
// Every value rather than one, because a duplicate is precisely the failure
// "replaced, not appended to" is about.
func valuesOf(env []string, name string) []string {
	var values []string
	for _, entry := range env {
		key, value, found := strings.Cut(entry, "=")
		if found && strings.EqualFold(key, name) {
			values = append(values, value)
		}
	}
	return values
}

// onlyValue is the one value an environment holds for a variable, and a failure
// if it holds none or more than one.
func onlyValue(t *testing.T, env []string, name string) string {
	t.Helper()

	values := valuesOf(env, name)
	if len(values) != 1 {
		t.Fatalf("the environment holds %d entries for %s (%q), want exactly one", len(values), name, values)
	}
	return values[0]
}

// spanNamed is the one exported span with a given name.
func spanNamed(t *testing.T, spans []*tracepb.Span, name string) *tracepb.Span {
	t.Helper()

	var found []*tracepb.Span
	for _, span := range spans {
		if span.GetName() == name {
			found = append(found, span)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the run exported %d spans named %q, want exactly one", len(found), name)
	}
	return found[0]
}

// recordedSpan is a real, sampled span for generatorEnv to propagate.
func recordedSpan(t *testing.T) (context.Context, oteltrace.Span) {
	t.Helper()

	provider := sdktrace.NewTracerProvider()
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return provider.Tracer(tracerScope).Start(t.Context(), "invocation")
}

// noopSpanContext is a context carrying the span an untraced run produces: one
// from the no-op provider, whose span context is not valid.
func noopSpanContext(t *testing.T) context.Context {
	t.Helper()

	ctx, span := noop.NewTracerProvider().Tracer(tracerScope).Start(t.Context(), "invocation")
	span.End()
	return ctx
}
