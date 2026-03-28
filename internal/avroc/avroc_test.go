// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"os"
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
	t.Run("finds executable generators", func(t *testing.T) {
		fsys := fstest.MapFS{
			"usr/local/bin/avroc-gen-go": &fstest.MapFile{Mode: 0o755},
			"usr/local/bin/other-tool":   &fstest.MapFile{Mode: 0o755},
		}

		got, err := lookupGenerators(fsys, "usr/local/bin")
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 1 {
			t.Fatalf("got %d generators, want 1", len(got))
		}
		if got["avroc-gen-go"] != "/usr/local/bin/avroc-gen-go" {
			t.Errorf("avroc-gen-go path = %q, want %q", got["avroc-gen-go"], "/usr/local/bin/avroc-gen-go")
		}
	})

	t.Run("multiple generators in same directory", func(t *testing.T) {
		fsys := fstest.MapFS{
			"bin/avroc-gen-go":   &fstest.MapFile{Mode: 0o755},
			"bin/avroc-gen-java": &fstest.MapFile{Mode: 0o755},
		}

		got, err := lookupGenerators(fsys, "bin")
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 2 {
			t.Fatalf("got %d generators, want 2", len(got))
		}
	})

	t.Run("multiple directories", func(t *testing.T) {
		fsys := fstest.MapFS{
			"dir1/avroc-gen-go":   &fstest.MapFile{Mode: 0o755},
			"dir2/avroc-gen-java": &fstest.MapFile{Mode: 0o755},
		}

		got, err := lookupGenerators(fsys, "dir1", "dir2")
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 2 {
			t.Fatalf("got %d generators, want 2", len(got))
		}
	})

	t.Run("skips non-executable files", func(t *testing.T) {
		fsys := fstest.MapFS{
			"bin/avroc-gen-go": &fstest.MapFile{Mode: 0o644},
		}

		got, err := lookupGenerators(fsys, "bin")
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 0 {
			t.Fatalf("got %d generators, want 0 (file not executable)", len(got))
		}
	})

	t.Run("nonexistent directory", func(t *testing.T) {
		fsys := fstest.MapFS{}

		got, err := lookupGenerators(fsys, "nonexistent")
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 0 {
			t.Fatalf("got %d generators, want 0", len(got))
		}
	})

	t.Run("empty directory", func(t *testing.T) {
		fsys := fstest.MapFS{
			"bin": &fstest.MapFile{Mode: 0o755 | os.ModeDir},
		}

		got, err := lookupGenerators(fsys, "bin")
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 0 {
			t.Fatalf("got %d generators, want 0", len(got))
		}
	})
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
	if !bytes.Contains([]byte(output), []byte("Usage: avroc")) {
		t.Errorf("output missing usage line:\n%s", output)
	}
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
		Fs:   fstest.MapFS{},
		Args: []string{},
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
	avrocpb.RegisterGeneratorServer(srv, &testGeneratorServer{})

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
}

func (s *testGeneratorServer) Generate(_ context.Context, req *avrocpb.GenerateRequest) (*avrocpb.GenerateResponse, error) {
	return &avrocpb.GenerateResponse{
		OutputFiles: []string{req.GetOutputDirectory() + "/output.go"},
	}, nil
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
	err := g.generate(ctx, outputDir, schema)
	if err != nil {
		t.Fatal(err)
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
