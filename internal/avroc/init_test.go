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

	"github.com/z5labs/avroc/internal/cli"
)

func initContext(workingDir string) cli.Context {
	return cli.Context{
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		OpenDir:    func(dir string) fs.FS { return os.DirFS(dir) },
		WorkingDir: workingDir,
	}
}

func TestRunInit(t *testing.T) {
	t.Run("scaffolds a valid manifest", func(t *testing.T) {
		dir := t.TempDir()

		if code := runInit(context.Background(), initContext(dir)); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}

		// The scaffold must be a manifest the loader accepts.
		m, err := loadManifest(initContext(dir), dir)
		if err != nil {
			t.Fatalf("scaffolded manifest does not load: %v", err)
		}
		if len(m.Generators) != 1 || m.Generators[0].Name != "go" {
			t.Errorf("scaffolded manifest = %+v", m)
		}
	})

	// The scaffold is where most adopters learn the manifest's shape, so it must
	// not offer a source or a version to fill in: avroc neither fetches a
	// generator nor pins one (#125). Read as text rather than through the
	// loader, because a field the struct no longer has would round-trip clean.
	t.Run("the scaffold names no removed OCI field", func(t *testing.T) {
		dir := t.TempDir()

		if code := runInit(context.Background(), initContext(dir)); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}

		data, err := os.ReadFile(filepath.Join(dir, manifestFilename))
		if err != nil {
			t.Fatal(err)
		}

		for _, field := range []string{"source", "version"} {
			if strings.Contains(string(data), field) {
				t.Errorf("scaffold mentions the removed %q field:\n%s", field, data)
			}
		}
	})

	t.Run("does not clobber an existing manifest", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, manifestFilename)

		existing := []byte(`{"generators":[{"name":"custom","out":"x"}]}`)
		if err := os.WriteFile(path, existing, 0o644); err != nil {
			t.Fatal(err)
		}

		if code := runInit(context.Background(), initContext(dir)); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(existing) {
			t.Errorf("existing manifest was modified:\n%s", got)
		}
	})
}
