// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/z5labs/avroc/internal/cli"

	"go.opentelemetry.io/otel"
)

// otlpEndpoint stands up somewhere for a traced run's spans to go, and returns
// what OTEL_EXPORTER_OTLP_ENDPOINT should be set to.
//
// It answers and discards. What an export holds is internal/telemetry's to
// check; what this package needs is an endpoint that exists, because a
// configured one is what makes a run traced.
func otlpEndpoint(t *testing.T) string {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	return server.URL
}

// tracedContext is a cli.Context whose environment configures tracing.
func tracedContext(t *testing.T, workingDir string, args ...string) cli.Context {
	t.Helper()

	env := map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": otlpEndpoint(t)}

	return cli.Context{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Env: cli.EnvironmentFunc(func(key string) (string, bool) {
			value, ok := env[key]
			return value, ok
		}),
		OpenDir:    func(dir string) fs.FS { return os.DirFS(dir) },
		WorkingDir: workingDir,
		Args:       args,
	}
}

// TestMainFlushesTracesOnEveryExitPath is the story's third requirement seen
// from the command's side.
//
// cmd/avroc calls os.Exit the moment Main returns, and os.Exit runs no deferred
// function — so the flush has to be reached before Main returns, on the paths
// that fail as much as on the one that succeeds. The observable form of "it was
// reached" is the provider itself: a tracer taken from a shut-down
// TracerProvider produces spans that do not record.
func TestMainFlushesTracesOnEveryExitPath(t *testing.T) {
	exits := map[string]struct {
		args []string
		code int
	}{
		"no arguments":          {nil, 1},
		"an unknown command":    {[]string{"bogus"}, 1},
		"help":                  {[]string{"help"}, 0},
		"a rejected argument":   {[]string{"generate", "schema.avdl"}, 1},
		"generate with no PATH": {[]string{"generate"}, 1},
	}

	for name, exit := range exits {
		t.Run(name, func(t *testing.T) {
			c := tracedContext(t, t.TempDir(), exit.args...)

			before := otel.GetTracerProvider()
			if code := Main(t.Context(), c); code != exit.code {
				t.Fatalf("exit code = %d, want %d", code, exit.code)
			}

			provider := otel.GetTracerProvider()
			if provider == before {
				t.Fatal("the run did not install a tracer provider, so this proves nothing about the flush")
			}

			_, span := provider.Tracer("avroc/test").Start(t.Context(), "after")
			span.End()
			if span.IsRecording() {
				t.Error("the tracer provider was still recording after Main returned: the flush was not reached")
			}
		})
	}
}

// TestMainFlushesTracesAfterTheRunIsCancelled is the same requirement on the one
// path that matters most: the run somebody pressed Ctrl-C on, whose context is
// cancelled before Main has finished with it.
func TestMainFlushesTracesAfterTheRunIsCancelled(t *testing.T) {
	c := tracedContext(t, t.TempDir(), "generate")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	before := otel.GetTracerProvider()
	if code := Main(ctx, c); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}

	provider := otel.GetTracerProvider()
	if provider == before {
		t.Fatal("the cancelled run did not install a tracer provider")
	}

	_, span := provider.Tracer("avroc/test").Start(context.Background(), "after")
	span.End()
	if span.IsRecording() {
		t.Error("the tracer provider was still recording after a cancelled run: the flush was not reached")
	}
}

// TestAMisconfiguredExporterDoesNotFailTheRun: a telemetry variable is not a
// reason to refuse to generate code. The run goes untraced and says so.
func TestAMisconfiguredExporterDoesNotFailTheRun(t *testing.T) {
	log, sink := bufferedLogger()

	c := cli.Context{
		Log: log,
		Env: cli.EnvironmentFunc(func(key string) (string, bool) {
			if key == "OTEL_EXPORTER_OTLP_ENDPOINT" {
				return "not a url", true
			}
			return "", false
		}),
		OpenDir:    staticOpenDir(fstest.MapFS{}),
		WorkingDir: t.TempDir(),
		Args:       []string{"help"},
	}

	if code := Main(t.Context(), c); code != 0 {
		t.Errorf("exit code = %d, want 0 — a telemetry misconfiguration is not a failed run", code)
	}
	if logged := sink.String(); !strings.Contains(logged, "tracing is disabled") {
		t.Errorf("the run did not say that tracing was disabled:\n%s", logged)
	}
}

// TestGeneratedBytesAreTheSameTracedOrNot is the determinism half of the story.
//
// Tracing is an observation of the run and never an input to it, so the tree a
// traced run leaves behind is the tree an untraced one leaves behind, byte for
// byte. Nothing in the generated output may reach for a trace id, a timestamp or
// an endpoint.
//
// The collector is a decoding one rather than somewhere to discard an export,
// because the comparison is only worth anything if the traced run was traced:
// with the spans of #192 on it, a run that exported nothing would pass this test
// while proving that tracing changes nothing about a run it never observed.
func TestGeneratedBytesAreTheSameTracedOrNot(t *testing.T) {
	collector := newSpanCollector(t)

	generate := func(t *testing.T, traced bool) map[string]string {
		t.Helper()

		projectRoot := t.TempDir()
		generatorPath := nameFromDescriptorGenerator(t)

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
		if traced {
			env["OTEL_EXPORTER_OTLP_ENDPOINT"] = collector.endpoint()
			env["OTEL_SERVICE_NAME"] = "avroc-under-test"
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
			t.Fatalf("avroc generate exited %d (traced: %t)", code, traced)
		}

		return treeOf(t, filepath.Join(projectRoot, "gen"))
	}

	untraced := generate(t, false)
	traced := generate(t, true)

	if len(untraced) == 0 {
		t.Fatal("the untraced run generated nothing, so the comparison is vacuous")
	}
	if names := collector.spanNames(t); len(names) == 0 {
		t.Fatal("the traced run exported no spans, so the comparison is vacuous")
	}
	if len(untraced) != len(traced) {
		t.Fatalf("the traced run produced %d files and the untraced one %d", len(traced), len(untraced))
	}
	for name, want := range untraced {
		got, ok := traced[name]
		if !ok {
			t.Errorf("the traced run did not produce %q", name)
			continue
		}
		if got != want {
			t.Errorf("%q differs between a traced and an untraced run:\ntraced:   %q\nuntraced: %q", name, got, want)
		}
	}
}

// treeOf reads every regular file beneath dir, keyed by its path relative to it.
func treeOf(t *testing.T, dir string) map[string]string {
	t.Helper()

	tree := make(map[string]string)
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		tree[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("failed to read %s: %v", dir, err)
	}
	return tree
}

// bufferedLogger is a logger a test can read back, safe against the SDK's own
// goroutine writing to it.
func bufferedLogger() (*slog.Logger, *lockedBuffer) {
	sink := &lockedBuffer{}
	return slog.New(slog.NewTextHandler(sink, nil)), sink
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
