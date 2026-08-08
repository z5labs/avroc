// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package plugin

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestOutputDir(t *testing.T) {
	t.Run("writes a file beneath the output directory", func(t *testing.T) {
		out := t.TempDir()
		d := NewOutputDir(out)

		if err := d.WriteFile("user.go", []byte("package pkg\n")); err != nil {
			t.Fatal(err)
		}

		got, err := os.ReadFile(filepath.Join(out, "user.go"))
		if err != nil {
			t.Fatal(err)
		}
		if want := "package pkg\n"; string(got) != want {
			t.Errorf("content = %q, want %q", string(got), want)
		}
	})

	t.Run("creates the directories a path names", func(t *testing.T) {
		// docs/plugin/SPEC.md lets a plugin create subdirectories beneath --out
		// to whatever depth it needs, and says nothing about creating them
		// first: a path is the whole of what a generator provides.
		out := t.TempDir()
		d := NewOutputDir(out)

		if err := d.WriteFile("a/b/c/user.go", []byte("package c\n")); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(out, "a", "b", "c", "user.go")); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a path is slash-separated whatever the host uses", func(t *testing.T) {
		out := t.TempDir()
		d := NewOutputDir(out)

		if err := d.WriteFile("pkg/user.go", nil); err != nil {
			t.Fatal(err)
		}
		// The path reported back is the one the generator gave, not the host's
		// spelling of it: avroc reports and merges on the same string.
		if got := d.Written(); !slices.Equal(got, []string{"pkg/user.go"}) {
			t.Errorf("Written() = %v, want [pkg/user.go]", got)
		}
	})

	t.Run("reports what was written, in the order it was written", func(t *testing.T) {
		d := NewOutputDir(t.TempDir())

		for _, p := range []string{"b.go", "a.go", "c.go"} {
			if err := d.WriteFile(p, nil); err != nil {
				t.Fatal(err)
			}
		}
		if got, want := d.Written(), []string{"b.go", "a.go", "c.go"}; !slices.Equal(got, want) {
			t.Errorf("Written() = %v, want %v", got, want)
		}
	})

	t.Run("refuses a path outside the output directory", func(t *testing.T) {
		out := t.TempDir()
		d := NewOutputDir(out)

		for _, bad := range []string{"../escape.go", "/etc/escape.go", "a/../../escape.go"} {
			if err := d.WriteFile(bad, []byte("package pkg\n")); err == nil {
				t.Errorf("WriteFile accepted %q", bad)
			}
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(out), "escape.go")); !os.IsNotExist(err) {
			t.Error("a file escaped the output directory")
		}
		if len(d.Written()) != 0 {
			t.Errorf("a refused path was reported as output: %v", d.Written())
		}
	})

	t.Run("refuses a path already written", func(t *testing.T) {
		// --out is empty when the invocation starts, so a repeated path is a
		// generator producing the same file twice. Overwriting would make the
		// output depend on which of the two it happened to emit last.
		d := NewOutputDir(t.TempDir())

		if err := d.WriteFile("user.go", []byte("first\n")); err != nil {
			t.Fatal(err)
		}
		if err := d.WriteFile("user.go", []byte("second\n")); err == nil {
			t.Error("WriteFile overwrote a path it had already written")
		}
	})

	t.Run("reports a directory it cannot create", func(t *testing.T) {
		out := t.TempDir()
		if err := os.WriteFile(filepath.Join(out, "a"), nil, 0o644); err != nil {
			t.Fatal(err)
		}

		d := NewOutputDir(out)
		if err := d.WriteFile("a/user.go", []byte("package pkg\n")); err == nil {
			t.Error("WriteFile reported success for a directory it could not create")
		}
	})

	t.Run("reports a file it cannot write, and leaves nothing behind", func(t *testing.T) {
		out := t.TempDir()
		d := NewOutputDir(out)

		// "a" is a directory by the time the second write names it as a file,
		// so the write fails after the path check and the directory creation
		// have both passed — the one place a partial file could come from.
		if err := d.WriteFile("a/user.go", []byte("package pkg\n")); err != nil {
			t.Fatal(err)
		}
		if err := d.WriteFile("a", []byte("package pkg\n")); err == nil {
			t.Fatal("WriteFile reported success for a path it could not write")
		}
		if got := d.Written(); !slices.Equal(got, []string{"a/user.go"}) {
			t.Errorf("Written() = %v, want only the file that was written", got)
		}
		// The directory is still a directory: a failed write does not remove
		// something it did not create.
		info, err := os.Stat(filepath.Join(out, "a"))
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() {
			t.Error("a failed write replaced a directory")
		}
	})

	t.Run("keeps the first failure for Main to find", func(t *testing.T) {
		d := NewOutputDir(t.TempDir())

		if d.Err() != nil {
			t.Fatalf("Err() = %v before anything was written", d.Err())
		}
		if err := d.WriteFile("user.go", nil); err != nil {
			t.Fatal(err)
		}
		if d.Err() != nil {
			t.Fatalf("Err() = %v after a write that succeeded", d.Err())
		}

		first := d.WriteFile("../escape.go", nil)
		if first == nil {
			t.Fatal("WriteFile accepted a path outside the output directory")
		}
		_ = d.WriteFile("/absolute.go", nil)

		// The first, not the last: it is the one that says what went wrong,
		// and every failure after it may be a consequence of it.
		if d.Err() != first {
			t.Errorf("Err() = %v, want the first failure %v", d.Err(), first)
		}
	})
}
