// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"bytes"
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
	"testing/fstest"
	"time"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/cli"
	"github.com/z5labs/avroc/internal/ir"

	"google.golang.org/protobuf/proto"
)

func TestLookupGenerators(t *testing.T) {
	discardLog := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := filepath.Join(string(filepath.Separator), "usr", "local", "bin")

	t.Run("finds generator executables", func(t *testing.T) {
		fsys := fstest.MapFS{
			"avroc-gen-go": executableFile(),
			"other-tool":   executableFile(),
		}

		got, err := lookupGenerators(context.Background(), discardLog, staticOpenDir(fsys), dir)
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 1 {
			t.Fatalf("got %d generators, want 1", len(got))
		}
		want := filepath.Join(dir, "avroc-gen-go")
		if got["avroc-gen-go"] != want {
			t.Errorf("avroc-gen-go path = %q, want %q", got["avroc-gen-go"], want)
		}
	})

	t.Run("skips a file without an execute bit", func(t *testing.T) {
		fsys := fstest.MapFS{
			"avroc-gen-go": &fstest.MapFile{Mode: 0o644},
		}

		got, err := lookupGenerators(context.Background(), discardLog, staticOpenDir(fsys), dir)
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 0 {
			t.Fatalf("got %d generators, want 0 (file not executable)", len(got))
		}
	})

	t.Run("multiple generators in same directory", func(t *testing.T) {
		fsys := fstest.MapFS{
			"avroc-gen-go":   executableFile(),
			"avroc-gen-java": executableFile(),
		}

		got, err := lookupGenerators(context.Background(), discardLog, staticOpenDir(fsys), dir)
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 2 {
			t.Fatalf("got %d generators, want 2", len(got))
		}
	})

	t.Run("multiple directories", func(t *testing.T) {
		dir1 := filepath.Join(string(filepath.Separator), "dir1")
		dir2 := filepath.Join(string(filepath.Separator), "dir2")
		openDir := func(d string) fs.FS {
			switch d {
			case dir1:
				return fstest.MapFS{"avroc-gen-go": executableFile()}
			case dir2:
				return fstest.MapFS{"avroc-gen-java": executableFile()}
			}
			return fstest.MapFS{}
		}

		got, err := lookupGenerators(context.Background(), discardLog, openDir, dir1, dir2)
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 2 {
			t.Fatalf("got %d generators, want 2", len(got))
		}
		if got["avroc-gen-go"] != filepath.Join(dir1, "avroc-gen-go") {
			t.Errorf("avroc-gen-go path = %q", got["avroc-gen-go"])
		}
		if got["avroc-gen-java"] != filepath.Join(dir2, "avroc-gen-java") {
			t.Errorf("avroc-gen-java path = %q", got["avroc-gen-java"])
		}
	})

	t.Run("the earliest PATH entry wins", func(t *testing.T) {
		first := filepath.Join(string(filepath.Separator), "first")
		second := filepath.Join(string(filepath.Separator), "second")
		openDir := func(string) fs.FS {
			return fstest.MapFS{"avroc-gen-go": executableFile()}
		}

		got, err := lookupGenerators(context.Background(), discardLog, openDir, first, second)
		if err != nil {
			t.Fatal(err)
		}

		// Prepending a directory is how an author shadows an installed
		// generator with one under development, so the later entry must not
		// quietly replace it.
		if want := filepath.Join(first, "avroc-gen-go"); got["avroc-gen-go"] != want {
			t.Errorf("avroc-gen-go path = %q, want %q", got["avroc-gen-go"], want)
		}
	})

	t.Run("an empty PATH entry is not the working directory", func(t *testing.T) {
		var opened []string
		openDir := func(d string) fs.FS {
			opened = append(opened, d)
			return fstest.MapFS{"avroc-gen-go": executableFile()}
		}

		got, err := lookupGenerators(context.Background(), discardLog, openDir, "", dir)
		if err != nil {
			t.Fatal(err)
		}

		if slices.Contains(opened, "") {
			t.Error("an empty PATH entry was searched")
		}
		if want := filepath.Join(dir, "avroc-gen-go"); got["avroc-gen-go"] != want {
			t.Errorf("avroc-gen-go path = %q, want %q", got["avroc-gen-go"], want)
		}
	})

	t.Run("nonexistent directory", func(t *testing.T) {
		openDir := func(string) fs.FS { return &errFS{err: fs.ErrNotExist} }

		got, err := lookupGenerators(context.Background(), discardLog, openDir, "nonexistent")
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 0 {
			t.Fatalf("got %d generators, want 0", len(got))
		}
	})

	t.Run("empty directory", func(t *testing.T) {
		got, err := lookupGenerators(context.Background(), discardLog, staticOpenDir(fstest.MapFS{}), dir)
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 0 {
			t.Fatalf("got %d generators, want 0", len(got))
		}
	})

	t.Run("skips directory with permission error and continues", func(t *testing.T) {
		restricted := filepath.Join(string(filepath.Separator), "restricted")
		openDir := func(d string) fs.FS {
			if d == restricted {
				return &errFS{err: fs.ErrPermission}
			}
			return fstest.MapFS{"avroc-gen-go": executableFile()}
		}

		var logBuf bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

		got, err := lookupGenerators(context.Background(), log, openDir, restricted, dir)
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 1 {
			t.Fatalf("got %d generators, want 1", len(got))
		}
		want := filepath.Join(dir, "avroc-gen-go")
		if got["avroc-gen-go"] != want {
			t.Errorf("avroc-gen-go path = %q, want %q", got["avroc-gen-go"], want)
		}
		if !bytes.Contains(logBuf.Bytes(), []byte("permission")) {
			t.Errorf("expected warning log for permission error, got: %s", logBuf.String())
		}
	})
}

