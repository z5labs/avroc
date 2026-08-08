// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func TestSafeOutputPath(t *testing.T) {
	// safeOutputPath returns an absolute path (it resolves root via
	// filepath.Abs), so an already-absolute root is what it returns paths
	// under, unchanged.
	const root = "/out"

	t.Run("accepts relative paths", func(t *testing.T) {
		for _, p := range []string{"person.go", "pkg/person.go", "a/b/c.avsc"} {
			got, err := safeOutputPath(root, p)
			if err != nil {
				t.Errorf("path %q: unexpected error: %v", p, err)
				continue
			}
			want := filepath.Join(root, filepath.FromSlash(p))
			if got != want {
				t.Errorf("path %q: got %q, want %q", p, got, want)
			}
		}
	})

	t.Run("rejects unsafe paths", func(t *testing.T) {
		for _, p := range []string{"", "../escape.go", "a/../../escape.go", "/etc/passwd", "pkg/../../escape.go"} {
			if _, err := safeOutputPath(root, p); err == nil {
				t.Errorf("path %q: expected error, got nil", p)
			}
		}
	})
}

// TestNewScratchDir is what a plugin is allowed to assume about --out: it
// exists, it is a directory, it is writable, and it is empty.
func TestNewScratchDir(t *testing.T) {
	output := t.TempDir()
	if err := os.WriteFile(filepath.Join(output, "from-a-previous-run.go"), []byte("package old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	scratch, err := newScratchDir(output, "avroc-gen-go")
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(scratch)
	if err != nil {
		t.Fatalf("the scratch directory does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("the scratch directory %q is not a directory", scratch)
	}

	// Empty even though the project tree it lives in is not: a plugin MUST NOT
	// expect a file it wrote on a previous run to be present, and this is what
	// makes that true rather than merely required.
	entries, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the scratch directory is not empty: %v", entries)
	}

	if err := os.WriteFile(filepath.Join(scratch, "user.go"), []byte("package avro\n"), 0o644); err != nil {
		t.Errorf("the scratch directory is not writable: %v", err)
	}

	// Two invocations of the same generator never share one, which is what lets
	// generators run concurrently into the same declared output directory.
	other, err := newScratchDir(output, "avroc-gen-go")
	if err != nil {
		t.Fatal(err)
	}
	if other == scratch {
		t.Errorf("two invocations were handed the same scratch directory %q", scratch)
	}
}

// TestMergeOutput is the merge itself: what the generator left in its scratch
// directory arrives in the project tree, at the same relative paths, with the
// same bytes, and the scratch directory is not what any of it is reported
// against.
func TestMergeOutput(t *testing.T) {
	output := t.TempDir()
	scratch, err := newScratchDir(output, "avroc-gen-go")
	if err != nil {
		t.Fatal(err)
	}

	writeScratch(t, scratch, "user.go", "package avro // user\n")
	writeScratch(t, scratch, "pkg/nested/order.go", "package nested // order\n")
	// A directory with nothing in it is not output: the generator produced no
	// file there, and a merge that created it would leave a person wondering.
	if err := os.MkdirAll(filepath.Join(scratch, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	merged, err := mergeOutput(scratch, output)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"pkg/nested/order.go", "user.go"}
	if !slices.Equal(merged, want) {
		t.Errorf("mergeOutput reported %q, want %q", merged, want)
	}

	for path, contents := range map[string]string{
		"user.go":             "package avro // user\n",
		"pkg/nested/order.go": "package nested // order\n",
	} {
		b, err := os.ReadFile(filepath.Join(output, filepath.FromSlash(path)))
		if err != nil {
			t.Errorf("%q did not reach the output tree: %v", path, err)
			continue
		}
		if string(b) != contents {
			t.Errorf("%q = %q, want %q", path, b, contents)
		}
	}

	if _, err := os.Stat(filepath.Join(output, "empty")); err == nil {
		t.Error("an empty directory in the scratch directory was merged as output")
	}

	// Everything moved rather than copied, so the scratch directory a caller
	// then removes holds nothing that only exists there.
	if _, err := os.Stat(filepath.Join(scratch, "user.go")); err == nil {
		t.Error("the merge left the generated file in the scratch directory as well")
	}
}

// TestMergeOutputReplacesAnExistingFile is regeneration: the second run's file
// is the one in the tree afterwards, whole, with nothing of the first left
// appended to it.
func TestMergeOutputReplacesAnExistingFile(t *testing.T) {
	output := t.TempDir()
	existing := filepath.Join(output, "user.go")
	if err := os.WriteFile(existing, []byte("package avro // the previous run, which was longer\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	scratch, err := newScratchDir(output, "avroc-gen-go")
	if err != nil {
		t.Fatal(err)
	}
	writeScratch(t, scratch, "user.go", "package avro // new\n")

	if _, err := mergeOutput(scratch, output); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), "package avro // new\n"; got != want {
		t.Errorf("user.go = %q, want %q", got, want)
	}
}

// TestMergeOutputRefusesToEscape is avroc enforcing docs/plugin/SPEC.md's
// boundary rather than trusting it. A symbolic link is the case a check on the
// relative path alone does not catch: every path here is beneath the scratch
// directory, and following one would still write outside the project tree.
func TestMergeOutputRefusesToEscape(t *testing.T) {
	t.Run("a symbolic link to a file", func(t *testing.T) {
		output := t.TempDir()
		outside := filepath.Join(t.TempDir(), "secret")
		if err := os.WriteFile(outside, []byte("not the generator's\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		scratch, err := newScratchDir(output, "avroc-gen-go")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(scratch, "user.go")); err != nil {
			t.Fatal(err)
		}

		assertRefused(t, output, scratch, "user.go")
	})

	t.Run("a directory the generator wrote through a symbolic link", func(t *testing.T) {
		output := t.TempDir()
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "escaped.go"), []byte("package escaped\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		scratch, err := newScratchDir(output, "avroc-gen-go")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(scratch, "pkg")); err != nil {
			t.Fatal(err)
		}

		assertRefused(t, output, scratch, "pkg")
	})

	t.Run("a named pipe", func(t *testing.T) {
		output := t.TempDir()
		scratch, err := newScratchDir(output, "avroc-gen-go")
		if err != nil {
			t.Fatal(err)
		}
		// A pipe would make the cross-filesystem copy block on a writer that is
		// never coming, so it is refused for a second reason as well as not
		// being a file the generator produced.
		if err := syscall.Mkfifo(filepath.Join(scratch, "user.go"), 0o644); err != nil {
			t.Skipf("cannot create a named pipe here: %v", err)
		}

		assertRefused(t, output, scratch, "user.go")
	})
}

// assertRefused runs a merge that must fail, naming the entry it refused, and
// checks that the project tree is untouched by the attempt.
func assertRefused(t *testing.T, output, scratch, name string) {
	t.Helper()

	merged, err := mergeOutput(scratch, output)
	if err == nil {
		t.Fatalf("mergeOutput accepted %q and merged %q", name, merged)
	}
	if !strings.Contains(err.Error(), name) {
		t.Errorf("error %q does not name the entry it refused", err)
	}

	entries, err := os.ReadDir(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Join(output, e.Name()) != scratch {
			t.Errorf("a refused merge left %q in the output tree", e.Name())
		}
	}
}

// TestMergeOutputCreatesNothingWhenItRefuses is the two-phase split: a plan that
// fails on one file must not have moved any of the others, whatever order the
// filesystem hands them over in.
func TestMergeOutputCreatesNothingWhenItRefuses(t *testing.T) {
	output := t.TempDir()
	scratch, err := newScratchDir(output, "avroc-gen-go")
	if err != nil {
		t.Fatal(err)
	}

	// Named so that either sorts first, because the refusal has to hold whichever
	// the walk reaches before the other.
	writeScratch(t, scratch, "aaa.go", "package avro // fine\n")
	writeScratch(t, scratch, "zzz.go", "package avro // also fine\n")
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), filepath.Join(scratch, "mmm.go")); err != nil {
		t.Fatal(err)
	}

	if _, err := mergeOutput(scratch, output); err == nil {
		t.Fatal("mergeOutput accepted a scratch directory holding a symbolic link")
	}

	for _, name := range []string{"aaa.go", "zzz.go"} {
		if _, err := os.Stat(filepath.Join(output, name)); err == nil {
			t.Errorf("%q was merged even though the merge failed", name)
		}
	}
}

// TestCopyIntoPlace covers moveIntoPlace's cross-filesystem case directly: the
// EXDEV branch cannot be provoked from a test without a second filesystem, so
// the copy it falls back to is exercised on its own terms.
func TestCopyIntoPlace(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("package avro\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "dst")
	if err := copyIntoPlace(src, dst); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), "package avro\n"; got != want {
		t.Errorf("dst = %q, want %q", got, want)
	}

	// The mode the generator gave the file, not the one CreateTemp chose: a
	// generator that emitted a script emitted an executable one.
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("dst mode = %v, want %v", got, os.FileMode(0o755))
	}

	// A move on both sides of a mount point: the source is gone, so the
	// "everything moved out of the scratch directory" invariant does not depend
	// on which branch moveIntoPlace took.
	if _, err := os.Stat(src); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the copy left its source behind: %v", err)
	}

	// And nothing staged is left beside the destination, so a merge does not
	// litter the project tree with temporary files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "dst" {
		t.Errorf("directory holds %v, want just dst", entries)
	}
}

func writeScratch(t *testing.T, scratch, name, contents string) {
	t.Helper()

	path := filepath.Join(scratch, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
