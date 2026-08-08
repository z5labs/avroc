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

	merged, err := mergeOne(scratch, output)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"pkg/nested/order.go", "user.go"}
	if !slices.Equal(merged, want) {
		t.Errorf("the merge reported %q, want %q", merged, want)
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

	if _, err := mergeOne(scratch, output); err != nil {
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

	merged, err := mergeOne(scratch, output)
	if err == nil {
		t.Fatalf("the merge accepted %q and merged %q", name, merged)
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

	if _, err := mergeOne(scratch, output); err == nil {
		t.Fatal("the merge accepted a scratch directory holding a symbolic link")
	}

	for _, name := range []string{"aaa.go", "zzz.go"} {
		if _, err := os.Stat(filepath.Join(output, name)); err == nil {
			t.Errorf("%q was merged even though the merge failed", name)
		}
	}
}

// TestCheckCollisions is the report #118 asks for: two generators producing one
// file is an error naming both of them and the path, and it is the same error
// whichever of them avroc heard from first.
func TestCheckCollisions(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "pkg")
	collision := filepath.Join(pkg, "user.avsc")

	t.Run("two generators producing one file", func(t *testing.T) {
		// A different relative path under a different output directory, resolving
		// to the same file: this is the collision that is only visible in the
		// destination, and it is the one the example manifest is a single edit
		// away from — json at "." and pcf at "pcf/", both emitting .avsc.
		json := &generatorOutput{generator: "avroc-gen-json", output: root, files: []mergedFile{
			{rel: "pkg/user.avsc", dst: collision},
		}}
		pcf := &generatorOutput{generator: "avroc-gen-pcf", output: pkg, files: []mergedFile{
			{rel: "user.avsc", dst: collision},
		}}

		err := checkCollisions([]*generatorOutput{json, pcf})
		if err == nil {
			t.Fatal("checkCollisions accepted two generators producing the same file")
		}
		for _, want := range []string{"avroc-gen-json", "avroc-gen-pcf", collision} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %q", err, want)
			}
		}

		// The report is a function of the plans and not of the schedule: the
		// generator that finished first is the one that arrives first here, and it
		// must not change a character of what the user is told.
		reversed := checkCollisions([]*generatorOutput{pcf, json})
		if reversed == nil {
			t.Fatal("checkCollisions accepted the same collision in the other order")
		}
		if reversed.Error() != err.Error() {
			t.Errorf("report = %q in one order and %q in the other", err, reversed)
		}
	})

	t.Run("the same relative path under different output directories does not collide", func(t *testing.T) {
		a := &generatorOutput{generator: "avroc-gen-go", output: root, files: []mergedFile{
			{rel: "user.go", dst: filepath.Join(root, "user.go")},
		}}
		b := &generatorOutput{generator: "avroc-gen-json", output: pkg, files: []mergedFile{
			{rel: "user.go", dst: filepath.Join(pkg, "user.go")},
		}}

		if err := checkCollisions([]*generatorOutput{a, b}); err != nil {
			t.Errorf("two generators writing to separate output directories were refused: %v", err)
		}
	})

	t.Run("every colliding path is reported once, in a fixed order", func(t *testing.T) {
		user := filepath.Join(root, "user.avsc")
		order := filepath.Join(root, "order.avsc")
		plan := func(name string, dsts ...string) *generatorOutput {
			out := &generatorOutput{generator: name, output: root}
			for _, dst := range dsts {
				out.files = append(out.files, mergedFile{rel: filepath.Base(dst), dst: dst})
			}
			return out
		}

		err := checkCollisions([]*generatorOutput{
			plan("avroc-gen-json", order, user),
			plan("avroc-gen-pcf", user),
			plan("avroc-gen-avsc", order, user),
		})
		if err == nil {
			t.Fatal("checkCollisions accepted three generators producing the same files")
		}

		// A third claimant does not report the path a second time.
		if got := strings.Count(err.Error(), user); got != 1 {
			t.Errorf("%q is reported %d times, want 1: %v", user, got, err)
		}
		for _, want := range []string{"avroc-gen-avsc", "avroc-gen-json", "avroc-gen-pcf"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %q", err, want)
			}
		}
		// Sorted by path, so that a run reporting more than one collision reports
		// them in an order the file set fixes.
		if strings.Index(err.Error(), order) > strings.Index(err.Error(), user) {
			t.Errorf("collisions are not reported in path order: %v", err)
		}
	})

	t.Run("nothing to merge is not a collision", func(t *testing.T) {
		if err := checkCollisions(nil); err != nil {
			t.Errorf("checkCollisions(nil) = %v, want nil", err)
		}
	})
}

// TestMergeOutputsRefusesACollision is why the merge is split into phases at all:
// two generators producing the same path fail the run at merge time, with
// neither generator's output written into the project tree.
func TestMergeOutputsRefusesACollision(t *testing.T) {
	output := t.TempDir()

	first := scratchPlan(t, output, "avroc-gen-json", map[string]string{
		"user.avsc":      `{"produced_by":"json"}`,
		"only-json.avsc": `{"produced_by":"json"}`,
		"pkg/order.avsc": `{"produced_by":"json"}`,
	})
	second := scratchPlan(t, output, "avroc-gen-pcf", map[string]string{
		"user.avsc": `{"produced_by":"pcf"}`,
	})

	err := mergeOutputs(output, []*generatorOutput{first, second})
	if err == nil {
		t.Fatal("the merge accepted two generators producing the same file")
	}
	if !strings.Contains(err.Error(), filepath.Join(output, "user.avsc")) {
		t.Errorf("error %q does not name the path they collided on", err)
	}

	// Not one file of either generator reached the tree — including the ones only
	// one of them produced, because a run that collided is a failed run.
	entries, readErr := os.ReadDir(output)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, e := range entries {
		if path := filepath.Join(output, e.Name()); path != first.scratch && path != second.scratch {
			t.Errorf("a refused merge left %q in the output tree", e.Name())
		}
	}

	// And both generators' files are still where they left them, so a caller that
	// wanted to report on them could.
	for _, out := range []*generatorOutput{first, second} {
		for _, f := range out.files {
			if _, err := os.Stat(f.src); err != nil {
				t.Errorf("generator %q: %q left its scratch directory: %v", out.generator, f.rel, err)
			}
		}
	}
}

// scratchPlan is one generator that has already run: a scratch directory holding
// the files it wrote, resolved into the plan run would have returned.
func scratchPlan(t *testing.T, output, generatorName string, files map[string]string) *generatorOutput {
	t.Helper()

	scratch, err := newScratchDir(output, generatorName)
	if err != nil {
		t.Fatal(err)
	}
	for name, contents := range files {
		writeScratch(t, scratch, name, contents)
	}

	planned, err := planMerge(scratch, output)
	if err != nil {
		t.Fatal(err)
	}
	return &generatorOutput{
		generator: generatorName,
		output:    output,
		scratch:   scratch,
		files:     planned,
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

// mergeOne is what one generator's output goes through: its scratch directory
// resolved into a plan, and that plan merged. It is the pair of calls generateAll
// makes with a single generator in the manifest, and it reports the files the
// merge moved.
func mergeOne(scratch, output string) ([]string, error) {
	files, err := planMerge(scratch, output)
	if err != nil {
		return nil, err
	}
	out := &generatorOutput{
		generator: testGeneratorName,
		output:    output,
		scratch:   scratch,
		files:     files,
	}
	if err := mergeOutputs(output, []*generatorOutput{out}); err != nil {
		return nil, err
	}
	return relPaths(files), nil
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