func staticOpenDir(fsys fs.FS) func(string) fs.FS {
	return func(string) fs.FS { return fsys }
}

// executableFile is a directory entry carrying the mode bits that make a POSIX
// host treat it as a program. Discovery is defined by those bits alone, so the
// fixture is the same one for every generator.
func executableFile() *fstest.MapFile {
	return &fstest.MapFile{Mode: 0o755}
}

// errFS implements fs.FS and returns a fixed error for every operation.
// Used to simulate real-filesystem failure modes (ErrNotExist, ErrPermission)
// that MapFS cannot otherwise produce.
type errFS struct{ err error }

func (f *errFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: f.err}
}

func (f *errFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return nil, &fs.PathError{Op: "read", Path: name, Err: f.err}
}

func TestMain_Dispatch(t *testing.T) {
	newContext := func(args ...string) cli.Context {
		return cli.Context{
			Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
			Env:        cli.EnvironmentFunc(func(string) (string, bool) { return "", false }),
			OpenDir:    staticOpenDir(fstest.MapFS{}),
			WorkingDir: t.TempDir(),
			Args:       args,
		}
	}

	t.Run("no arguments is a usage error", func(t *testing.T) {
		if code := Main(context.Background(), newContext()); code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
	})

	t.Run("unknown command is an error", func(t *testing.T) {
		if code := Main(context.Background(), newContext("bogus")); code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
	})

	t.Run("help exits 0", func(t *testing.T) {
		if code := Main(context.Background(), newContext("help")); code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
	})

	t.Run("init scaffolds and exits 0", func(t *testing.T) {
		if code := Main(context.Background(), newContext("init")); code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
	})

	t.Run("init rejects extra arguments", func(t *testing.T) {
		if code := Main(context.Background(), newContext("init", "schema.avdl")); code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
	})

	t.Run("generate rejects extra arguments", func(t *testing.T) {
		if code := Main(context.Background(), newContext("generate", "schema.avdl")); code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
	})

	t.Run("inspect renders the descriptor it is given", func(t *testing.T) {
		path, _ := writeInspectFixture(t)

		if code := Main(context.Background(), newContext("inspect", path)); code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
	})

	t.Run("inspect requires a descriptor", func(t *testing.T) {
		if code := Main(context.Background(), newContext("inspect")); code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
	})
}

