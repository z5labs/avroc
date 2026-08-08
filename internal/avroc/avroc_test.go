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
	"net"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/cli"
	"github.com/z5labs/avroc/internal/ir"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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

// TestHelperGenerator is a test function that doubles as a real generator subprocess.
// When run normally by "go test", AVROC_TEST_GENERATOR is not set, so it returns immediately.
// When re-invoked as a subprocess, it starts a real gRPC server on the Unix socket.
func TestHelperGenerator(t *testing.T) {
	if os.Getenv("AVROC_TEST_GENERATOR") != "1" {
		return
	}

	socketPath := os.Args[len(os.Args)-1]

	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}

	srv := grpc.NewServer(grpc.Creds(insecure.NewCredentials()))
	avrocpb.RegisterGeneratorServer(srv, &testGeneratorServer{path: os.Getenv("AVROC_TEST_GEN_PATH")})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()

	if err := srv.Serve(lis); err != nil {
		t.Fatal(err)
	}
}

type testGeneratorServer struct {
	avrocpb.UnimplementedGeneratorServer
	path string
}

// testDescriptorGlob matches the descriptor files avroc writes for the
// stand-in generator these tests drive. It leans on the generator's name being
// part of the per-invocation directory, so a leftover from some other test —
// or from another package's temporary files — is not mistaken for this one's.
const testDescriptorGlob = "avroc-gen-test-descriptor-*/" + descriptorFilename

// findDescriptorMatching reports whether a descriptor file holding exactly req
// is readable right now. A leftover from an earlier crashed run may sit beside
// it, so the test is "at least one decodes and equals req" rather than "exactly
// one file exists": a stale file cannot make this pass, and cannot make it fail
// either.
//
// Making good on the second half is why a match that cannot be read or decoded
// is skipped rather than returned as an error. A file that vanished between the
// glob and the read, or that some earlier run left half-written, is not evidence
// about the descriptor this invocation wrote — but it would sit ahead of it in
// the glob's sorted order often enough to make the failure look real. The last
// such error is kept only to be quoted when nothing matched, so a genuinely
// unreadable descriptor still says why rather than reporting a bare miss.
func findDescriptorMatching(req *avrocpb.GenerateRequest) error {
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), testDescriptorGlob))
	if err != nil {
		return fmt.Errorf("failed to search for the descriptor file: %w", err)
	}
	if len(matches) == 0 {
		return fmt.Errorf("no descriptor file under %q while the generator is running", os.TempDir())
	}

	var skipped error
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			skipped = fmt.Errorf("failed to read descriptor %q: %w", m, err)
			continue
		}
		var onDisk avrocpb.GenerateRequest
		if err := proto.Unmarshal(b, &onDisk); err != nil {
			skipped = fmt.Errorf("descriptor %q does not decode: %w", m, err)
			continue
		}
		if proto.Equal(&onDisk, req) {
			return nil
		}
	}

	if skipped != nil {
		return fmt.Errorf("none of the %d descriptor file(s) on disk match what this generator received; last unusable one: %w", len(matches), skipped)
	}
	return fmt.Errorf("none of the %d descriptor file(s) on disk match what this generator received", len(matches))
}

func (s *testGeneratorServer) Generate(req *avrocpb.GenerateRequest, stream avrocpb.Generator_GenerateServer) error {
	// This stand-in generator holds avroc to the producer half of
	// docs/ir/SPEC.md's version rule, on the real client path rather than on a
	// request a test constructed: if avroc stops stamping every descriptor it
	// emits, TestGeneratorGenerate fails here with the reason rather than
	// somewhere downstream with a missing file.
	if err := ir.CheckVersion(req.GetVersion()); err != nil {
		return fmt.Errorf("avroc emitted a descriptor this generator will not read: %w", err)
	}

	// And to the other half docs/plugin/SPEC.md adds: while a generator is
	// running, a complete descriptor carrying exactly what it received is on
	// disk. Checked from inside the subprocess because that is the only vantage
	// point from which "before the generator started, and not yet deleted" is
	// observable at all.
	if err := findDescriptorMatching(req); err != nil {
		return err
	}

	path := s.path
	if path == "" {
		path = "output.go"
	}
	last := true
	return stream.Send(&avrocpb.GenerateResponse{
		Path:    &path,
		Content: []byte("package main\n"),
		Last:    &last,
	})
}

