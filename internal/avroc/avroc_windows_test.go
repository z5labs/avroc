// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

//go:build windows

package avroc

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func generatorFixtureName(base string) string { return base + ".exe" }

func generatorFixtureFile() *fstest.MapFile {
	return &fstest.MapFile{}
}

func TestLookupGenerators_Windows_RequiresExeSuffix(t *testing.T) {
	fsys := fstest.MapFS{
		"avroc-gen-go":     &fstest.MapFile{},
		"avroc-gen-go.exe": &fstest.MapFile{},
	}

	got, err := lookupGenerators(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), staticOpenDir(fsys), `C:\bin`)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 {
		t.Fatalf("got %d generators, want 1", len(got))
	}
	if want := filepath.Join(`C:\bin`, "avroc-gen-go.exe"); got["avroc-gen-go"] != want {
		t.Errorf("avroc-gen-go path = %q, want %q", got["avroc-gen-go"], want)
	}
}

func TestLookupGenerators_Windows_CaseInsensitiveExt(t *testing.T) {
	fsys := fstest.MapFS{
		"avroc-gen-go.EXE": &fstest.MapFile{},
	}

	got, err := lookupGenerators(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), staticOpenDir(fsys), `C:\bin`)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 {
		t.Fatalf("got %d generators, want 1", len(got))
	}
	if _, ok := got["avroc-gen-go"]; !ok {
		t.Errorf("expected key avroc-gen-go, got keys: %v", got)
	}
}

func TestLookupGenerators_Windows_IgnoresExecBit(t *testing.T) {
	// On Windows the 0o111 bit is meaningless; a .exe file with no mode
	// bits set must still be discovered.
	fsys := fstest.MapFS{
		"avroc-gen-go.exe": &fstest.MapFile{Mode: 0},
	}

	got, err := lookupGenerators(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), staticOpenDir(fsys), `C:\bin`)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 {
		t.Fatalf("got %d generators, want 1 (exec bit must be ignored)", len(got))
	}
}
