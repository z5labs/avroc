// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/z5labs/avroc/internal/cli"
	"github.com/z5labs/avroc/internal/ir"
)

// writeRecord writes an avroc.gen.json under projectRoot verbatim, so that a
// record avroc would never have written itself can be put in front of it.
func writeRecord(t *testing.T, projectRoot, contents string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(projectRoot, outputRecordFilename), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readRecord is the record as a person would read it in a diff.
func readRecord(t *testing.T, projectRoot string) string {
	t.Helper()

	b, err := os.ReadFile(filepath.Join(projectRoot, outputRecordFilename))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLoadOutputRecord(t *testing.T) {
	t.Run("a missing record prunes nothing", func(t *testing.T) {
		// The safe direction of the two: a project that has never been generated,
		// or one whose record was never committed, leaves a stale file for one more
		// run rather than removing a file it cannot vouch for.
		r, err := loadOutputRecord(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Files) != 0 {
			t.Errorf("a missing record names %v, want nothing", r.Files)
		}
	})

	t.Run("reads the paths it records", func(t *testing.T) {
		root := t.TempDir()
		writeRecord(t, root, `{"version":1,"files":["gen/user.go","pcf/user.avsc"]}`)

		r, err := loadOutputRecord(root)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"gen/user.go", "pcf/user.avsc"}; !slices.Equal(r.Files, want) {
			t.Errorf("files = %v, want %v", r.Files, want)
		}
	})

	t.Run("a null file list is not a nil pointer", func(t *testing.T) {
		root := t.TempDir()
		writeRecord(t, root, `{"version":1,"files":null}`)

		r, err := loadOutputRecord(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Files) != 0 {
			t.Errorf("files = %v, want nothing", r.Files)
		}
	})

	// A path spelled another way is still the path it names, and every comparison
	// after the load is between strings — so the one spelling avroc writes is the
	// one a record is read as. Left uncleaned, "./gen/user.go" in the record and
	// "gen/user.go" from the run would not match, and avroc would remove the file
	// it had just generated.
	t.Run("reads a path in the one spelling avroc writes", func(t *testing.T) {
		root := t.TempDir()
		writeRecord(t, root, `{"version":1,"files":["./gen/user.go","gen/pkg/../order.go"]}`)

		r, err := loadOutputRecord(root)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"gen/user.go", "gen/order.go"}; !slices.Equal(r.Files, want) {
			t.Errorf("files = %v, want %v", r.Files, want)
		}
	})

	// Every one of these is a record avroc did not write, and the response to each
	// is to fail the run rather than to delete the files a format avroc is guessing
	// at appears to name.
	for name, contents := range map[string]string{
		"not JSON":                        "gen/user.go\n",
		"an unknown field":                `{"version":1,"files":[],"generators":[]}`,
		"trailing data":                   `{"version":1,"files":[]}{"version":1,"files":[]}`,
		"a newer schema version":          `{"version":99,"files":["gen/user.go"]}`,
		"a negative schema version":       `{"version":-1,"files":["gen/user.go"]}`,
		"an absolute path":                `{"version":1,"files":["/etc/passwd"]}`,
		"a path climbing out":             `{"version":1,"files":["../../elsewhere/user.go"]}`,
		"a reference to itself":           fmt.Sprintf(`{"version":1,"files":[%q]}`, outputRecordFilename),
		"a reference to itself via a dot": fmt.Sprintf(`{"version":1,"files":["./%s"]}`, outputRecordFilename),
		"a reference to itself via ..":    fmt.Sprintf(`{"version":1,"files":["gen/../%s"]}`, outputRecordFilename),
		"a path climbing out and in":      `{"version":1,"files":["gen/../../user.go"]}`,
		"an empty path":                   `{"version":1,"files":[""]}`,
		"the project directory itself":    `{"version":1,"files":["."]}`,
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			root := t.TempDir()
			writeRecord(t, root, contents)

			if _, err := loadOutputRecord(root); err == nil {
				t.Errorf("loadOutputRecord accepted a record with %s", name)
			}
		})
	}

	// The reference-to-itself checks above are what stops this, and the file being
	// protected is the one avroc needs on the next run to prune anything at all.
	t.Run("a record that names itself is refused with the record still there", func(t *testing.T) {
		root := t.TempDir()
		writeRecord(t, root, fmt.Sprintf(`{"version":1,"files":["./%s"]}`, outputRecordFilename))

		if _, err := loadOutputRecord(root); err == nil {
			t.Fatal("loadOutputRecord accepted a record naming itself")
		}
		if _, err := os.Stat(filepath.Join(root, outputRecordFilename)); err != nil {
			t.Errorf("the record removed itself: %v", err)
		}
	})
}

