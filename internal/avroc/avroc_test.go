// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/z5labs/avroc/internal/avrocpb"
	"github.com/z5labs/avroc/internal/cli"

	"github.com/z5labs/avro-go/idl"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

func TestMapToProtoSchema_Record(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name:      "User",
			Namespace: "com.example",
			Aliases:   []string{"Person"},
			Fields: []*idl.Field{
				{
					Name:      "name",
					Type:      &idl.Ident{Value: "string"},
					SortOrder: idl.SortOrderAsc,
				},
				{
					Name:    "age",
					Aliases: []string{"years"},
					Type:    &idl.Ident{Value: "int"},
				},
				{
					Name: "tags",
					Type: &idl.Array{
						Items: &idl.Ident{Value: "string"},
					},
				},
				{
					Name: "metadata",
					Type: &idl.Map{
						Values: &idl.Ident{Value: "string"},
					},
				},
				{
					Name: "optionalField",
					Type: &idl.Union{
						Types: []idl.Type{
							&idl.Ident{Value: "null"},
							&idl.Ident{Value: "string"},
						},
					},
				},
			},
		},
	}

	got, err := mapToProtoSchema(schema)
	if err != nil {
		t.Fatal(err)
	}

	if got.GetNamespace() != "com.example" {
		t.Errorf("namespace = %q, want %q", got.GetNamespace(), "com.example")
	}

	rec := got.GetType().GetRecord()
	if rec == nil {
		t.Fatal("expected record type")
	}
	if rec.GetName() != "User" {
		t.Errorf("record name = %q, want %q", rec.GetName(), "User")
	}
	if len(rec.GetAliases()) != 1 || rec.GetAliases()[0] != "Person" {
		t.Errorf("record aliases = %v, want [Person]", rec.GetAliases())
	}
	if len(rec.GetFields()) != 5 {
		t.Fatalf("fields count = %d, want 5", len(rec.GetFields()))
	}

	// name field
	nameField := rec.GetFields()[0]
	if nameField.GetName() != "name" {
		t.Errorf("field[0] name = %q, want %q", nameField.GetName(), "name")
	}
	if nameField.GetType().GetIdent().GetValue() != "string" {
		t.Errorf("field[0] type = %v, want ident(string)", nameField.GetType())
	}
	if nameField.GetSortOrder() != avrocpb.SortOrder_SORT_ORDER_ASC {
		t.Errorf("field[0] sort_order = %v, want ASC", nameField.GetSortOrder())
	}

	// age field with alias
	ageField := rec.GetFields()[1]
	if len(ageField.GetAliases()) != 1 || ageField.GetAliases()[0] != "years" {
		t.Errorf("field[1] aliases = %v, want [years]", ageField.GetAliases())
	}

	// tags field (array)
	tagsField := rec.GetFields()[2]
	arr := tagsField.GetType().GetArray()
	if arr == nil {
		t.Fatal("expected array type for tags field")
	}
	if arr.GetItems().GetIdent().GetValue() != "string" {
		t.Errorf("tags items type = %v, want ident(string)", arr.GetItems())
	}

	// metadata field (map)
	metaField := rec.GetFields()[3]
	m := metaField.GetType().GetMapType()
	if m == nil {
		t.Fatal("expected map type for metadata field")
	}
	if m.GetValues().GetValue() != "string" {
		t.Errorf("metadata values type = %q, want %q", m.GetValues().GetValue(), "string")
	}

	// optionalField (union)
	unionField := rec.GetFields()[4]
	u := unionField.GetType().GetUnion()
	if u == nil {
		t.Fatal("expected union type for optionalField")
	}
	if len(u.GetTypes()) != 2 {
		t.Fatalf("union types count = %d, want 2", len(u.GetTypes()))
	}
}

func TestMapToProtoSchema_Enum(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Enum{
			Name:      "Status",
			Namespace: "com.example",
			Values: []*idl.Ident{
				{Value: "ACTIVE"},
				{Value: "INACTIVE"},
			},
			Default: &idl.Ident{Value: "ACTIVE"},
		},
	}

	got, err := mapToProtoSchema(schema)
	if err != nil {
		t.Fatal(err)
	}

	enum := got.GetType().GetEnumType()
	if enum == nil {
		t.Fatal("expected enum type")
	}
	if enum.GetName() != "Status" {
		t.Errorf("enum name = %q, want %q", enum.GetName(), "Status")
	}
	if len(enum.GetValues()) != 2 {
		t.Fatalf("enum values count = %d, want 2", len(enum.GetValues()))
	}
	if enum.GetValues()[0].GetValue() != "ACTIVE" {
		t.Errorf("enum value[0] = %q, want %q", enum.GetValues()[0].GetValue(), "ACTIVE")
	}
	if enum.GetDefault().GetValue() != "ACTIVE" {
		t.Errorf("enum default = %q, want %q", enum.GetDefault().GetValue(), "ACTIVE")
	}
}

