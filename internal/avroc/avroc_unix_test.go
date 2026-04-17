// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

//go:build !windows

package avroc

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"testing/fstest"
)

func generatorFixtureName(base string) string { return base }

func generatorFixtureFile() *fstest.MapFile {
	return &fstest.MapFile{Mode: 0o755}
}

func TestLookupGenerators_Unix_SkipsNonExecutable(t *testing.T) {
	fsys := fstest.MapFS{
		"avroc-gen-go": &fstest.MapFile{Mode: 0o644},
	}

	got, err := lookupGenerators(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), staticOpenDir(fsys), "/bin")
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 0 {
		t.Fatalf("got %d generators, want 0 (file not executable)", len(got))
	}
}
