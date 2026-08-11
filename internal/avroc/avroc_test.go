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
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
//
// The generator says nothing at all on standard error, because the exit status
// is the whole of what avroc analyses (#190): a silent failure is still a
// failure, and it is the case a run that inferred anything from the generator's
// output would get wrong.
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

	for dir := range descriptorDirs(t) {
		if _, ok := before[dir]; !ok {
			t.Errorf("descriptor directory %q survived a failed invocation", dir)
		}
	}
}

// TestGeneratorGenerateSignal is docs/plugin/SPEC.md's other failure: a
// generator killed by a signal is reported as killed by that signal, naming it,
// and distinguishably from one that exited non-zero.
func TestGeneratorGenerateSignal(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	log, records := recordingLogger()
	g := testGenerator(t, writeShellGenerator(t, "kill -TERM $$\n"))
	g.log = log

	projectRoot, outputDir := newProject(t)
	err := generateOne(ctx, g, projectRoot, outputDir, nil, testSchema("User"))
	if err == nil {
		t.Fatal("generate reported success for a generator killed by a signal")
	}
	if !strings.Contains(err.Error(), "terminated by signal") {
		t.Errorf("error %q does not say the generator was terminated by a signal", err)
	}
	if !strings.Contains(err.Error(), "terminated") {
		t.Errorf("error %q does not name the signal", err)
	}
	if !strings.Contains(err.Error(), testGeneratorName) {
		t.Errorf("error %q does not name the generator", err)
	}

	var reported bool
	for _, r := range records() {
		if r.message != "generator terminated by signal" {
			continue
		}
		reported = true
		if r.attrs["signal"] != "terminated" {
			t.Errorf("signal attribute = %q, want %q", r.attrs["signal"], "terminated")
		}
		if r.attrs["signal_number"] != strconv.Itoa(int(syscall.SIGTERM)) {
			t.Errorf("signal_number attribute = %q, want %q", r.attrs["signal_number"], strconv.Itoa(int(syscall.SIGTERM)))
		}
		if r.attrs["generator"] != testGeneratorName {
			t.Errorf("generator attribute = %q, want %q", r.attrs["generator"], testGeneratorName)
		}
		if _, ok := r.attrs["exit_code"]; ok {
			t.Error("a generator killed by a signal was reported with an exit code")
		}
	}
	if !reported {
		t.Errorf("nothing in the log reports the signal: %v", records())
	}
}

// TestGeneratorGenerateNeverRan is the third of reportFailure's cases, and the
// one that is neither of the other two: the executable avroc resolved could not
// be started at all — a file that lost its execute bit between discovery and
// invocation. It is reported as a failure to run, without an exit status and
// without a signal, because there was no process to produce either.
func TestGeneratorGenerateNeverRan(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	log, records := recordingLogger()
	executablePath := writeShellGenerator(t, "exit 0\n")
	if err := os.Chmod(executablePath, 0o644); err != nil {
		t.Fatal(err)
	}
	g := testGenerator(t, executablePath)
	g.log = log

	projectRoot, outputDir := newProject(t)
	err := generateOne(ctx, g, projectRoot, outputDir, nil, testSchema("User"))
	if err == nil {
		t.Fatal("generate reported success for a generator that never ran")
	}
	if !strings.Contains(err.Error(), "failed to run") {
		t.Errorf("error %q does not report a generator that never ran", err)
	}
	if !strings.Contains(err.Error(), testGeneratorName) {
		t.Errorf("error %q does not name the generator", err)
	}
	if strings.Contains(err.Error(), "exited with status") || strings.Contains(err.Error(), "signal") {
		t.Errorf("error %q reports a generator that never ran as one that exited", err)
	}

	var reported bool
	for _, r := range records() {
		if r.message == "failed to run generator" {
			reported = true
			if r.attrs["generator"] != testGeneratorName {
				t.Errorf("generator attribute = %q, want %q", r.attrs["generator"], testGeneratorName)
			}
		}
	}
	if !reported {
		t.Errorf("nothing in the log reports the generator that never ran: %v", records())
	}
}