// testGeneratorName is the generator these tests drive. It is part of the
// per-invocation descriptor directory's name, so a leftover from some other
// test — or from another package's temporary files — is not mistaken for one of
// these.
const testGeneratorName = "avroc-gen-test"

// writeShellGenerator writes body out as an executable shell script and returns
// its path.
//
// A shell script is the generator these tests use on purpose. docs/plugin/SPEC.md
// makes the contract "deliberately small enough that a generator can be a shell
// script", and a test driving one holds avroc to that: a vector that needed a
// flag-parsing library, or a descriptor a script could not read, would fail here
// rather than in a plugin author's terminal.
func writeShellGenerator(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), testGeneratorName)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// recordArgsScript is the body of a generator that records its whole argument
// vector one argument per line, copies the descriptor it was handed to
// copyPath, and writes outFile beneath --out.
//
// Copying the descriptor rather than checking it in the shell is what lets the
// assertions be about the bytes: the copy is taken while the generator is
// running, which is the only vantage point from which "written in full before
// the generator started, and removed once it exited" is observable at all.
func recordArgsScript(argsPath, copyPath, outFile string) string {
	return fmt.Sprintf(`set -e
: > '%s'
for a in "$@"; do printf '%%s\n' "$a" >> '%s'; done

while [ $# -gt 0 ]; do
  case "$1" in
    --descriptor) descriptor=$2; shift 2 ;;
    --out) out=$2; shift 2 ;;
    --opt) shift 2 ;;
    *) echo "error: unexpected argument $1" >&2; exit 1 ;;
  esac
done

cp "$descriptor" '%s'
mkdir -p "$(dirname "$out/%s")"
printf 'package main\n' > "$out/%s"
`, argsPath, argsPath, copyPath, outFile, outFile)
}