// TestMarshalOutputRecord is what makes the record reviewable: the same file set
// renders to the same bytes however it reached the marshaller, so a regeneration
// that produced the same files is not a diff and a rename is the two lines it
// actually is.
func TestMarshalOutputRecord(t *testing.T) {
	first, err := marshalOutputRecord([]string{"pcf/user.avsc", "gen/user.go", "gen/user.go"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := marshalOutputRecord([]string{"gen/user.go", "pcf/user.avsc"})
	if err != nil {
		t.Fatal(err)
	}

	if string(first) != string(second) {
		t.Errorf("the record depends on the order it was given:\n%s\n%s", first, second)
	}
	want := `{
  "version": 1,
  "files": [
    "gen/user.go",
    "pcf/user.avsc"
  ]
}
`
	if string(first) != want {
		t.Errorf("record =\n%s\nwant\n%s", first, want)
	}

	// A run that generated nothing records an empty array rather than JSON null,
	// so the record avroc reads back is one it can validate rather than one it has
	// to interpret.
	empty, err := marshalOutputRecord(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(empty), `"files": []`) {
		t.Errorf("an empty record renders as\n%s", empty)
	}
}

// TestPruneStale is #119's rule in one place: a file the previous run recorded
// and this one did not produce is removed, and nothing else is — whatever else is
// sitting in the same directory.
func TestPruneStale(t *testing.T) {
	discardLog := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("removes what this run did not produce", func(t *testing.T) {
		root := t.TempDir()
		stale := writeProjectFile(t, root, "gen/user.go", "package avro // the previous run\n")
		kept := writeProjectFile(t, root, "gen/order.go", "package avro // this run too\n")

		if err := pruneStale(context.Background(), discardLog, root,
			[]string{"gen/order.go", "gen/user.go"},
			[]string{"gen/order.go"},
		); err != nil {
			t.Fatal(err)
		}

		if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the stale file survived: %v", err)
		}
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("a file this run produced was removed: %v", err)
		}
	})

	// The acceptance criterion that decides whether an output directory can be
	// shared with hand-written source: it can, because ownership is the record and
	// not the directory.
	t.Run("never removes a file no record names", func(t *testing.T) {
		root := t.TempDir()
		handWritten := writeProjectFile(t, root, "doc.go", "package avro // written by a person\n")
		alsoHandWritten := writeProjectFile(t, root, "gen/helpers.go", "package avro // and this one\n")

		if err := pruneStale(context.Background(), discardLog, root, []string{"gen/user.go"}, nil); err != nil {
			t.Fatal(err)
		}

		for _, path := range []string{handWritten, alsoHandWritten} {
			if _, err := os.Stat(path); err != nil {
				t.Errorf("a file avroc never recorded was removed: %v", err)
			}
		}
	})

	t.Run("a recorded file that is already gone is not a failure", func(t *testing.T) {
		root := t.TempDir()

		if err := pruneStale(context.Background(), discardLog, root, []string{"gen/user.go"}, nil); err != nil {
			t.Errorf("pruning a file somebody had already removed failed: %v", err)
		}
	})

	t.Run("leaves a recorded path that is no longer a regular file", func(t *testing.T) {
		root := t.TempDir()
		taken := filepath.Join(root, "gen", "user.go")
		if err := os.MkdirAll(taken, 0o755); err != nil {
			t.Fatal(err)
		}

		log, records := recordingLogger()
		if err := pruneStale(context.Background(), log, root, []string{"gen/user.go"}, nil); err != nil {
			t.Fatal(err)
		}

		// A person who replaced a generated file with a directory has taken it
		// over, and removing it would be avroc deleting something it did not write.
		if _, err := os.Stat(taken); err != nil {
			t.Errorf("a recorded path that is now a directory was removed: %v", err)
		}
		var warned bool
		for _, r := range records() {
			if r.message == "leaving a recorded output that is no longer a regular file" {
				warned = true
				if r.level != slog.LevelWarn {
					t.Errorf("level = %v, want WARN", r.level)
				}
			}
		}
		if !warned {
			t.Errorf("nothing in the log says the path was left alone: %v", records())
		}
	})

	t.Run("removes a directory it emptied, and leaves one it did not", func(t *testing.T) {
		root := t.TempDir()
		writeProjectFile(t, root, "emptied/user.go", "package avro\n")
		writeProjectFile(t, root, "shared/user.go", "package avro\n")
		writeProjectFile(t, root, "shared/doc.go", "package avro // written by a person\n")

		if err := pruneStale(context.Background(), discardLog, root,
			[]string{"emptied/user.go", "shared/user.go"}, nil,
		); err != nil {
			t.Fatal(err)
		}

		if _, err := os.Stat(filepath.Join(root, "emptied")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a directory pruning emptied survived: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "shared", "doc.go")); err != nil {
			t.Errorf("a directory still holding a file a person wrote was removed: %v", err)
		}
	})

	t.Run("removes nested directories it emptied, and never the project root", func(t *testing.T) {
		root := t.TempDir()
		writeProjectFile(t, root, "gen/pkg/nested/user.go", "package nested\n")

		if err := pruneStale(context.Background(), discardLog, root, []string{"gen/pkg/nested/user.go"}, nil); err != nil {
			t.Fatal(err)
		}

		if _, err := os.Stat(filepath.Join(root, "gen")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the emptied tree survived: %v", err)
		}
		if _, err := os.Stat(root); err != nil {
			t.Errorf("the project root was removed: %v", err)
		}
	})

	t.Run("refuses a recorded path outside the project", func(t *testing.T) {
		// Unreachable through loadOutputRecord, which rejects the record instead —
		// this is the check that makes the rule hold at the point of removal, so
		// that a future caller cannot reach os.Remove with a path nobody validated.
		root := t.TempDir()
		elsewhere := filepath.Join(t.TempDir(), "user.go")
		if err := os.WriteFile(elsewhere, []byte("package elsewhere\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		err := pruneStale(context.Background(), discardLog, root, []string{"../" + filepath.Base(filepath.Dir(elsewhere)) + "/user.go"}, nil)
		if err == nil {
			t.Fatal("pruning accepted a recorded path outside the project")
		}
		if _, statErr := os.Stat(elsewhere); statErr != nil {
			t.Errorf("a file outside the project was removed: %v", statErr)
		}
	})
}

// writeProjectFile writes a file at a slash-separated path relative to the
// project root, creating the directories above it, and returns its full path.
func writeProjectFile(t *testing.T, projectRoot, rel, contents string) string {
	t.Helper()

	path := filepath.Join(projectRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestGenerateAllPrunesAndRecords is the whole stage on the real fork/exec path:
// one run produces a file and records it, the next produces a different one, and
// the tree a person then commits holds only what the second run generated.
func TestGenerateAllPrunesAndRecords(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	projectRoot, output := newProject(t)

	if err := generateAll(ctx, log, projectRoot, []genTask{collidingTask(t, "json", output, "user.avsc")}); err != nil {
		t.Fatal(err)
	}
	if got, want := readRecord(t, projectRoot), `"gen/user.avsc"`; !strings.Contains(got, want) {
		t.Errorf("the record does not name %s:\n%s", want, got)
	}

	// A file a person wrote into the same directory the generator writes into,
	// which is what an output directory shared with hand-written source comes down
	// to: it is in no record, so it is never a candidate.
	handWritten := writeProjectFile(t, projectRoot, "gen/notes.md", "written by a person\n")

	if err := generateAll(ctx, log, projectRoot, []genTask{collidingTask(t, "json", output, "order.avsc")}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(output, "user.avsc")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the file only the first run produced survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(output, "order.avsc")); err != nil {
		t.Errorf("the file this run produced is not in the tree: %v", err)
	}
	if _, err := os.Stat(handWritten); err != nil {
		t.Errorf("a file a person wrote into the output directory was removed: %v", err)
	}

	if got, want := readRecord(t, projectRoot), `{
  "version": 1,
  "files": [
    "gen/order.avsc"
  ]
}
`; got != want {
		t.Errorf("record =\n%s\nwant\n%s", got, want)
	}
}

// TestGenerateAllLeavesTheRecordAloneWhenNothingChanged is why the write is
// conditional: a regeneration that produced the same files leaves the committed
// record exactly as it found it, so nothing downstream rebuilds because avroc
// ran.
func TestGenerateAllLeavesTheRecordAloneWhenNothingChanged(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	projectRoot, output := newProject(t)
	task := collidingTask(t, "json", output, "user.avsc")

	if err := generateAll(ctx, log, projectRoot, []genTask{task}); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(projectRoot, outputRecordFilename))
	if err != nil {
		t.Fatal(err)
	}

	if err := generateAll(ctx, log, projectRoot, []genTask{task}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(projectRoot, outputRecordFilename))
	if err != nil {
		t.Fatal(err)
	}

	if !before.ModTime().Equal(after.ModTime()) {
		t.Errorf("the record was rewritten by a run that changed nothing: %v then %v", before.ModTime(), after.ModTime())
	}
}

// TestGenerateAllRefusesAGeneratorThatProducesTheRecord is the reserved name: a
// generator writing avroc's own record would have its output silently overwritten
// moments later, so the run is refused in the same phase two generators claiming
// one path are — before anything is written where a person would find it.
func TestGenerateAllRefusesAGeneratorThatProducesTheRecord(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	projectRoot := t.TempDir()

	// An output directory of the project root itself, which the example manifest's
	// json generator already uses: it is the only place a generator can reach the
	// record at all.
	err := generateAll(ctx, log, projectRoot, []genTask{collidingTask(t, "json", projectRoot, outputRecordFilename)})
	if err == nil {
		t.Fatal("generation accepted a generator that produces avroc's own record")
	}
	if !strings.Contains(err.Error(), outputRecordFilename) {
		t.Errorf("error %q does not name the file it refused", err)
	}

	if _, statErr := os.Stat(filepath.Join(projectRoot, outputRecordFilename)); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("the refused run left a record behind: %v", statErr)
	}
}

// TestGenerateAllRefusesAnUnreadableRecordBeforeGenerating is the same argument
// the capability handshake makes: a record avroc cannot make sense of fails the
// run with nothing generated, because the only choices left after the merge would
// be to leave the stale files behind forever or to remove paths avroc cannot
// vouch for.
func TestGenerateAllRefusesAnUnreadableRecordBeforeGenerating(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	projectRoot, output := newProject(t)
	writeRecord(t, projectRoot, `{"version":1,"files":["/etc/passwd"]}`)

	err := generateAll(ctx, log, projectRoot, []genTask{collidingTask(t, "json", output, "user.avsc")})
	if err == nil {
		t.Fatal("generation accepted a record naming a path outside the project")
	}

	if _, statErr := os.Stat(output); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("the run generated into %q before reading the record: %v", output, statErr)
	}
}