func TestMapToProtoSchema_Fixed(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Fixed{
			Name:    "MD5",
			Aliases: []string{"Hash"},
			Size:    16,
		},
	}

	got, err := mapToProtoSchema(schema)
	if err != nil {
		t.Fatal(err)
	}

	fixed := got.GetType().GetFixed()
	if fixed == nil {
		t.Fatal("expected fixed type")
	}
	if fixed.GetName() != "MD5" {
		t.Errorf("fixed name = %q, want %q", fixed.GetName(), "MD5")
	}
	if fixed.GetSize() != 16 {
		t.Errorf("fixed size = %d, want 16", fixed.GetSize())
	}
	if len(fixed.GetAliases()) != 1 || fixed.GetAliases()[0] != "Hash" {
		t.Errorf("fixed aliases = %v, want [Hash]", fixed.GetAliases())
	}
}

func TestMapToProtoSchema_WithSupportingTypes(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "com.example",
		Type: &idl.Record{
			Name: "Order",
			Fields: []*idl.Field{
				{
					Name: "item",
					Type: &idl.Ident{Value: "Item"},
				},
			},
		},
		Types: []idl.Type{
			&idl.Record{
				Name: "Item",
				Fields: []*idl.Field{
					{
						Name: "name",
						Type: &idl.Ident{Value: "string"},
					},
				},
			},
		},
	}

	got, err := mapToProtoSchema(schema)
	if err != nil {
		t.Fatal(err)
	}

	if got.GetType().GetRecord().GetName() != "Order" {
		t.Errorf("primary type name = %q, want %q", got.GetType().GetRecord().GetName(), "Order")
	}
	if len(got.GetTypes()) != 1 {
		t.Fatalf("supporting types count = %d, want 1", len(got.GetTypes()))
	}
	if got.GetTypes()[0].GetRecord().GetName() != "Item" {
		t.Errorf("supporting type name = %q, want %q", got.GetTypes()[0].GetRecord().GetName(), "Item")
	}
}

func TestMapToProtoSchema_EnumNilDefault(t *testing.T) {
	schema := &idl.Schema{
		Type: &idl.Enum{
			Name: "Color",
			Values: []*idl.Ident{
				{Value: "RED"},
			},
		},
	}

	got, err := mapToProtoSchema(schema)
	if err != nil {
		t.Fatal(err)
	}

	if got.GetType().GetEnumType().GetDefault() != nil {
		t.Errorf("expected nil default, got %v", got.GetType().GetEnumType().GetDefault())
	}
}