func readLines(t *testing.T, path string) []string {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func testGenerator(t *testing.T, executablePath string) generator {
	t.Helper()

	return generator{
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		name:           testGeneratorName,
		executablePath: executablePath,
	}
}

// generateOne puts a single generator through the whole of generation — the
// invocation, the plan, the collision check, the merge and the prune — which is
// the path a run of any size takes. A run of one is where a generator's own
// behaviour is observable without another generator's timing in the way.
//
// projectRoot is the directory a real run reads avroc.json from, and is where the
// record of what was generated lands; output is the generator's own directory
// beneath it.
func generateOne(ctx context.Context, g generator, projectRoot, output string, options []*avrocpb.Option, schemas ...*avrocpb.Schema) error {
	return generateAll(ctx, g.log, projectRoot, []genTask{{
		name:           g.name,
		executablePath: g.executablePath,
		output:         output,
		options:        options,
		schemas:        schemas,
	}})
}

// newProject is a project directory and one generator's output directory beneath
// it: the shape planGenerators resolves a manifest into, where the output tree is
// a subdirectory of the project rather than the project itself.
func newProject(t *testing.T) (projectRoot, output string) {
	t.Helper()

	projectRoot = t.TempDir()
	return projectRoot, filepath.Join(projectRoot, "gen")
}

// TestGeneratorGenerate is docs/plugin/SPEC.md's Invocation on the real
// fork/exec path: the argument vector, the absolute paths in it, the descriptor
// those paths name, and the files the generator wrote for itself.
func TestGeneratorGenerate(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	scratch := t.TempDir()
	argsPath := filepath.Join(scratch, "args")
	copyPath := filepath.Join(scratch, "descriptor.copy")

	g := testGenerator(t, writeShellGenerator(t, recordArgsScript(argsPath, copyPath, "pkg/output.go")))

	options := []*avrocpb.Option{
		{Name: proto.String("encoding"), Value: proto.String("single_object")},
		{Name: proto.String("package_name"), Value: proto.String("avro")},
	}
	schema := testSchema("User")

	// The descriptor's lifetime ends when the generator exits, so the set of
	// descriptor directories on disk must not grow across an invocation. Taken
	// before rather than asserted as "none afterwards", because a crashed
	// earlier run may have left one and that is not this test's failure.
	before := descriptorDirs(t)

	// A directory avroc has to create: a plugin may assume --out exists.
	projectRoot, outputDir := newProject(t)

	if err := generateOne(ctx, g, projectRoot, outputDir, options, schema); err != nil {
		t.Fatal(err)
	}

	args := readLines(t, argsPath)
	if len(args) != 8 {
		t.Fatalf("generator received %d arguments %q, want 8", len(args), args)
	}
	if args[0] != "--descriptor" || args[2] != "--out" {
		t.Errorf("argument vector = %q, want --descriptor <path> --out <dir> first", args)
	}
	if !filepath.IsAbs(args[1]) {
		t.Errorf("--descriptor %q is not an absolute path", args[1])
	}
	if filepath.Base(args[1]) != descriptorFilename {
		t.Errorf("--descriptor %q is not the descriptor avroc writes", args[1])
	}
	// --out is a private scratch directory beneath the project's output tree, not
	// the tree itself: a generator does not learn where the project's output goes
	// and cannot write into it.
	absOutput, err := filepath.Abs(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(args[3]) {
		t.Errorf("--out %q is not an absolute path", args[3])
	}
	if args[3] == absOutput {
		t.Errorf("--out = %q, the project's own output directory rather than a scratch directory", args[3])
	}
	if filepath.Dir(args[3]) != absOutput {
		t.Errorf("--out = %q, want a directory beneath %q", args[3], absOutput)
	}
	// And it is gone: the scratch directory's lifetime is the invocation's, on
	// success as much as on failure.
	if _, err := os.Stat(args[3]); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the scratch directory %q survived the invocation: %v", args[3], err)
	}
	// The options follow in the order the manifest fixed, so the vector is a
	// function of the manifest rather than of a map iteration.
	wantOpts := []string{"--opt", "encoding=single_object", "--opt", "package_name=avro"}
	if got := args[4:]; !slices.Equal(got, wantOpts) {
		t.Errorf("option arguments = %q, want %q", got, wantOpts)
	}

	// The descriptor the generator could read while it ran is the one avroc
	// built for this invocation, complete and decodable.
	b, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatalf("the generator could not copy the descriptor it was handed: %v", err)
	}
	var onDisk avrocpb.GenerateRequest
	if err := proto.Unmarshal(b, &onDisk); err != nil {
		t.Fatalf("the descriptor the generator read does not decode: %v", err)
	}
	if err := ir.CheckVersion(onDisk.GetVersion()); err != nil {
		t.Errorf("avroc emitted a descriptor no generator here would read: %v", err)
	}
	if want := newDescriptor(options, []*avrocpb.Schema{schema}); !proto.Equal(&onDisk, want) {
		t.Errorf("descriptor on disk = %v, want %v", &onDisk, want)
	}

	// The generator wrote into its scratch directory and avroc merged it: the
	// file is in the project's tree, at the path the generator chose, with the
	// subdirectory it asked for created on the way.
	if _, err := os.Stat(filepath.Join(outputDir, "pkg", "output.go")); err != nil {
		t.Errorf("expected the generator's file merged into the output directory: %v", err)
	}
	// And nothing else, so the merge left no scratch directory behind in a tree
	// a person is about to commit.
	if entries, err := os.ReadDir(outputDir); err != nil {
		t.Error(err)
	} else if len(entries) != 1 || entries[0].Name() != "pkg" {
		t.Errorf("the output directory holds %v, want just the merged pkg directory", entries)
	}

	for dir := range descriptorDirs(t) {
		if _, ok := before[dir]; !ok {
			t.Errorf("descriptor directory %q survived the invocation that created it", dir)
		}
	}
}

