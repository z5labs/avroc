// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/z5labs/avroc/internal/cli"
)

// fakeFetcher records calls and returns canned digests so the acquisition logic
// is tested without a registry. resolveCalls drives the reproducibility checks.
type fakeFetcher struct {
	digests      map[string]string // "source:version" -> digest
	resolveCalls int
	pullCalls    int
	pulled       []string
}

func (f *fakeFetcher) resolve(_ context.Context, ref string) (string, error) {
	f.resolveCalls++
	d, ok := f.digests[ref]
	if !ok {
		return "", fmt.Errorf("fakeFetcher: no image for %q", ref)
	}
	return d, nil
}

func (f *fakeFetcher) pull(_ context.Context, ref, _ string) (string, error) {
	f.pullCalls++
	f.pulled = append(f.pulled, ref)
	_, digest, ok := strings.Cut(ref, "@")
	if !ok {
		return "", fmt.Errorf("fakeFetcher: pull expects a digest reference, got %q", ref)
	}
	return digest, nil
}

func getContext(dir string) cli.Context {
	return cli.Context{
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Env:        cli.EnvironmentFunc(func(string) (string, bool) { return filepath.Join(dir, "cache"), true }),
		OpenDir:    func(d string) fs.FS { return os.DirFS(d) },
		WorkingDir: dir,
		Args:       []string{"get"},
	}
}

func writeManifest(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, manifestFilename), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readLock(t *testing.T, dir string) *lockfile {
	t.Helper()
	l, err := loadLockfile(getContext(dir), dir)
	if err != nil {
		t.Fatalf("loadLockfile: %v", err)
	}
	return l
}