func TestGeneratorGenerate(t *testing.T) {
	t.Setenv("AVROC_TEST_GENERATOR", "1")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	g := generator{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		env: cli.EnvironmentFunc(func(key string) (string, bool) {
			if key == "AVROC_GENERATOR_ARGS" {
				return "-test.run=TestHelperGenerator --", true
			}
			return "", false
		}),
		name:           "avroc-gen-test",
		executablePath: os.Args[0],
	}

	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name: proto.String("User"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("name"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Reference{
									Reference: &avrocpb.Reference{
										Name: proto.String("string"),
										Kind: avrocpb.TypeRefKind_TYPE_REF_KIND_PRIMITIVE.Enum(),
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// The descriptor's lifetime ends when the generator exits, so the set of
	// descriptor directories on disk must not grow across an invocation. Taken
	// before rather than asserted as "none afterwards", because a crashed
	// earlier run may have left one and that is not this test's failure.
	before := descriptorDirs(t)

	outputDir := t.TempDir()
	err := g.generate(ctx, outputDir, nil, schema)
	if err != nil {
		t.Fatal(err)
	}

	// avroc, not the generator, performs the write. Confirm the streamed file
	// landed under the output directory.
	written := filepath.Join(outputDir, "output.go")
	if _, err := os.Stat(written); err != nil {
		t.Errorf("expected generated file at %q: %v", written, err)
	}

	for dir := range descriptorDirs(t) {
		if _, ok := before[dir]; !ok {
			t.Errorf("descriptor directory %q survived the invocation that created it", dir)
		}
	}
}

// descriptorDirs is the set of per-invocation descriptor directories currently
// on disk for the stand-in generator.
func descriptorDirs(t *testing.T) map[string]struct{} {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "avroc-gen-test-descriptor-*"))
	if err != nil {
		t.Fatal(err)
	}
	dirs := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		dirs[m] = struct{}{}
	}
	return dirs
}

// fakeClientStream replays a fixed sequence of GenerateResponse messages,
// implementing avrocpb.Generator_GenerateClient for writeStream tests.
type fakeClientStream struct {
	grpc.ClientStream
	msgs []*avrocpb.GenerateResponse
	i    int
}

func (f *fakeClientStream) Recv() (*avrocpb.GenerateResponse, error) {
	if f.i >= len(f.msgs) {
		return nil, io.EOF
	}
	m := f.msgs[f.i]
	f.i++
	return m, nil
}

func TestWriteStream(t *testing.T) {
	g := generator{
		log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		name: "avroc-gen-test",
	}

	t.Run("writes reassembled file under output root", func(t *testing.T) {
		root := t.TempDir()
		path := "pkg/person.go"
		last := func(b bool) *bool { return &b }
		stream := &fakeClientStream{msgs: []*avrocpb.GenerateResponse{
			{Path: &path, Content: []byte("package pkg\n"), Last: last(false)},
			{Path: &path, Content: []byte("// trailer\n"), Last: last(true)},
		}}

		written, err := g.writeStream(root, stream)
		if err != nil {
			t.Fatal(err)
		}
		if len(written) != 1 {
			t.Fatalf("expected 1 written file, got %d", len(written))
		}

		got, err := os.ReadFile(filepath.Join(root, "pkg", "person.go"))
		if err != nil {
			t.Fatal(err)
		}
		if want := "package pkg\n// trailer\n"; string(got) != want {
			t.Errorf("content = %q, want %q", string(got), want)
		}
	})

	t.Run("rejects paths that escape the output root", func(t *testing.T) {
		for _, bad := range []string{"../escape.go", "/etc/evil.go", "a/../../escape.go"} {
			root := t.TempDir()
			path := bad
			last := true
			stream := &fakeClientStream{msgs: []*avrocpb.GenerateResponse{
				{Path: &path, Content: []byte("package x\n"), Last: &last},
			}}

			if _, err := g.writeStream(root, stream); err == nil {
				t.Errorf("path %q: expected error, got nil", bad)
			}

			// Nothing must be written next to the output root.
			if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.go")); !os.IsNotExist(err) {
				t.Errorf("path %q: file escaped the output root", bad)
			}
		}
	})

	t.Run("errors when the stream ends with an unterminated file", func(t *testing.T) {
		root := t.TempDir()
		path := "pkg/person.go"
		last := func(b bool) *bool { return &b }
		stream := &fakeClientStream{msgs: []*avrocpb.GenerateResponse{
			{Path: &path, Content: []byte("package pkg\n"), Last: last(false)},
			// No terminating chunk: the stream just ends.
		}}

		if _, err := g.writeStream(root, stream); err == nil {
			t.Fatal("expected error for unterminated file, got nil")
		}

		// The partial file must not be left behind in the output tree.
		if _, err := os.Stat(filepath.Join(root, "pkg", "person.go")); !os.IsNotExist(err) {
			t.Errorf("partial file was not discarded: %v", err)
		}
	})

	t.Run("errors when a chunk follows a terminated file", func(t *testing.T) {
		root := t.TempDir()
		path := "person.go"
		last := func(b bool) *bool { return &b }
		stream := &fakeClientStream{msgs: []*avrocpb.GenerateResponse{
			{Path: &path, Content: []byte("package pkg\n"), Last: last(true)},
			{Path: &path, Content: []byte("// extra\n"), Last: last(true)},
		}}

		if _, err := g.writeStream(root, stream); err == nil {
			t.Fatal("expected error for chunk after termination, got nil")
		}
	})
}

func TestGeneratorGenerate_RejectsUnsafePath(t *testing.T) {
	t.Setenv("AVROC_TEST_GENERATOR", "1")
	t.Setenv("AVROC_TEST_GEN_PATH", "../escape.go")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	g := generator{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		env: cli.EnvironmentFunc(func(key string) (string, bool) {
			if key == "AVROC_GENERATOR_ARGS" {
				return "-test.run=TestHelperGenerator --", true
			}
			return "", false
		}),
		name:           "avroc-gen-test",
		executablePath: os.Args[0],
	}

	schema := &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{Name: proto.String("User")},
			},
		},
	}

	outputDir := t.TempDir()
	if err := g.generate(ctx, outputDir, nil, schema); err == nil {
		t.Fatal("expected error for generator returning an escaping path")
	}

	if _, err := os.Stat(filepath.Join(filepath.Dir(outputDir), "escape.go")); !os.IsNotExist(err) {
		t.Errorf("file escaped the output root")
	}
}
