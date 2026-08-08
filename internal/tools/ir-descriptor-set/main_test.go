// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/z5labs/avroc/avrocpb"
)

// TestWritesThePublishedBytes asserts the file this tool produces is exactly
// what avrocpb publishes. The artifact ends up attached to a release and copied
// into an image, so a tool that re-encoded, reordered or truncated on the way
// out would put a second definition of the IR into circulation under the same
// name.
func TestWritesThePublishedBytes(t *testing.T) {
	out := filepath.Join(t.TempDir(), "ir.binpb")

	if err := run([]string{"-o", out}); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read %s: %v", out, err)
	}

	want, err := avrocpb.MarshalFileDescriptorSet()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("wrote %d bytes, expected the %d published bytes", len(got), len(want))
	}
}

// TestCreatesTheParentDirectory covers the pipeline's case: an output path in a
// container filesystem where nothing has made the directory yet.
func TestCreatesTheParentDirectory(t *testing.T) {
	out := filepath.Join(t.TempDir(), "nested", "deeper", "ir.binpb")

	if err := run([]string{"-o", out}); err != nil {
		t.Fatalf("run: %v", err)
	}

	if _, err := os.Stat(out); err != nil {
		t.Fatalf("stat %s: %v", out, err)
	}
}

// TestRejectsAMissingOutputPath keeps the tool from having a default that
// writes somewhere nobody asked for.
func TestRejectsAMissingOutputPath(t *testing.T) {
	if err := run(nil); err == nil {
		t.Fatal("expected an error when -o is absent")
	}
}

// TestRejectsPositionalArguments makes a mistyped invocation fail loudly rather
// than silently ignoring the argument that was meant to be the output path.
func TestRejectsPositionalArguments(t *testing.T) {
	out := filepath.Join(t.TempDir(), "ir.binpb")

	if err := run([]string{"-o", out, "extra"}); err == nil {
		t.Fatal("expected an error for an unexpected positional argument")
	}
}