func TestRunGet(t *testing.T) {
	const manifest = `{
		"inputs": ["schema.avdl"],
		"generators": [
			{"name": "go", "source": "ghcr.io/z5labs/avroc-gen-go", "version": "v0.1.0", "out": "gen"}
		]
	}`

	t.Run("resolves, pulls, and writes the lockfile", func(t *testing.T) {
		dir := t.TempDir()
		writeManifest(t, dir, manifest)
		f := &fakeFetcher{digests: map[string]string{
			"ghcr.io/z5labs/avroc-gen-go:v0.1.0": "sha256:aaa",
		}}

		if code := getWithFetcher(context.Background(), getContext(dir), f, false); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if f.resolveCalls != 1 || f.pullCalls != 1 {
			t.Errorf("resolveCalls=%d pullCalls=%d, want 1 and 1", f.resolveCalls, f.pullCalls)
		}
		if want := "ghcr.io/z5labs/avroc-gen-go@sha256:aaa"; len(f.pulled) != 1 || f.pulled[0] != want {
			t.Errorf("pulled = %v, want [%s]", f.pulled, want)
		}

		lock := readLock(t, dir)
		if len(lock.Generators) != 1 {
			t.Fatalf("locked generators = %d, want 1", len(lock.Generators))
		}
		got := lock.Generators[0]
		want := lockedGenerator{Name: "go", Source: "ghcr.io/z5labs/avroc-gen-go", Version: "v0.1.0", Digest: "sha256:aaa"}
		if got != want {
			t.Errorf("locked = %+v, want %+v", got, want)
		}
	})

	t.Run("reruns are reproducible: pinned digest reused, no re-resolve", func(t *testing.T) {
		dir := t.TempDir()
		writeManifest(t, dir, manifest)

		first := &fakeFetcher{digests: map[string]string{"ghcr.io/z5labs/avroc-gen-go:v0.1.0": "sha256:aaa"}}
		if code := getWithFetcher(context.Background(), getContext(dir), first, false); code != 0 {
			t.Fatalf("first get exit = %d", code)
		}

		// A registry that would now resolve to a *different* digest must not be
		// consulted: the pinned digest is reused.
		second := &fakeFetcher{digests: map[string]string{"ghcr.io/z5labs/avroc-gen-go:v0.1.0": "sha256:MOVED"}}
		if code := getWithFetcher(context.Background(), getContext(dir), second, false); code != 0 {
			t.Fatalf("second get exit = %d", code)
		}
		if second.resolveCalls != 0 {
			t.Errorf("resolveCalls = %d on rerun, want 0 (pin reused)", second.resolveCalls)
		}
		if d := readLock(t, dir).Generators[0].Digest; d != "sha256:aaa" {
			t.Errorf("digest moved to %q on rerun, want sha256:aaa", d)
		}
	})

	t.Run("-upgrade re-resolves and rewrites the pin", func(t *testing.T) {
		dir := t.TempDir()
		writeManifest(t, dir, manifest)

		first := &fakeFetcher{digests: map[string]string{"ghcr.io/z5labs/avroc-gen-go:v0.1.0": "sha256:aaa"}}
		if code := getWithFetcher(context.Background(), getContext(dir), first, false); code != 0 {
			t.Fatalf("first get exit = %d", code)
		}

		upgraded := &fakeFetcher{digests: map[string]string{"ghcr.io/z5labs/avroc-gen-go:v0.1.0": "sha256:bbb"}}
		if code := getWithFetcher(context.Background(), getContext(dir), upgraded, true); code != 0 {
			t.Fatalf("upgrade get exit = %d", code)
		}
		if upgraded.resolveCalls != 1 {
			t.Errorf("resolveCalls = %d on -upgrade, want 1", upgraded.resolveCalls)
		}
		if d := readLock(t, dir).Generators[0].Digest; d != "sha256:bbb" {
			t.Errorf("digest = %q after -upgrade, want sha256:bbb", d)
		}
	})

	t.Run("a changed version is drift: re-resolves without -upgrade", func(t *testing.T) {
		dir := t.TempDir()
		writeManifest(t, dir, manifest)

		first := &fakeFetcher{digests: map[string]string{"ghcr.io/z5labs/avroc-gen-go:v0.1.0": "sha256:aaa"}}
		if code := getWithFetcher(context.Background(), getContext(dir), first, false); code != 0 {
			t.Fatalf("first get exit = %d", code)
		}

		// Bump the version in the manifest; the old pin no longer matches.
		writeManifest(t, dir, strings.Replace(manifest, "v0.1.0", "v0.2.0", 1))
		bumped := &fakeFetcher{digests: map[string]string{"ghcr.io/z5labs/avroc-gen-go:v0.2.0": "sha256:ccc"}}
		if code := getWithFetcher(context.Background(), getContext(dir), bumped, false); code != 0 {
			t.Fatalf("bumped get exit = %d", code)
		}
		if bumped.resolveCalls != 1 {
			t.Errorf("resolveCalls = %d after version bump, want 1 (drift)", bumped.resolveCalls)
		}
		got := readLock(t, dir).Generators[0]
		if got.Version != "v0.2.0" || got.Digest != "sha256:ccc" {
			t.Errorf("locked = %+v, want version v0.2.0 digest sha256:ccc", got)
		}
	})

	t.Run("skips PATH-only generators and writes no lockfile when none have a source", func(t *testing.T) {
		dir := t.TempDir()
		writeManifest(t, dir, `{"generators":[{"name":"json","out":"."}]}`)
		f := &fakeFetcher{}

		if code := getWithFetcher(context.Background(), getContext(dir), f, false); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if f.resolveCalls != 0 || f.pullCalls != 0 {
			t.Errorf("fetcher called for PATH-only generator: resolve=%d pull=%d", f.resolveCalls, f.pullCalls)
		}
		if _, err := os.Stat(filepath.Join(dir, lockFilename)); !os.IsNotExist(err) {
			t.Errorf("expected no lockfile, stat err = %v", err)
		}
	})

	t.Run("errors when a generator has a source but no version", func(t *testing.T) {
		dir := t.TempDir()
		writeManifest(t, dir, `{"generators":[{"name":"go","source":"ghcr.io/z5labs/avroc-gen-go","out":"gen"}]}`)
		if code := getWithFetcher(context.Background(), getContext(dir), &fakeFetcher{}, false); code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
	})

	t.Run("lockfile output is byte-deterministic across runs", func(t *testing.T) {
		dir := t.TempDir()
		writeManifest(t, dir, `{
			"generators": [
				{"name": "json", "source": "ghcr.io/z5labs/avroc-gen-json", "version": "v0.1.0", "out": "."},
				{"name": "go", "source": "ghcr.io/z5labs/avroc-gen-go", "version": "v0.1.0", "out": "gen"}
			]
		}`)
		f := &fakeFetcher{digests: map[string]string{
			"ghcr.io/z5labs/avroc-gen-go:v0.1.0":   "sha256:go",
			"ghcr.io/z5labs/avroc-gen-json:v0.1.0": "sha256:json",
		}}
		if code := getWithFetcher(context.Background(), getContext(dir), f, false); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		got, err := os.ReadFile(filepath.Join(dir, lockFilename))
		if err != nil {
			t.Fatal(err)
		}
		// "go" must precede "json" regardless of manifest order.
		if !strings.Contains(string(got), `"digest": "sha256:go"`) ||
			strings.Index(string(got), "sha256:go") > strings.Index(string(got), "sha256:json") {
			t.Errorf("lockfile not sorted by name:\n%s", got)
		}
	})

	t.Run("-upgrade flag parses via runGet", func(t *testing.T) {
		dir := t.TempDir()
		writeManifest(t, dir, `{"generators":[{"name":"json","out":"."}]}`)
		c := getContext(dir)
		c.Args = []string{"get", "-upgrade"}
		if code := runGet(context.Background(), c); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	})

	t.Run("an unknown flag is rejected", func(t *testing.T) {
		c := getContext(t.TempDir())
		c.Args = []string{"get", "-nope"}
		if code := runGet(context.Background(), c); code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
	})

	t.Run("extra positional arguments are rejected", func(t *testing.T) {
		dir := t.TempDir()
		writeManifest(t, dir, `{"generators":[{"name":"json","out":"."}]}`)
		c := getContext(dir)
		c.Args = []string{"get", "foo"}
		if code := runGet(context.Background(), c); code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
	})

	t.Run("removes a stale lockfile when no generators have a source", func(t *testing.T) {
		dir := t.TempDir()

		// First acquire with an OCI source so a lockfile exists.
		writeManifest(t, dir, manifest)
		f := &fakeFetcher{digests: map[string]string{"ghcr.io/z5labs/avroc-gen-go:v0.1.0": "sha256:aaa"}}
		if code := getWithFetcher(context.Background(), getContext(dir), f, false); code != 0 {
			t.Fatalf("first get exit = %d", code)
		}
		if _, err := os.Stat(filepath.Join(dir, lockFilename)); err != nil {
			t.Fatalf("expected lockfile after first get: %v", err)
		}

		// Remove the OCI source from the manifest and re-run: the lockfile is now
		// stale and must be deleted.
		writeManifest(t, dir, `{"generators":[{"name":"json","out":"."}]}`)
		if code := getWithFetcher(context.Background(), getContext(dir), &fakeFetcher{}, false); code != 0 {
			t.Fatalf("second get exit = %d", code)
		}
		if _, err := os.Stat(filepath.Join(dir, lockFilename)); !os.IsNotExist(err) {
			t.Errorf("expected stale lockfile to be removed, stat err = %v", err)
		}
	})
}

func TestMain_DispatchGet(t *testing.T) {
	// Routes "get" through Main end-to-end: a PATH-only manifest acquires
	// nothing and exits 0, proving dispatch is wired and not rejected as extra
	// args.
	dir := t.TempDir()
	writeManifest(t, dir, `{"generators":[{"name":"json","out":"."}]}`)
	if code := Main(context.Background(), getContext(dir)); code != 0 {
		t.Fatalf("Main(get) exit = %d, want 0", code)
	}
}