// recordIDL is the sample schema with its record renamed: the one edit a person
// makes that turns a generated file into a stale one.
func recordIDL(name string) string {
	return fmt.Sprintf(`namespace com.example;
schema %s;
record %s {
  string name;
}
`, name, name)
}

// nameFromDescriptorGenerator is a conforming plugin that names the file it
// writes after the record in the descriptor it was handed.
//
// A shell script, like every generator in this package's tests, and it recovers
// the name by scanning the descriptor for printable tokens rather than by parsing
// protobuf — which is enough to make its output a function of the schema, and is
// the point: the file avroc has to prune is one whose name only the schema
// decided.
func nameFromDescriptorGenerator(t *testing.T) string {
	t.Helper()

	return writeNamedShellGenerator(t, "test", fmt.Sprintf(`set -e
if [ "$1" = "--plugin-info" ]; then
  printf '{"name":"test","version":"9.9.9","ir_version":%d,"options":[]}\n'
  exit 0
fi

while [ $# -gt 0 ]; do
  case "$1" in
    --descriptor) descriptor=$2; shift 2 ;;
    --out) out=$2; shift 2 ;;
    --opt) shift 2 ;;
    *) echo "error: unexpected argument $1" >&2; exit 1 ;;
  esac
done

name=$(tr -c 'A-Za-z0-9_.' '\n' < "$descriptor" | grep -E '^com\.example\.[A-Za-z][A-Za-z0-9]*$' | head -1)
if [ -z "$name" ]; then
  echo "error: no record name in the descriptor" >&2
  exit 1
fi
printf 'package avro // %%s\n' "$name" > "$out/$name.go"
`, ir.Version))
}

