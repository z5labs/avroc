// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"bytes"
	"context"
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
	outputDir := filepath.Join(t.TempDir(), "gen")

	if err := g.generate(ctx, outputDir, options, schema); err != nil {
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
	if want, err := filepath.Abs(outputDir); err != nil {
		t.Fatal(err)
	} else if args[3] != want {
		t.Errorf("--out = %q, want the absolute %q", args[3], want)
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

	// The generator writes its own files now; avroc only creates the directory.
	if _, err := os.Stat(filepath.Join(outputDir, "pkg", "output.go")); err != nil {
		t.Errorf("expected the generator's file under the output directory: %v", err)
	}

	for dir := range descriptorDirs(t) {
		if _, ok := before[dir]; !ok {
			t.Errorf("descriptor directory %q survived the invocation that created it", dir)
		}
	}
}

// TestGeneratorGenerateNonZeroExit is the whole of the failure signal: a
// non-zero exit fails the run, and the descriptor is still removed.
func TestGeneratorGenerateNonZeroExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	g := testGenerator(t, writeShellGenerator(t, "echo 'error: nope' >&2\nexit 3\n"))

	before := descriptorDirs(t)

	err := g.generate(ctx, t.TempDir(), nil, testSchema("User"))
	if err == nil {
		t.Fatal("generate accepted a generator that exited non-zero")
	}
	if !strings.Contains(err.Error(), testGeneratorName) {
		t.Errorf("error %q does not name the generator", err)
	}

	for dir := range descriptorDirs(t) {
		if _, ok := before[dir]; !ok {
			t.Errorf("descriptor directory %q survived a failed invocation", dir)
		}
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

	done := make(chan error, 1)
	go func() {
		done <- g.generate(ctx, t.TempDir(), nil, testSchema("User"))
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