// TestGeneratorStandardErrorReachesAvrocsUnaltered is #190: avroc analyses the
// exit status and nothing else, so a generator's standard error is passed
// through to avroc's own — the same bytes, in the same order, with no prefix and
// no reformatting.
//
// The lines the generator writes are deliberately the ones the removed
// diagnostic format used to rewrite: a severity-prefixed line, a line that was
// never a diagnostic, a bare severity with an empty message, a blank line, and a
// final line without its newline. Under the old contract each of those came out
// as something other than what was written; under this one none of them does.
//
// Exiting zero after writing to standard error is the other half of the rule —
// the exit status is the verdict and what is on stderr is not.
func TestGeneratorStandardErrorReachesAvrocsUnaltered(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	const written = "error: com.example.User: package_name is required\n" +
		"Traceback (most recent call last):\n" +
		"error:\n" +
		"\n" +
		"  indented under a stack trace\n" +
		"trailing without a newline"

	g := testGenerator(t, writeShellGenerator(t, fmt.Sprintf("printf '%%s' '%s' >&2\nexit 0\n", written)))

	projectRoot, outputDir := newProject(t)
	got := captureStderr(t, func() {
		if err := generateOne(ctx, g, projectRoot, outputDir, nil, testSchema("User")); err != nil {
			t.Errorf("a generator that exited zero after writing to standard error failed the run: %v", err)
		}
	})

	if got != written {
		t.Errorf("avroc's standard error carried\n%q\nwant\n%q", got, written)
	}
}

// TestGeneratorStandardErrorArrivesBeforeTheProcessExits is the other half of
// the passthrough: the bytes reach avroc's standard error as the generator
// writes them, not when it exits. A generator that complains and then hangs is
// the case that makes the difference visible, and it is the one a user is most
// likely to hit.
func TestGeneratorStandardErrorArrivesBeforeTheProcessExits(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	release := filepath.Join(t.TempDir(), "release")
	g := testGenerator(t, writeShellGenerator(t, fmt.Sprintf(`echo 'still running' >&2
while [ ! -f '%s' ]; do sleep 0.05; done
exit 0
`, release)))

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	restore := swapStderr(t, w)

	projectRoot, outputDir := newProject(t)
	done := make(chan error, 1)
	go func() {
		done <- generateOne(ctx, g, projectRoot, outputDir, nil, testSchema("User"))
	}()

	// The read blocks until the generator writes, and the generator does not
	// exit until this test lets it — so a line read here is one that arrived
	// while the process was still running.
	line := make(chan string, 1)
	go func() {
		buf := make([]byte, len("still running\n"))
		if _, err := io.ReadFull(r, buf); err != nil {
			line <- ""
			return
		}
		line <- string(buf)
	}()

	select {
	case got := <-line:
		if got != "still running\n" {
			t.Errorf("avroc's standard error carried %q before the generator exited, want %q", got, "still running\n")
		}
	case <-time.After(30 * time.Second):
		t.Error("nothing reached avroc's standard error while the generator was still running")
	}

	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Errorf("generate failed: %v", err)
	}

	restore()
	_ = w.Close()
}

// swapStderr points os.Stderr at f for the rest of the test and returns the
// restore, which is also registered with Cleanup so that a failing test cannot
// leave the process writing into a closed pipe. Calling it twice is harmless.
//
// The process-wide variable is what has to move: a generation invocation hands
// the child avroc's own descriptor rather than a writer of its choosing, which
// is the property under test, so there is nothing narrower to intercept.
func swapStderr(t *testing.T, f *os.File) (restore func()) {
	t.Helper()

	saved := os.Stderr
	os.Stderr = f

	var once sync.Once
	restore = func() { once.Do(func() { os.Stderr = saved }) }
	t.Cleanup(restore)
	return restore
}

// captureStderr runs fn with os.Stderr replaced by a pipe and returns everything
// written to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	restore := swapStderr(t, w)

	// Drained concurrently, so that a generator writing more than the pipe's
	// buffer holds cannot deadlock against the read.
	var out bytes.Buffer
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		_, _ = io.Copy(&out, r)
	}()

	func() {
		defer restore()
		defer func() { _ = w.Close() }()
		fn()
	}()

	<-drained
	return out.String()
}

// recordedLine is one slog record, reduced to what these tests assert on.
type recordedLine struct {
	level   slog.Level
	message string
	attrs   map[string]string
}

func (r recordedLine) String() string {
	return fmt.Sprintf("%v %q %v", r.level, r.message, r.attrs)
}