func TestLookupGenerators(t *testing.T) {
	discardLog := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := filepath.Join(string(filepath.Separator), "usr", "local", "bin")

	t.Run("finds generator executables", func(t *testing.T) {
		fsys := fstest.MapFS{
			generatorFixtureName("avroc-gen-go"): generatorFixtureFile(),
			"other-tool":                         generatorFixtureFile(),
		}

		got, err := lookupGenerators(context.Background(), discardLog, staticOpenDir(fsys), dir)
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 1 {
			t.Fatalf("got %d generators, want 1", len(got))
		}
		want := filepath.Join(dir, generatorFixtureName("avroc-gen-go"))
		if got["avroc-gen-go"] != want {
			t.Errorf("avroc-gen-go path = %q, want %q", got["avroc-gen-go"], want)
		}
	})

	t.Run("multiple generators in same directory", func(t *testing.T) {
		fsys := fstest.MapFS{
			generatorFixtureName("avroc-gen-go"):   generatorFixtureFile(),
			generatorFixtureName("avroc-gen-java"): generatorFixtureFile(),
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
				return fstest.MapFS{generatorFixtureName("avroc-gen-go"): generatorFixtureFile()}
			case dir2:
				return fstest.MapFS{generatorFixtureName("avroc-gen-java"): generatorFixtureFile()}
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
		if got["avroc-gen-go"] != filepath.Join(dir1, generatorFixtureName("avroc-gen-go")) {
			t.Errorf("avroc-gen-go path = %q", got["avroc-gen-go"])
		}
		if got["avroc-gen-java"] != filepath.Join(dir2, generatorFixtureName("avroc-gen-java")) {
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
			return fstest.MapFS{generatorFixtureName("avroc-gen-go"): generatorFixtureFile()}
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
		want := filepath.Join(dir, generatorFixtureName("avroc-gen-go"))
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

func TestPrintHelp(t *testing.T) {
	var buf bytes.Buffer
	err := printHelp(&buf, "avroc-gen-go", "avroc-gen-java")
	if err != nil {
		t.Fatal(err)
	}

	output := buf.String()
	if !bytes.Contains([]byte(output), []byte("-go_out string")) {
		t.Errorf("output missing -go_out flag:\n%s", output)
	}
	if !bytes.Contains([]byte(output), []byte("-java_out string")) {
		t.Errorf("output missing -java_out flag:\n%s", output)
	}
	if !bytes.Contains([]byte(output), []byte("-go_opt key=value")) {
		t.Errorf("output missing -go_opt flag:\n%s", output)
	}
	if !bytes.Contains([]byte(output), []byte("-java_opt key=value")) {
		t.Errorf("output missing -java_opt flag:\n%s", output)
	}
	if !bytes.Contains([]byte(output), []byte("Usage: avroc")) {
		t.Errorf("output missing usage line:\n%s", output)
	}
}

func TestOptionFlag(t *testing.T) {
	t.Run("single option", func(t *testing.T) {
		of := &optionFlag{}
		if err := of.Set("package_name=my_package"); err != nil {
			t.Fatal(err)
		}

		opts := of.options()
		if len(opts) != 1 {
			t.Fatalf("got %d options, want 1", len(opts))
		}
		if opts[0].GetName() != "package_name" {
			t.Errorf("option name = %q, want %q", opts[0].GetName(), "package_name")
		}
		if opts[0].GetValue() != "my_package" {
			t.Errorf("option value = %q, want %q", opts[0].GetValue(), "my_package")
		}
	})

	t.Run("multiple options", func(t *testing.T) {
		of := &optionFlag{}
		if err := of.Set("package_name=my_package"); err != nil {
			t.Fatal(err)
		}
		if err := of.Set("other_opt=other_val"); err != nil {
			t.Fatal(err)
		}

		opts := of.options()
		if len(opts) != 2 {
			t.Fatalf("got %d options, want 2", len(opts))
		}
		if opts[0].GetName() != "package_name" {
			t.Errorf("option[0] name = %q, want %q", opts[0].GetName(), "package_name")
		}
		if opts[1].GetName() != "other_opt" {
			t.Errorf("option[1] name = %q, want %q", opts[1].GetName(), "other_opt")
		}
	})

	t.Run("value with equals sign", func(t *testing.T) {
		of := &optionFlag{}
		if err := of.Set("key=val=ue"); err != nil {
			t.Fatal(err)
		}

		opts := of.options()
		if len(opts) != 1 {
			t.Fatalf("got %d options, want 1", len(opts))
		}
		if opts[0].GetName() != "key" {
			t.Errorf("option name = %q, want %q", opts[0].GetName(), "key")
		}
		if opts[0].GetValue() != "val=ue" {
			t.Errorf("option value = %q, want %q", opts[0].GetValue(), "val=ue")
		}
	})

	t.Run("invalid format", func(t *testing.T) {
		of := &optionFlag{}
		if err := of.Set("invalid"); err == nil {
			t.Fatal("expected error for option without '='")
		}
	})

	t.Run("string representation", func(t *testing.T) {
		of := &optionFlag{}
		if err := of.Set("a=b"); err != nil {
			t.Fatal(err)
		}
		if err := of.Set("c=d"); err != nil {
			t.Fatal(err)
		}
		if of.String() != "a=b,c=d" {
			t.Errorf("String() = %q, want %q", of.String(), "a=b,c=d")
		}
	})
}

func TestMain_PathNotSet(t *testing.T) {
	code := Main(context.Background(), cli.Context{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Env: cli.EnvironmentFunc(func(key string) (string, bool) {
			return "", false
		}),
	})

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

func TestMain_NoIDLFiles(t *testing.T) {
	code := Main(context.Background(), cli.Context{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Env: cli.EnvironmentFunc(func(key string) (string, bool) {
			if key == "PATH" {
				return "/nonexistent", true
			}
			return "", false
		}),
		OpenDir: staticOpenDir(fstest.MapFS{}),
		Args:    []string{},
	})

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
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

func (s *testGeneratorServer) Generate(_ *avrocpb.GenerateRequest, stream avrocpb.Generator_GenerateServer) error {
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
								Type: &avrocpb.Type_Ident{
									Ident: &avrocpb.Ident{Value: proto.String("string")},
								},
							},
						},
					},
				},
			},
		},
	}

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

// Verify proto.String is used correctly by checking roundtrip.
func TestMapToProtoSchema_ProtoStringFields(t *testing.T) {
	schema := &idl.Schema{
		Namespace: "ns",
		Type: &idl.Ident{
			Value: "string",
		},
	}

	got, err := mapToProtoSchema(schema)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the proto message can be serialized and deserialized.
	data, err := proto.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}

	var roundtrip avrocpb.Schema
	if err := proto.Unmarshal(data, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if roundtrip.GetNamespace() != "ns" {
		t.Errorf("roundtrip namespace = %q, want %q", roundtrip.GetNamespace(), "ns")
	}
	if roundtrip.GetType().GetIdent().GetValue() != "string" {
		t.Errorf("roundtrip type = %v, want ident(string)", roundtrip.GetType())
	}
}