// TestGeneratorGenerateNonZeroExit is the whole of the failure signal: a
// non-zero exit fails the run whatever the generator left on disk, nothing it
// left is merged, the status is reported without anything being concluded from
// its value, and the descriptor and scratch directories are still removed.
func TestGeneratorGenerateNonZeroExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	log, records := recordingLogger()
	// Partial output before the failure, because a plugin MAY exit non-zero with
	// files already written: what avroc does with them is decided by the exit
	// status and not by their presence.
	g := testGenerator(t, writeShellGenerator(t, `set -e
while [ $# -gt 0 ]; do
  case "$1" in
    --out) out=$2; shift 2 ;;
    *) shift ;;
  esac
done
printf 'package half\n' > "$out/half.go"
echo 'error: com.example.User: nope' >&2
exit 3
`))
	g.log = log

	before := descriptorDirs(t)

	projectRoot, outputDir := newProject(t)
	err := generateOne(ctx, g, projectRoot, outputDir, nil, testSchema("User"))
	if err == nil {
		t.Fatal("generate accepted a generator that exited non-zero")
	}
	if !strings.Contains(err.Error(), testGeneratorName) {
		t.Errorf("error %q does not name the generator", err)
	}
	// Distinguishable from the signal case, which is the whole point of
	// reporting them separately.
	if !strings.Contains(err.Error(), "exited with status 3") {
		t.Errorf("error %q does not report the exit status", err)
	}
	if strings.Contains(err.Error(), "signal") {
		t.Errorf("error %q reports a generator that exited as one that was signalled", err)
	}

	// The half-written file the generator left is not output, and the scratch
	// directory that held it is gone — so the project tree a failed run leaves
	// behind is the one it started with.
	entries, readErr := os.ReadDir(outputDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Errorf("a failed invocation left %v in the output directory", entries)
	}

	var reported bool
	for _, r := range records() {
		switch r.message {
		case "generator exited non-zero":
			reported = true
			if r.attrs["exit_code"] != "3" {
				t.Errorf("exit_code attribute = %q, want %q", r.attrs["exit_code"], "3")
			}
			if r.attrs["generator"] != testGeneratorName {
				t.Errorf("generator attribute = %q, want %q", r.attrs["generator"], testGeneratorName)
			}
		case "generated output":
			t.Error("a failed invocation was reported as having generated output")
		}
	}
	if !reported {
		t.Errorf("nothing in the log reports the exit status: %v", records())
	}

	// The generator's diagnostic still reached the log: a failing run is the one
	// where its explanation matters most.
	var diagnosed bool
	for _, r := range records() {
		if r.message == "com.example.User: nope" && r.attrs["severity"] == "error" {
			diagnosed = true
		}
	}
	if !diagnosed {
		t.Errorf("the failing generator's diagnostic is not in the log: %v", records())
	}

	for dir := range descriptorDirs(t) {
		if _, ok := before[dir]; !ok {
			t.Errorf("descriptor directory %q survived a failed invocation", dir)
		}
	}
}

// TestGeneratorGenerateRefusesAnEscape is the boundary on the real fork/exec
// path: a generator that exits zero having planted a link out of its scratch
// directory fails the run, and nothing it produced is merged.
//
// It is the escape a relative-path check cannot see. Every path the generator
// used is beneath the directory it was given; only following the link leaves it,
// which is why the merge refuses the link rather than resolving it.
func TestGeneratorGenerateRefusesAnEscape(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	elsewhere := t.TempDir()
	g := testGenerator(t, writeShellGenerator(t, fmt.Sprintf(`set -e
while [ $# -gt 0 ]; do
  case "$1" in
    --out) out=$2; shift 2 ;;
    *) shift ;;
  esac
done
printf 'package avro\n' > "$out/legitimate.go"
ln -s '%s' "$out/escape"
`, elsewhere)))

	projectRoot, outputDir := newProject(t)
	err := generateOne(ctx, g, projectRoot, outputDir, nil, testSchema("User"))
	if err == nil {
		t.Fatal("generate merged the output of a generator that escaped its scratch directory")
	}
	if !strings.Contains(err.Error(), "escape") {
		t.Errorf("error %q does not name the entry it refused", err)
	}

	// The refusal is all or nothing: the file the generator was entitled to write
	// does not reach the tree either, because a run that escaped is a failed run.
	entries, readErr := os.ReadDir(outputDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Errorf("a refused merge left %v in the output directory", entries)
	}
}

