// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/z5labs/avroc/avrocpb"
)

// Where the descriptor lives, and why it is checked from the generator's side.
//
// #218 moved the per-invocation descriptor directory out of os.TempDir and into
// the generator's own output tree, beside the scratch directory --out names. What
// docs/plugin/SPEC.md requires of it did not change and is checked where it
// always was — TestGeneratorGenerate reads the descriptor the generator was
// handed, its mode, and its removal. What is new is a location inside a directory
// avroc also walks, merges from, prunes against and records, so the claim that
// has to be made here is a negative one: nothing that reads the output tree can
// see it.
//
// Every assertion below is made from inside the running generator or from the
// tree afterwards, never from avroc's own variables. A check written against
// descriptorDir would pass on an avroc that handed the generator some other path
// entirely.

// listOutputScript is the body of a generator that records what it can see —
// every entry in its own --out, and every entry in the directory holding the
// descriptor — and then writes files.
//
// The listings are taken before it writes anything, which is the only vantage
// point from which "--out is empty, and the descriptor is not in it" is
// observable at all.
func listOutputScript(outListing, treeListing string, files ...string) string {
	var writes strings.Builder
	for _, f := range files {
		fmt.Fprintf(&writes, "printf '%%s\\n' 'generated' > \"$out/%s\"\n", f)
	}

	return fmt.Sprintf(`set -e
while [ $# -gt 0 ]; do
  case "$1" in
    --descriptor) descriptor=$2; shift 2 ;;
    --out) out=$2; shift 2 ;;
    --opt) shift 2 ;;
    *) echo "error: unexpected argument $1" >&2; exit 1 ;;
  esac
done

ls -A "$out" > '%s'
ls -A "$(dirname "$(dirname "$descriptor")")" > '%s'
%s`, outListing, treeListing, writes.String())
}

// TestTheDescriptorDirectoryIsInvisibleToTheOutputTree is #218's claim about the
// new location, made with two generators sharing one output tree so that the
// descriptor directory has a neighbour to be confused with.
//
// The four things it must be invisible to are the four things avroc does with
// that tree: hand it to a generator as --out, walk it into a merge plan, merge
// those plans against each other, and record what was merged. A descriptor
// directory that reached any one of them would be adopted as output, reported as
// a collision, or written into avroc.gen.json for the next run to prune.
func TestTheDescriptorDirectoryIsInvisibleToTheOutputTree(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	projectRoot, output := newProject(t)
	listings := t.TempDir()

	type generatorRun struct {
		name        string
		file        string
		outListing  string
		treeListing string
	}
	runs := []generatorRun{
		{name: "first", file: "first.avsc"},
		{name: "second", file: "second.avsc"},
	}

	tasks := make([]genTask, 0, len(runs))
	for i := range runs {
		runs[i].outListing = filepath.Join(listings, runs[i].name+".out")
		runs[i].treeListing = filepath.Join(listings, runs[i].name+".tree")

		body := listOutputScript(runs[i].outListing, runs[i].treeListing, runs[i].file)
		tasks = append(tasks, genTask{
			name:           "avroc-gen-" + runs[i].name,
			executablePath: writeNamedShellGenerator(t, runs[i].name, body),
			output:         output,
			schemas:        []*avrocpb.Schema{testSchema("User")},
		})
	}

	if err := generateAll(ctx, log, projectRoot, tasks); err != nil {
		t.Fatalf("two generators sharing an output tree failed: %v", err)
	}

	for _, run := range runs {
		// --out is empty when the generator starts, which docs/plugin/SPEC.md's
		// "The output directory" promises outright: the descriptor directory is a
		// sibling of it and never inside it, so a generator walking its own
		// directory cannot reach the descriptor and cannot mistake it for a file
		// it wrote.
		if entries := listedEntries(t, run.outListing); len(entries) != 0 {
			t.Errorf("generator %q was handed an --out holding %v", run.name, entries)
		}

		// The tree that holds the descriptor is the project's output tree — which
		// is the whole of the move — and everything this run put in it while the
		// generator was running is hidden. The tree started empty, so every entry
		// here belongs to the run rather than to the project.
		entries := listedEntries(t, run.treeListing)
		if len(entries) == 0 {
			t.Errorf("generator %q saw nothing in the directory holding its descriptor", run.name)
		}
		var ownDescriptor bool
		for _, e := range entries {
			if !strings.HasPrefix(e, ".") {
				t.Errorf("generator %q saw %q beside its descriptor, which is not a hidden per-invocation directory", run.name, e)
			}
			if strings.HasPrefix(e, ".avroc-gen-"+run.name+"-descriptor-") {
				ownDescriptor = true
			}
		}
		if !ownDescriptor {
			t.Errorf("generator %q did not see its own descriptor directory in %v: the descriptor is not in the output tree", run.name, entries)
		}
	}

	// Nothing of either invocation survives the merge, so the tree a person is
	// about to commit holds the generated files and nothing else.
	want := []string{"first.avsc", "second.avsc"}
	if got := treeEntries(t, output); !slices.Equal(got, want) {
		t.Errorf("the output tree holds %v, want %v", got, want)
	}

	// And the record names exactly them. A descriptor directory that had reached
	// a merge plan would be recorded here, and the next run would prune a path
	// avroc never wrote.
	recorded := recordedFiles(t, projectRoot)
	wantRecorded := []string{"gen/first.avsc", "gen/second.avsc"}
	if !slices.Equal(recorded, wantRecorded) {
		t.Errorf("%s records %v, want %v", outputRecordFilename, recorded, wantRecorded)
	}
}

