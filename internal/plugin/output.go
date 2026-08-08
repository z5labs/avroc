// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// FileWriter is how a generator produces output: one call per file, naming a
// slash-separated path relative to --out and carrying that file's whole
// content.
//
// docs/plugin/SPEC.md asks a generator for files rather than for a stream —
// "everything the plugin leaves in that directory on a zero exit is the output
// of the run" — and this is that sentence as a Go interface. A file is written
// once, is complete when the call returns, and is never reopened, so there is
// no half-written state a generator can leave behind and no terminating marker
// it can forget to send.
//
// The path is slash-separated whatever the host's separator, because it is the
// same path avroc reports and merges; OutputPath converts it.
type FileWriter interface {
	// WriteFile writes content at path, relative to the output directory,
	// creating any directory the path names along the way.
	WriteFile(path string, content []byte) error
}

// OutputDir is the FileWriter Main hands a generator: every file written
// beneath one invocation's --out directory.
//
// It is exported because a generator's own tests write through it too. Running
// them against the real writer rather than a stand-in is what keeps "the
// generator produced these files" an assertion about the code that does the
// writing — the path escape, the directory creation and the refusal to write a
// path twice are all part of what a generation does, and a fake would have to
// re-implement them to be worth asserting against.
type OutputDir struct {
	out string

	written []string
	err     error
}

// NewOutputDir returns an OutputDir writing beneath out.
func NewOutputDir(out string) *OutputDir {
	return &OutputDir{out: out}
}

// WriteFile writes one whole file beneath the output directory.
//
// A path already written is refused rather than overwritten. --out is empty
// when the invocation starts, so the only way a path can arrive twice is a
// generator that produced the same file twice, and keeping the second silently
// would make the output depend on the order the generator happened to emit in.
func (d *OutputDir) WriteFile(path string, content []byte) error {
	// A linear scan rather than a set: a generator produces a handful of files,
	// and the slice is needed anyway to report them in emission order.
	if slices.Contains(d.written, path) {
		return d.record(fmt.Errorf("generator wrote %q twice", path))
	}

	dst, err := OutputPath(d.out, path)
	if err != nil {
		return d.record(fmt.Errorf("refusing to write %q: %w", path, err))
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return d.record(fmt.Errorf("failed to create output directory for %q: %w", dst, err))
	}
	if err := os.WriteFile(dst, content, 0o644); err != nil {
		// A partially written file is worse than no file: the next thing to
		// read it is a compiler, and half a source file is a confusing error a
		// long way from its cause. Removing it here is belt and braces — avroc
		// discards the whole scratch directory on a non-zero exit — but it
		// costs nothing and does not depend on the caller getting the exit
		// status right.
		_ = os.Remove(dst)
		return d.record(fmt.Errorf("failed to write %q: %w", dst, err))
	}

	d.written = append(d.written, path)
	return nil
}

// Written names the files this invocation produced, in the order the generator
// wrote them.
func (d *OutputDir) Written() []string { return d.written }

// Err reports the first failure WriteFile returned.
//
// Main checks it even when the generator returned no error of its own, so a
// generator that dropped a failed write on the floor fails the invocation
// rather than exiting zero with output it does not have. That matters more
// here than it usually would: a zero exit is the whole of the success signal,
// and avroc adopts whatever is in the directory on the strength of it.
func (d *OutputDir) Err() error { return d.err }

// record keeps the first error WriteFile returned and hands it back to the
// caller unchanged.
func (d *OutputDir) record(err error) error {
	if d.err == nil {
		d.err = err
	}
	return err
}