// TestGenerateAllRefusesACollision is #118 on the real fork/exec path: two
// generators that both produce one file fail the run, the report names both of
// them and the path, and nothing either of them produced is merged.
//
// It runs the pair twice, in both manifest orders. Which of two concurrent
// generators finishes first is not something a test can fix, so what is pinned
// here is that it cannot matter: the two runs produce the identical report, and
// each of them leaves the tree it started with.
func TestGenerateAllRefusesACollision(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	projectRoot, output := newProject(t)

	// The collision the example manifest is a single edit away from: two
	// generators emitting .avsc into one directory, each with a file of its own
	// as well as the one they both claim.
	json := collidingTask(t, "json", output, "user.avsc", "only-json.avsc")
	pcf := collidingTask(t, "pcf", output, "user.avsc", "only-pcf.avsc")

	var reports []string
	for _, tasks := range [][]genTask{{json, pcf}, {pcf, json}} {
		err := generateAll(ctx, log, projectRoot, tasks)
		if err == nil {
			t.Fatalf("generation accepted %q and %q both producing user.avsc", tasks[0].name, tasks[1].name)
		}
		for _, want := range []string{"avroc-gen-json", "avroc-gen-pcf", filepath.Join(output, "user.avsc")} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %q", err, want)
			}
		}
		reports = append(reports, err.Error())

		// Nothing merged, and no scratch directory left behind either — so the
		// second run starts from the tree the first one did.
		entries, readErr := os.ReadDir(output)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("a collision left %v in the output directory", entries)
		}
	}

	if reports[0] != reports[1] {
		t.Errorf("the report depends on the order the generators ran:\n%s\n%s", reports[0], reports[1])
	}
}

// TestGenerateAllMergesEveryGenerator is the case a collision is the exception
// to: two generators writing into one output directory, neither claiming a path
// the other does, both merged.
func TestGenerateAllMergesEveryGenerator(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	projectRoot, output := newProject(t)

	tasks := []genTask{
		collidingTask(t, "json", output, "user.avsc"),
		collidingTask(t, "pcf", output, "pcf/user.avsc"),
	}
	if err := generateAll(ctx, log, projectRoot, tasks); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"user.avsc", "pcf/user.avsc"} {
		b, err := os.ReadFile(filepath.Join(output, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("%q did not reach the output tree: %v", name, err)
			continue
		}
		if got := strings.TrimSpace(string(b)); got == "" {
			t.Errorf("%q is empty", name)
		}
	}

	// Both scratch directories are gone, so a tree a person is about to commit
	// holds only the generated files.
	entries, err := os.ReadDir(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("the output directory holds %v, want just the two merged paths", entries)
	}
}

// TestGenerateAllDiscardsEveryGeneratorWhenOneFails is the other consequence of
// merging only once every generator has finished: a run is all or nothing, so a
// generator that exited zero has its output discarded along with the failing
// one's rather than left in the tree.
func TestGenerateAllDiscardsEveryGeneratorWhenOneFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	projectRoot, output := newProject(t)

	failing := genTask{
		name: "avroc-gen-pcf",
		executablePath: writeNamedShellGenerator(t, "pcf", `echo 'error: com.example.User: nope' >&2
exit 1
`),
		output:  output,
		schemas: []*avrocpb.Schema{testSchema("User")},
	}

	err := generateAll(ctx, log, projectRoot, []genTask{collidingTask(t, "json", output, "user.avsc"), failing})
	if err == nil {
		t.Fatal("generation succeeded with a generator that exited non-zero")
	}

	// A half-generated tree is worse than an ungenerated one, so the generator
	// that succeeded contributes nothing to a run that failed.
	entries, readErr := os.ReadDir(output)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Errorf("a failed run left %v in the output directory", entries)
	}
}

