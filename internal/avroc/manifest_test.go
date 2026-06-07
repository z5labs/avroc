// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"io"
	"io/fs"
	"log/slog"
	"testing"
	"testing/fstest"

	"github.com/z5labs/avroc/internal/cli"
)

func manifestContext(fsys fs.FS) cli.Context {
	return cli.Context{
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		OpenDir:    staticOpenDir(fsys),
		WorkingDir: ".",
	}
}

func TestLoadManifest(t *testing.T) {
	t.Run("parses a valid manifest", func(t *testing.T) {
		fsys := fstest.MapFS{
			manifestFilename: &fstest.MapFile{Data: []byte(`{
				"inputs": ["schemas/user.avdl"],
				"generators": [
					{
						"name": "go",
						"source": "ghcr.io/z5labs/avroc-gen-go",
						"version": "v0.1.0",
						"out": "gen/go",
						"options": {"package_name": "models"},
						"inputs": ["schemas/extra.avdl"]
					}
				]
			}`)},
		}

		m, err := loadManifest(manifestContext(fsys), ".")
		if err != nil {
			t.Fatal(err)
		}

		if len(m.Inputs) != 1 || m.Inputs[0] != "schemas/user.avdl" {
			t.Errorf("inputs = %v, want [schemas/user.avdl]", m.Inputs)
		}
		if len(m.Generators) != 1 {
			t.Fatalf("generators count = %d, want 1", len(m.Generators))
		}
		g := m.Generators[0]
		if g.Name != "go" || g.Out != "gen/go" {
			t.Errorf("generator = %+v, want name=go out=gen/go", g)
		}
		if g.Source != "ghcr.io/z5labs/avroc-gen-go" || g.Version != "v0.1.0" {
			t.Errorf("source/version = %q/%q", g.Source, g.Version)
		}
		if g.Options["package_name"] != "models" {
			t.Errorf("options = %v", g.Options)
		}
		if len(g.Inputs) != 1 || g.Inputs[0] != "schemas/extra.avdl" {
			t.Errorf("per-generator inputs = %v", g.Inputs)
		}
	})

	t.Run("rejects unknown fields", func(t *testing.T) {
		fsys := fstest.MapFS{
			manifestFilename: &fstest.MapFile{Data: []byte(`{
				"generators": [{"name": "go", "out": "gen", "typo": true}]
			}`)},
		}
		if _, err := loadManifest(manifestContext(fsys), "."); err == nil {
			t.Fatal("expected error for unknown field, got nil")
		}
	})

	t.Run("rejects a manifest with no generators", func(t *testing.T) {
		fsys := fstest.MapFS{
			manifestFilename: &fstest.MapFile{Data: []byte(`{"generators": []}`)},
		}
		if _, err := loadManifest(manifestContext(fsys), "."); err == nil {
			t.Fatal("expected error for empty generators, got nil")
		}
	})

	t.Run("rejects a generator missing name", func(t *testing.T) {
		fsys := fstest.MapFS{
			manifestFilename: &fstest.MapFile{Data: []byte(`{"generators": [{"out": "gen"}]}`)},
		}
		if _, err := loadManifest(manifestContext(fsys), "."); err == nil {
			t.Fatal("expected error for missing name, got nil")
		}
	})

	t.Run("rejects a generator missing out", func(t *testing.T) {
		fsys := fstest.MapFS{
			manifestFilename: &fstest.MapFile{Data: []byte(`{"generators": [{"name": "go"}]}`)},
		}
		if _, err := loadManifest(manifestContext(fsys), "."); err == nil {
			t.Fatal("expected error for missing out, got nil")
		}
	})

	t.Run("reports a missing manifest file", func(t *testing.T) {
		if _, err := loadManifest(manifestContext(fstest.MapFS{}), "."); err == nil {
			t.Fatal("expected error for missing manifest, got nil")
		}
	})
}

func TestGeneratorConfigOptions(t *testing.T) {
	g := GeneratorConfig{
		Options: map[string]string{
			"encoding":     "single_object",
			"package_name": "models",
		},
	}

	opts := g.options()
	if len(opts) != 2 {
		t.Fatalf("options count = %d, want 2", len(opts))
	}
	// Sorted by key: "encoding" before "package_name".
	if opts[0].GetName() != "encoding" || opts[0].GetValue() != "single_object" {
		t.Errorf("opts[0] = %q=%q", opts[0].GetName(), opts[0].GetValue())
	}
	if opts[1].GetName() != "package_name" || opts[1].GetValue() != "models" {
		t.Errorf("opts[1] = %q=%q", opts[1].GetName(), opts[1].GetValue())
	}
}

func TestMarshalManifest(t *testing.T) {
	m := &Manifest{
		Inputs: []string{"schemas/user.avdl"},
		Generators: []GeneratorConfig{
			{Name: "go", Out: "gen/go", Options: map[string]string{"package_name": "models"}},
		},
	}

	data, err := marshalManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Error("expected a trailing newline")
	}

	// The rendered manifest must round-trip back through the loader.
	fsys := fstest.MapFS{manifestFilename: &fstest.MapFile{Data: data}}
	got, err := loadManifest(manifestContext(fsys), ".")
	if err != nil {
		t.Fatalf("rendered manifest did not round-trip: %v", err)
	}
	if len(got.Generators) != 1 || got.Generators[0].Name != "go" {
		t.Errorf("round-tripped manifest = %+v", got)
	}
}