// recordingLogger returns a logger that keeps every record, and the accessor
// that reads them back. The accessor copies, so a caller ranging over it cannot
// race a generator still being run by the pool.
func recordingLogger() (*slog.Logger, func() []recordedLine) {
	h := &recordingHandler{}
	return slog.New(h), h.records
}

type recordingHandler struct {
	mu    sync.Mutex
	lines []recordedLine
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	line := recordedLine{
		level:   r.Level,
		message: r.Message,
		attrs:   make(map[string]string),
	}
	r.Attrs(func(a slog.Attr) bool {
		line.attrs[a.Key] = a.Value.String()
		return true
	})

	h.mu.Lock()
	defer h.mu.Unlock()
	h.lines = append(h.lines, line)
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingHandler) records() []recordedLine {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]recordedLine(nil), h.lines...)
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

// tracedTask is a generator that records when it started and when it finished
// into a log every generator in the run appends to, holds that window open for
// pause seconds, and writes one file of its own.
//
// The pause is what makes the fan-out observable. Two generators that did
// nothing but append two lines each could run at the same moment and still not
// interleave, so a log of them would say nothing either way; a window every
// generator holds open for the same length of time turns "how many ran at once"
// into something the log records rather than something a test hopes for.
func tracedTask(t *testing.T, name, output, logPath, pause string) genTask {
	t.Helper()

	body := fmt.Sprintf(`set -e
while [ $# -gt 0 ]; do
  case "$1" in
    --out) out=$2; shift 2 ;;
    *) shift ;;
  esac
done
printf '%%s start\n' '%s' >> '%s'
sleep %s
printf '%%s end\n' '%s' >> '%s'
printf '%%s\n' 'produced by avroc-gen-%s' > "$out/%s.txt"
`, name, logPath, pause, name, logPath, name, name)

	return genTask{
		name:           "avroc-gen-" + name,
		executablePath: writeNamedShellGenerator(t, name, body),
		output:         output,
		schemas:        []*avrocpb.Schema{testSchema("User")},
	}
}

// traceEvent is one line a tracedTask wrote: a generator's name and whether it
// was starting or finishing.
type traceEvent struct {
	generator string
	event     string
}

// readTrace reads back what the generators of a run recorded, in the order the
// appends landed. A trace no generator wrote to is not there at all, which is
// itself an answer — none of them ran.
func readTrace(t *testing.T, path string) []traceEvent {
	t.Helper()

	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}

	var events []traceEvent
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		name, event, ok := strings.Cut(line, " ")
		if !ok {
			t.Fatalf("trace line %q is not %q", line, "<generator> <event>")
		}
		events = append(events, traceEvent{generator: name, event: event})
	}
	return events
}

// peakConcurrency is the most generators a trace ever had running at once.
func peakConcurrency(t *testing.T, path string) int {
	t.Helper()

	var running, peak int
	for _, e := range readTrace(t, path) {
		switch e.event {
		case "start":
			running++
			peak = max(peak, running)
		case "end":
			running--
		default:
			t.Fatalf("trace holds the unknown event %q", e.event)
		}
	}
	return peak
}

// generatorsThatStarted is every generator a trace saw begin, in the order they
// began.
func generatorsThatStarted(t *testing.T, path string) []string {
	t.Helper()

	var started []string
	for _, e := range readTrace(t, path) {
		if e.event == "start" {
			started = append(started, e.generator)
		}
	}
	return started
}

// generatorsReported is every generator the run logged output for, in the order
// it reported them.
func generatorsReported(records []recordedLine) []string {
	var reported []string
	for _, r := range records {
		if r.message == "generated output" {
			reported = append(reported, r.attrs["generator"])
		}
	}
	return reported
}

