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

func lockContext(fsys fs.FS) cli.Context {
	return cli.Context{
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		OpenDir: func(string) fs.FS { return fsys },
	}
}

func TestLoadLockfile(t *testing.T) {
	t.Run("parses a valid lockfile", func(t *testing.T) {
		fsys := fstest.MapFS{
			lockFilename: &fstest.MapFile{Data: []byte(`{
				"version": 1,
				"generators": [
					{"name": "go", "source": "ghcr.io/z5labs/avroc-gen-go", "version": "v0.1.0", "digest": "sha256:abc"}
				]
			}`)},
		}

		l, err := loadLockfile(lockContext(fsys), ".")
		if err != nil {
			t.Fatal(err)
		}
		if l.Version != 1 {
			t.Errorf("version = %d, want 1", l.Version)
		}
		if len(l.Generators) != 1 {
			t.Fatalf("generators = %d, want 1", len(l.Generators))
		}
		g := l.Generators[0]
		if g.Name != "go" || g.Source != "ghcr.io/z5labs/avroc-gen-go" || g.Version != "v0.1.0" || g.Digest != "sha256:abc" {
			t.Errorf("generator = %+v", g)
		}
	})

	t.Run("missing lockfile yields an empty lockfile, not an error", func(t *testing.T) {
		l, err := loadLockfile(lockContext(fstest.MapFS{}), ".")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if l == nil {
			t.Fatal("lockfile is nil")
		}
		if len(l.Generators) != 0 {
			t.Errorf("generators = %d, want 0", len(l.Generators))
		}
		if l.Version != lockfileVersion {
			t.Errorf("version = %d, want %d", l.Version, lockfileVersion)
		}
	})

	t.Run("rejects unknown fields", func(t *testing.T) {
		fsys := fstest.MapFS{
			lockFilename: &fstest.MapFile{Data: []byte(`{"version":1,"bogus":true,"generators":[]}`)},
		}
		if _, err := loadLockfile(lockContext(fsys), "."); err == nil {
			t.Fatal("expected an error for unknown field, got nil")
		}
	})

	t.Run("rejects trailing data", func(t *testing.T) {
		fsys := fstest.MapFS{
			lockFilename: &fstest.MapFile{Data: []byte(`{"version":1,"generators":[]}{"extra":1}`)},
		}
		if _, err := loadLockfile(lockContext(fsys), "."); err == nil {
			t.Fatal("expected an error for trailing data, got nil")
		}
	})

	t.Run("rejects a newer schema version", func(t *testing.T) {
		fsys := fstest.MapFS{
			lockFilename: &fstest.MapFile{Data: []byte(`{"version":999,"generators":[]}`)},
		}
		if _, err := loadLockfile(lockContext(fsys), "."); err == nil {
			t.Fatal("expected an error for a newer schema version, got nil")
		}
	})
}

func TestLockfileFind(t *testing.T) {
	l := &lockfile{
		Version: 1,
		Generators: []lockedGenerator{
			{Name: "go", Source: "ghcr.io/z5labs/avroc-gen-go", Version: "v0.1.0", Digest: "sha256:go"},
		},
	}

	t.Run("matches name, source, and version", func(t *testing.T) {
		got, ok := l.find("go", "ghcr.io/z5labs/avroc-gen-go", "v0.1.0")
		if !ok {
			t.Fatal("expected a match")
		}
		if got.Digest != "sha256:go" {
			t.Errorf("digest = %q, want sha256:go", got.Digest)
		}
	})

	t.Run("a changed version is drift, not a match", func(t *testing.T) {
		if _, ok := l.find("go", "ghcr.io/z5labs/avroc-gen-go", "v0.2.0"); ok {
			t.Error("expected no match for a different version")
		}
	})

	t.Run("a changed source is drift, not a match", func(t *testing.T) {
		if _, ok := l.find("go", "example.com/other", "v0.1.0"); ok {
			t.Error("expected no match for a different source")
		}
	})
}

func TestMarshalLock(t *testing.T) {
	t.Run("sorts generators by name and is deterministic", func(t *testing.T) {
		l := &lockfile{
			Version: 1,
			Generators: []lockedGenerator{
				{Name: "json", Source: "ghcr.io/z5labs/avroc-gen-json", Version: "v0.1.0", Digest: "sha256:json"},
				{Name: "go", Source: "ghcr.io/z5labs/avroc-gen-go", Version: "v0.1.0", Digest: "sha256:go"},
			},
		}

		first, err := marshalLock(l)
		if err != nil {
			t.Fatal(err)
		}
		second, err := marshalLock(l)
		if err != nil {
			t.Fatal(err)
		}
		if string(first) != string(second) {
			t.Fatalf("marshalLock not deterministic:\n%s\n---\n%s", first, second)
		}

		// "go" must sort before "json".
		want := `{
  "version": 1,
  "generators": [
    {
      "name": "go",
      "source": "ghcr.io/z5labs/avroc-gen-go",
      "version": "v0.1.0",
      "digest": "sha256:go"
    },
    {
      "name": "json",
      "source": "ghcr.io/z5labs/avroc-gen-json",
      "version": "v0.1.0",
      "digest": "sha256:json"
    }
  ]
}
`
		if string(first) != want {
			t.Errorf("marshalLock output:\n%s\nwant:\n%s", first, want)
		}
	})

	t.Run("does not mutate the input slice order", func(t *testing.T) {
		l := &lockfile{
			Version: 1,
			Generators: []lockedGenerator{
				{Name: "json"},
				{Name: "go"},
			},
		}
		if _, err := marshalLock(l); err != nil {
			t.Fatal(err)
		}
		if l.Generators[0].Name != "json" {
			t.Errorf("input slice was reordered: %+v", l.Generators)
		}
	})

	t.Run("round-trips through marshal then load", func(t *testing.T) {
		l := &lockfile{
			Version: 1,
			Generators: []lockedGenerator{
				{Name: "go", Source: "ghcr.io/z5labs/avroc-gen-go", Version: "v0.1.0", Digest: "sha256:go"},
			},
		}
		data, err := marshalLock(l)
		if err != nil {
			t.Fatal(err)
		}
		fsys := fstest.MapFS{lockFilename: &fstest.MapFile{Data: data}}
		got, err := loadLockfile(lockContext(fsys), ".")
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Generators) != 1 || got.Generators[0] != l.Generators[0] {
			t.Errorf("round-trip mismatch: %+v", got.Generators)
		}
	})
}