// TestRunGenerateRenamingARecordRemovesTheStaleFile is the story #119 is written
// against, end to end and through the CLI entry point: a record is renamed in the
// IDL, the generator produces a file under the new name, and the file the old
// name produced is gone rather than left in the tree to be committed and
// eventually compiled.
func TestRunGenerateRenamingARecordRemovesTheStaleFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	projectRoot := t.TempDir()
	generatorPath := nameFromDescriptorGenerator(t)

	if err := os.WriteFile(filepath.Join(projectRoot, manifestFilename), []byte(`{
  "inputs": ["schema.avdl"],
  "generators": [{"name": "test", "out": "gen"}]
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	c := cli.Context{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Env: cli.EnvironmentFunc(func(key string) (string, bool) {
			if key == "PATH" {
				return filepath.Dir(generatorPath), true
			}
			return "", false
		}),
		OpenDir:    func(dir string) fs.FS { return os.DirFS(dir) },
		WorkingDir: projectRoot,
	}

	generate := func(record string) {
		t.Helper()

		if err := os.WriteFile(filepath.Join(projectRoot, "schema.avdl"), []byte(recordIDL(record)), 0o644); err != nil {
			t.Fatal(err)
		}
		if code := runGenerate(ctx, c, noopTracer()); code != 0 {
			t.Fatalf("avroc generate over record %q exited %d", record, code)
		}
	}

	generate("User")
	before := filepath.Join(projectRoot, "gen", "com.example.User.go")
	if _, err := os.Stat(before); err != nil {
		t.Fatalf("the first run did not produce %q: %v", before, err)
	}

	generate("Order")

	after := filepath.Join(projectRoot, "gen", "com.example.Order.go")
	if _, err := os.Stat(after); err != nil {
		t.Errorf("the renamed record did not produce %q: %v", after, err)
	}
	if _, err := os.Stat(before); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the file the old record name produced is still in the tree: %v", err)
	}

	if got, want := readRecord(t, projectRoot), `"gen/com.example.Order.go"`; !strings.Contains(got, want) {
		t.Errorf("the record does not name %s:\n%s", want, got)
	}
}