// TestGenerateAllBoundsHowManyGeneratorsRunAtOnce is #184: the fan-out is the
// machine's rather than the manifest's, and a manifest larger than the bound
// still runs every generator in it.
//
// The bound is forced to one, which is the smallest there is and the one a
// manifest of four is unambiguously larger than. GOMAXPROCS alone would not
// produce this: the unit being bounded is a process, and the Go scheduler does
// not bound those — an unbounded pool forks every generator the moment it is
// submitted however few processors the runtime was given.
func TestGenerateAllBoundsHowManyGeneratorsRunAtOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()

	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	bound := maxConcurrentGenerators()

	log, records := recordingLogger()
	projectRoot, output := newProject(t)
	trace := filepath.Join(t.TempDir(), "trace")

	names := []string{"a", "b", "c", "d"}
	tasks := make([]genTask, 0, len(names))
	for _, name := range names {
		tasks = append(tasks, tracedTask(t, name, output, trace, "0.2"))
	}

	if err := generateAll(ctx, log, projectRoot, tasks); err != nil {
		t.Fatal(err)
	}

	if peak := peakConcurrency(t, trace); peak > bound {
		t.Errorf("%d generators ran at once, want at most the bound of %d", peak, bound)
	}

	// Bounded is not skipped: every generator the manifest declared ran to
	// completion and its file is in the tree.
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(output, name+".txt")); err != nil {
			t.Errorf("generator %q did not reach the output tree: %v", name, err)
		}
	}

	// And the outcome is the manifest's order, which is the invariant a limiter
	// is the obvious way to break: results are stored at each task's own index
	// rather than appended as they finish.
	want := []string{"avroc-gen-a", "avroc-gen-b", "avroc-gen-c", "avroc-gen-d"}
	if got := generatorsReported(records()); !slices.Equal(got, want) {
		t.Errorf("generators reported in the order %q, want the manifest's %q", got, want)
	}
}

// TestGenerateAllIsTheManifestsOrderWhateverTheBound runs one manifest twice —
// once bounded to a single generator, once at the bound the machine gives it —
// and requires the two runs to be the same run.
//
// The generators finish in the reverse of the manifest's order when they are
// allowed to overlap and in the manifest's order when they are not, so the two
// runs genuinely differ in who finished when. What must not differ is anything
// the user sees: the files merged, the record of them, and the order the run is
// reported in.
func TestGenerateAllIsTheManifestsOrderWhateverTheBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()

	names := []string{"a", "b", "c", "d"}
	// Reverse of the manifest's order: the first generator declared is the last
	// to finish whenever they run together.
	pauses := []string{"0.4", "0.3", "0.2", "0.1"}

	run := func(t *testing.T) (record []byte, reported []string, finished []string) {
		t.Helper()

		log, records := recordingLogger()
		projectRoot, output := newProject(t)
		trace := filepath.Join(t.TempDir(), "trace")

		tasks := make([]genTask, 0, len(names))
		for i, name := range names {
			tasks = append(tasks, tracedTask(t, name, output, trace, pauses[i]))
		}
		if err := generateAll(ctx, log, projectRoot, tasks); err != nil {
			t.Fatal(err)
		}

		for _, name := range names {
			if _, err := os.Stat(filepath.Join(output, name+".txt")); err != nil {
				t.Errorf("generator %q did not reach the output tree: %v", name, err)
			}
		}

		b, err := os.ReadFile(filepath.Join(projectRoot, outputRecordFilename))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range readTrace(t, trace) {
			if e.event == "end" {
				finished = append(finished, e.generator)
			}
		}
		return b, generatorsReported(records()), finished
	}

	unboundedRecord, unboundedReport, unboundedFinish := run(t)

	boundedRecord, boundedReport, boundedFinish := func() ([]byte, []string, []string) {
		defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
		return run(t)
	}()

	want := []string{"avroc-gen-a", "avroc-gen-b", "avroc-gen-c", "avroc-gen-d"}
	if !slices.Equal(unboundedReport, want) {
		t.Errorf("unbounded run reported %q, want the manifest's %q", unboundedReport, want)
	}
	if !slices.Equal(boundedReport, want) {
		t.Errorf("bounded run reported %q, want the manifest's %q", boundedReport, want)
	}
	if !bytes.Equal(unboundedRecord, boundedRecord) {
		t.Errorf("the record depends on the bound:\n%s\n%s", unboundedRecord, boundedRecord)
	}

	// Not an assertion about the bound so much as evidence that the assertions
	// above were worth making: if the two runs finished in the same order, the
	// pauses stopped separating them and this test has been checking nothing.
	if slices.Equal(unboundedFinish, boundedFinish) {
		t.Logf("both runs finished in the order %q; the bound made no observable difference", boundedFinish)
	}
}