// listedEntries reads a listing one of the generators above wrote.
func listedEntries(t *testing.T, path string) []string {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the generator did not record a listing: %v", err)
	}
	var entries []string
	for _, line := range strings.Split(string(b), "\n") {
		if line != "" {
			entries = append(entries, line)
		}
	}
	slices.Sort(entries)
	return entries
}

// treeEntries is every path beneath dir, relative and slash-separated.
func treeEntries(t *testing.T, dir string) []string {
	t.Helper()

	var found []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		found = append(found, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(found)
	return found
}

// recordedFiles is what avroc.gen.json names, read as the file rather than
// through loadOutputRecord: the record is what a later run prunes against, and
// what is on disk is the only form of it that matters.
func recordedFiles(t *testing.T, projectRoot string) []string {
	t.Helper()

	b, err := os.ReadFile(filepath.Join(projectRoot, outputRecordFilename))
	if err != nil {
		t.Fatalf("a successful run left no %s: %v", outputRecordFilename, err)
	}
	var record outputRecord
	if err := json.Unmarshal(b, &record); err != nil {
		t.Fatalf("failed to parse %s: %v", outputRecordFilename, err)
	}
	return record.Files
}

// TestTheOutputDirectoryIsCreatedBeforeTheDescriptorNeedsIt is the ordering
// #218 flipped, checked through its consequence rather than by reading the
// source: the descriptor is written into the output tree, so a manifest naming
// an output directory that does not exist yet — several levels of it — has to
// have that directory created first, and the invocation that used to only need
// it for --out now needs it for both.
func TestTheOutputDirectoryIsCreatedBeforeTheDescriptorNeedsIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	scratch := t.TempDir()
	argsPath := filepath.Join(scratch, "args")
	copyPath := filepath.Join(scratch, "descriptor.copy")

	g := testGenerator(t, writeShellGenerator(t, recordArgsScript(argsPath, copyPath, "output.go")))

	projectRoot := t.TempDir()
	outputDir := filepath.Join(projectRoot, "gen", "go", "avro")

	if err := generateOne(ctx, g, projectRoot, outputDir, nil, testSchema("User")); err != nil {
		t.Fatalf("generation into an output directory that did not exist failed: %v", err)
	}

	// The generator could read the descriptor, which is the whole of the claim:
	// a descriptor directory made before its parent existed would have failed the
	// invocation instead.
	if _, err := os.ReadFile(copyPath); err != nil {
		t.Errorf("the generator could not read the descriptor it was handed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "output.go")); err != nil {
		t.Errorf("expected the generator's file merged into the created output directory: %v", err)
	}
	if dirs := descriptorDirs(t, outputDir); len(dirs) != 0 {
		t.Errorf("descriptor directories %v survived the invocation that created them", dirs)
	}
}

// TestEitherDirectoryFailingIsReportedAsItself is the other half of the flip.
// The two directories are now one inside the other, so the failure to create the
// output tree and the failure to create the descriptor's own directory inside it
// are adjacent in a way they were not before — and reporting one as the other
// would send a reader to the wrong end of the problem.
func TestEitherDirectoryFailingIsReportedAsItself(t *testing.T) {
	t.Run("the output directory cannot be created", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		log, records := recordingLogger()
		g := testGenerator(t, writeShellGenerator(t, "exit 0\n"))
		g.log = log

		// A regular file where a parent of the output directory would have to be,
		// so MkdirAll cannot create it and nothing about the descriptor has been
		// reached yet.
		projectRoot := t.TempDir()
		blocked := filepath.Join(projectRoot, "gen")
		if err := os.WriteFile(blocked, []byte("not a directory\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		err := generateOne(ctx, g, projectRoot, filepath.Join(blocked, "go"), nil, testSchema("User"))
		if err == nil {
			t.Fatal("generation succeeded with a file where the output directory had to be")
		}
		assertReported(t, records, "failed to create output directory")
		assertNotReported(t, records, "failed to create descriptor directory")
	})

	t.Run("the descriptor directory cannot be created", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root, which writes into a directory with no write bit")
		}

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()

		log, records := recordingLogger()
		g := testGenerator(t, writeShellGenerator(t, "exit 0\n"))
		g.log = log

		// The output directory exists and is not writable, so MkdirAll is happy
		// with it and the descriptor's directory is the first thing that cannot be
		// made.
		projectRoot := t.TempDir()
		outputDir := filepath.Join(projectRoot, "gen")
		if err := os.Mkdir(outputDir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			// Restored so that the test's own temporary directory can be removed.
			_ = os.Chmod(outputDir, 0o755)
		})

		err := generateOne(ctx, g, projectRoot, outputDir, nil, testSchema("User"))
		if err == nil {
			t.Fatal("generation succeeded with an output directory it could not write into")
		}
		if !strings.Contains(err.Error(), "failed to create descriptor directory") {
			t.Errorf("error %q does not say the descriptor directory could not be created", err)
		}
		assertReported(t, records, "failed to create descriptor directory")
		assertNotReported(t, records, "failed to create output directory")
	})
}

// assertReported requires message to be among what was logged.
func assertReported(t *testing.T, records func() []recordedLine, message string) {
	t.Helper()

	for _, r := range records() {
		if r.message == message {
			return
		}
	}
	t.Errorf("nothing in the log reports %q: %v", message, records())
}

// assertNotReported requires message not to be among what was logged, which is
// how one of two adjacent failures is held to being reported as itself.
func assertNotReported(t *testing.T, records func() []recordedLine, message string) {
	t.Helper()

	for _, r := range records() {
		if r.message == message {
			t.Errorf("the failure was also reported as %q", message)
			return
		}
	}
}
