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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/z5labs/avroc/internal/cli"
)

const sampleIDL = `namespace com.example;
schema User;
record User {
  string name;
}
`

// writeIDL writes a sample IDL file under dir and returns its base name.
func writeIDL(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(sampleIDL), 0o644); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestPlanGenerators(t *testing.T) {
	t.Run("resolves a local generator with composed, deduped inputs", func(t *testing.T) {
		dir := t.TempDir()
		shared := writeIDL(t, dir, "shared.avdl")
		own := writeIDL(t, dir, "own.avdl")

		m := &Manifest{
			Inputs: []string{shared},
			Generators: []GeneratorConfig{
				{
					Name:    "go",
					Out:     "gen/go",
					Options: map[string]string{"package_name": "models"},
					// shared is repeated here; it must be deduped to one schema.
					Inputs: []string{own, shared},
				},
			},
		}
		generators := map[string]string{"avroc-gen-go": "/fake/avroc-gen-go"}

		tasks, err := planGenerators(m, generators, dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(tasks) != 1 {
			t.Fatalf("tasks = %d, want 1", len(tasks))
		}

		task := tasks[0]
		if task.name != "avroc-gen-go" {
			t.Errorf("name = %q, want avroc-gen-go", task.name)
		}
		if task.executablePath != "/fake/avroc-gen-go" {
			t.Errorf("executablePath = %q", task.executablePath)
		}
		if want := filepath.Join(dir, "gen/go"); task.output != want {
			t.Errorf("output = %q, want %q", task.output, want)
		}
		if len(task.options) != 1 || task.options[0].GetName() != "package_name" {
			t.Errorf("options = %v", task.options)
		}
		// shared + own + shared(dup) => 2 schemas.
		if len(task.schemas) != 2 {
			t.Errorf("schemas = %d, want 2 (deduped)", len(task.schemas))
		}
	})

	t.Run("parses a shared input once across generators", func(t *testing.T) {
		dir := t.TempDir()
		shared := writeIDL(t, dir, "shared.avdl")

		m := &Manifest{
			Inputs: []string{shared},
			Generators: []GeneratorConfig{
				{Name: "go", Out: "gen/go"},
				{Name: "json", Out: "gen/json"},
			},
		}
		generators := map[string]string{
			"avroc-gen-go":   "/fake/avroc-gen-go",
			"avroc-gen-json": "/fake/avroc-gen-json",
		}

		tasks, err := planGenerators(m, generators, dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(tasks) != 2 {
			t.Fatalf("tasks = %d, want 2", len(tasks))
		}
		// The cache must hand both generators the very same schema value.
		if tasks[0].schemas[0] != tasks[1].schemas[0] {
			t.Error("shared input was parsed more than once (schema not cached)")
		}
	})

	t.Run("errors when a generator is not on PATH", func(t *testing.T) {
		m := &Manifest{
			Generators: []GeneratorConfig{
				{Name: "go", Out: "gen", Inputs: []string{"x.avdl"}},
			},
		}

		_, err := planGenerators(m, map[string]string{}, t.TempDir())
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
		if !strings.Contains(err.Error(), "avroc-gen-go") {
			t.Errorf("error should name the missing generator, got: %v", err)
		}
	})

	t.Run("errors when a generator has no inputs", func(t *testing.T) {
		m := &Manifest{
			Generators: []GeneratorConfig{{Name: "go", Out: "gen"}},
		}
		generators := map[string]string{"avroc-gen-go": "/fake/avroc-gen-go"}

		if _, err := planGenerators(m, generators, t.TempDir()); err == nil {
			t.Fatal("expected an error for missing inputs, got nil")
		}
	})
}

func TestRunGenerate_MissingManifest(t *testing.T) {
	ctx := cli.Context{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Env: cli.EnvironmentFunc(func(key string) (string, bool) {
			if key == "PATH" {
				return "/nonexistent", true
			}
			return "", false
		}),
		OpenDir:    func(dir string) fs.FS { return os.DirFS(dir) },
		WorkingDir: t.TempDir(), // empty dir: no avroc.json
	}

	if code := runGenerate(context.Background(), ctx); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

func TestRunGenerate_PathNotSet(t *testing.T) {
	ctx := cli.Context{
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Env:     cli.EnvironmentFunc(func(string) (string, bool) { return "", false }),
		OpenDir: staticOpenDir(fstest.MapFS{}),
	}

	if code := runGenerate(context.Background(), ctx); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}