// TestGenerateAllUnderTheBoundStillFailsAsAWhole is the failure path with a
// manifest larger than the bound: a generator queued behind the failing one is
// still run, the run still fails, and nothing any of them produced is adopted.
func TestGenerateAllUnderTheBoundStillFailsAsAWhole(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()

	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	projectRoot, output := newProject(t)
	trace := filepath.Join(t.TempDir(), "trace")

	failing := genTask{
		name: "avroc-gen-failing",
		executablePath: writeNamedShellGenerator(t, "failing", `echo 'error: com.example.User: nope' >&2
exit 1
`),
		output:  output,
		schemas: []*avrocpb.Schema{testSchema("User")},
	}
	tasks := []genTask{
		tracedTask(t, "first", output, trace, "0.1"),
		failing,
		tracedTask(t, "last", output, trace, "0.1"),
	}

	if err := generateAll(ctx, log, projectRoot, tasks); err == nil {
		t.Fatal("generation succeeded with a generator that exited non-zero")
	}

	// The sibling behind the failure in the queue still ran: a failing generator
	// is not a reason to abandon the ones that have not started, because the run
	// has to be able to report every generator's diagnostics and not just the
	// first one's.
	want := []string{"first", "last"}
	if got := generatorsThatStarted(t, trace); !slices.Equal(got, want) {
		t.Errorf("generators that ran = %q, want %q", got, want)
	}

	// Nothing adopted, and no scratch directory left behind: the tree a failed
	// run leaves is the one it started with.
	entries, err := os.ReadDir(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a failed run left %v in the output directory", entries)
	}
}

// TestGenerateAllUnderTheBoundIsCancellable is cancellation with a manifest
// larger than the bound: the generator that was running is waited on, the
// generators still queued behind it never start, and none of them leaves
// anything in the project's tree.
func TestGenerateAllUnderTheBoundIsCancellable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	projectRoot, output := newProject(t)
	trace := filepath.Join(t.TempDir(), "trace")

	// exec, so that the process avroc kills is the one that sleeps; a shell that
	// forked it would leave it orphaned holding the streams avroc reads.
	blocking := genTask{
		name: "avroc-gen-blocking",
		executablePath: writeNamedShellGenerator(t, "blocking", fmt.Sprintf(`printf '%%s start\n' 'blocking' >> '%s'
exec sleep 300
`, trace)),
		output:  output,
		schemas: []*avrocpb.Schema{testSchema("User")},
	}
	tasks := []genTask{
		blocking,
		tracedTask(t, "queued", output, trace, "0.1"),
	}

	done := make(chan error, 1)
	go func() {
		done <- generateAll(ctx, log, projectRoot, tasks)
	}()

	deadline := time.Now().Add(30 * time.Second)
	for len(generatorsThatStarted(t, trace)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the first generator never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("generation reported success for a cancelled run")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("generation did not return after its context was cancelled")
	}

	// The queued generator was submitted, so it is waited on either way; what it
	// must not do is fork a process for a run that has been cancelled.
	if got := generatorsThatStarted(t, trace); !slices.Equal(got, []string{"blocking"}) {
		t.Errorf("generators that ran = %q, want just the one that was running when the run was cancelled", got)
	}

	// And neither of them left a scratch directory in a tree a person is about to
	// look at.
	entries, err := os.ReadDir(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a cancelled run left %v in the output directory", entries)
	}
}

// TestGenerateAllRefusesACollisionUnderTheBound is #118 with a manifest larger
// than the bound: the generators can no longer all be in flight together, and
// the report is the same report — every colliding path, naming every generator
// that claimed it, in an order that is the plans' and not the scheduler's.
func TestGenerateAllRefusesACollisionUnderTheBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()

	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	projectRoot, output := newProject(t)

	var reports []string
	for _, order := range [][]string{{"json", "pcf"}, {"pcf", "json"}} {
		// Four generators against a bound of one, two of which claim user.avsc.
		tasks := []genTask{
			collidingTask(t, "first", output, "only-first.avsc"),
			collidingTask(t, order[0], output, "user.avsc", "only-"+order[0]+".avsc"),
			collidingTask(t, "third", output, "only-third.avsc"),
			collidingTask(t, order[1], output, "user.avsc", "only-"+order[1]+".avsc"),
		}

		err := generateAll(ctx, log, projectRoot, tasks)
		if err == nil {
			t.Fatalf("generation accepted %q and %q both producing user.avsc", order[0], order[1])
		}
		for _, want := range []string{"avroc-gen-json", "avroc-gen-pcf", filepath.Join(output, "user.avsc")} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %q", err, want)
			}
		}
		reports = append(reports, err.Error())

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