// collidingTask is a generator named avroc-gen-<name> that writes one line into
// each of files and exits zero: the smallest generator that can be made to claim
// a path another one claims too.
//
// Each is its own executable under its own name, because a report naming one of
// two generators only means anything if the other could have been named instead.
func collidingTask(t *testing.T, name, output string, files ...string) genTask {
	t.Helper()

	var writes strings.Builder
	for _, f := range files {
		fmt.Fprintf(&writes, "mkdir -p \"$(dirname \"$out/%s\")\"\nprintf '%%s\\n' 'produced by avroc-gen-%s' > \"$out/%s\"\n", f, name, f)
	}

	body := fmt.Sprintf(`set -e
while [ $# -gt 0 ]; do
  case "$1" in
    --out) out=$2; shift 2 ;;
    *) shift ;;
  esac
done
%s`, writes.String())

	return genTask{
		name:           "avroc-gen-" + name,
		executablePath: writeNamedShellGenerator(t, name, body),
		output:         output,
		schemas:        []*avrocpb.Schema{testSchema("User")},
	}
}

// TestGeneratorGenerateCancellation is the other half of the process lifecycle:
// cancelling the signal-based parent context reaches the child, and generate
// returns rather than waiting on a process nobody is going to stop.
func TestGeneratorGenerateCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	started := filepath.Join(t.TempDir(), "started")
	// exec, so that the process avroc kills is the one that sleeps. A shell that
	// forked the sleep would leave it orphaned holding the inherited standard
	// streams, and the test binary would then outlive its own generator.
	g := testGenerator(t, writeShellGenerator(t, fmt.Sprintf(": > '%s'\nexec sleep 300\n", started)))

	before := descriptorDirs(t)

	projectRoot, outputDir := newProject(t)
	done := make(chan error, 1)
	go func() {
		done <- generateOne(ctx, g, projectRoot, outputDir, nil, testSchema("User"))
	}()

	// Cancel only once the child is definitely running, so the test is about
	// cancellation reaching a live process rather than about a context that was
	// already done before the fork.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the generator never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("generate reported success for a cancelled invocation")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("generate did not return after its context was cancelled")
	}

	for dir := range descriptorDirs(t) {
		if _, ok := before[dir]; !ok {
			t.Errorf("descriptor directory %q survived a cancelled invocation", dir)
		}
	}

	// The scratch directory goes with it. Cancellation is the case that used to
	// leave one behind — the removal is deferred rather than done on the success
	// path for exactly this reason — and one left in the project's output tree is
	// a directory the user then finds and has to identify.
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a cancelled invocation left %v in the output directory", entries)
	}
}

// descriptorDirs is the set of per-invocation descriptor directories currently
// on disk for the stand-in generator.
func descriptorDirs(t *testing.T) map[string]struct{} {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(os.TempDir(), testGeneratorName+"-descriptor-*"))
	if err != nil {
		t.Fatal(err)
	}
	dirs := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		dirs[m] = struct{}{}
	}
	return dirs
}

// TestGeneratorArgs pins the vector docs/plugin/SPEC.md specifies, including
// the case the invocation tests above cannot reach: a generator with no
// options at all still gets --descriptor and --out and nothing more.
func TestGeneratorArgs(t *testing.T) {
	testCases := []struct {
		name    string
		options []*avrocpb.Option
		want    []string
	}{
		{
			name: "no options",
			want: []string{"--descriptor", "/tmp/d/descriptor.binpb", "--out", "/w/gen"},
		},
		{
			name: "an option whose value is empty",
			options: []*avrocpb.Option{
				{Name: proto.String("package_name"), Value: proto.String("")},
			},
			want: []string{"--descriptor", "/tmp/d/descriptor.binpb", "--out", "/w/gen", "--opt", "package_name="},
		},
		{
			name: "an option whose value carries further equals signs",
			options: []*avrocpb.Option{
				{Name: proto.String("tag"), Value: proto.String("a=b=c")},
			},
			want: []string{"--descriptor", "/tmp/d/descriptor.binpb", "--out", "/w/gen", "--opt", "tag=a=b=c"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := generatorArgs("/tmp/d/descriptor.binpb", "/w/gen", tc.options)
			if !slices.Equal(got, tc.want) {
				t.Errorf("generatorArgs = %q, want %q", got, tc.want)
			}
		})
	}
}
