// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package plugin

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/cli"
	"github.com/z5labs/avroc/internal/ir"

	"google.golang.org/protobuf/proto"
)

func option(name, value string) *avrocpb.Option {
	return &avrocpb.Option{Name: proto.String(name), Value: proto.String(value)}
}

func TestParseArgs(t *testing.T) {
	testCases := []struct {
		name string
		args []string
		want *Invocation
	}{
		{
			name: "the vector avroc emits",
			args: []string{"--descriptor", "/tmp/d/descriptor.binpb", "--out", "/w/gen"},
			want: &Invocation{Descriptor: "/tmp/d/descriptor.binpb", Out: "/w/gen"},
		},
		{
			name: "options in the order they were given",
			args: []string{"--descriptor", "d", "--out", "o", "--opt", "b=2", "--opt", "a=1"},
			want: &Invocation{
				Descriptor: "d",
				Out:        "o",
				Options:    []*avrocpb.Option{option("b", "2"), option("a", "1")},
			},
		},
		{
			name: "an option value may be empty",
			args: []string{"--descriptor", "d", "--out", "o", "--opt", "k="},
			want: &Invocation{Descriptor: "d", Out: "o", Options: []*avrocpb.Option{option("k", "")}},
		},
		{
			name: "an option value may carry further equals signs",
			args: []string{"--descriptor", "d", "--out", "o", "--opt", "k=a=b"},
			want: &Invocation{Descriptor: "d", Out: "o", Options: []*avrocpb.Option{option("k", "a=b")}},
		},
		{
			name: "- names standard input",
			args: []string{"--descriptor", "-", "--out", "o"},
			want: &Invocation{Descriptor: "-", Out: "o"},
		},
		{
			name: "-- ends the options",
			args: []string{"--descriptor", "d", "--out", "o", "--"},
			want: &Invocation{Descriptor: "d", Out: "o"},
		},
		{
			name: "the order of --descriptor and --out is not fixed",
			args: []string{"--out", "o", "--opt", "k=v", "--descriptor", "d"},
			want: &Invocation{Descriptor: "d", Out: "o", Options: []*avrocpb.Option{option("k", "v")}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseArgs(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if got.Descriptor != tc.want.Descriptor {
				t.Errorf("Descriptor = %q, want %q", got.Descriptor, tc.want.Descriptor)
			}
			if got.Out != tc.want.Out {
				t.Errorf("Out = %q, want %q", got.Out, tc.want.Out)
			}
			if len(got.Options) != len(tc.want.Options) {
				t.Fatalf("got %d options, want %d", len(got.Options), len(tc.want.Options))
			}
			for i, opt := range got.Options {
				if !proto.Equal(opt, tc.want.Options[i]) {
					t.Errorf("option %d = %v, want %v", i, opt, tc.want.Options[i])
				}
			}
		})
	}
}

func TestParseArgsRejects(t *testing.T) {
	testCases := []struct {
		name string
		args []string
	}{
		{name: "nothing at all", args: nil},
		{name: "no descriptor", args: []string{"--out", "o"}},
		{name: "no output directory", args: []string{"--descriptor", "d"}},
		{name: "a descriptor with no value", args: []string{"--out", "o", "--descriptor"}},
		{name: "an output directory with no value", args: []string{"--descriptor", "d", "--out"}},
		{name: "an option with no value", args: []string{"--descriptor", "d", "--out", "o", "--opt"}},
		{name: "two descriptors", args: []string{"--descriptor", "a", "--descriptor", "b", "--out", "o"}},
		{name: "two output directories", args: []string{"--descriptor", "d", "--out", "a", "--out", "b"}},
		{name: "an unknown option", args: []string{"--descriptor", "d", "--out", "o", "--verbose"}},
		{name: "an operand", args: []string{"--descriptor", "d", "--out", "o", "schema.avdl"}},
		{name: "an operand after --", args: []string{"--descriptor", "d", "--out", "o", "--", "schema.avdl"}},
		{name: "an option that is not k=v", args: []string{"--descriptor", "d", "--out", "o", "--opt", "k"}},
		{name: "an option with an empty key", args: []string{"--descriptor", "d", "--out", "o", "--opt", "=v"}},
		{
			// The joined form is one a plugin MAY accept and avroc never emits,
			// so accepting it here would be surface with nothing behind it.
			name: "the joined form",
			args: []string{"--descriptor=d", "--out=o"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseArgs(tc.args); err == nil {
				t.Errorf("ParseArgs(%q) accepted a vector the contract does not define", tc.args)
			}
		})
	}
}

func testDescriptor() *avrocpb.GenerateRequest {
	return &avrocpb.GenerateRequest{
		Version: proto.Int32(ir.Version),
		Schemas: []*avrocpb.Schema{
			{
				Namespace: proto.String("com.example"),
				Type: &avrocpb.Type{
					Type: &avrocpb.Type_Record{
						Record: &avrocpb.Record{
							Name:     proto.String("User"),
							FullName: proto.String("com.example.User"),
						},
					},
				},
			},
		},
	}
}

func TestReadDescriptor(t *testing.T) {
	desc := testDescriptor()
	// The options on the wire are deliberately different from the ones on the
	// command line, so the test can tell which of the two was believed.
	desc.Options = []*avrocpb.Option{option("stale", "yes")}

	b, err := proto.Marshal(desc)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("from a file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "descriptor.binpb")
		if err := os.WriteFile(path, b, 0o444); err != nil {
			t.Fatal(err)
		}

		inv, err := ParseArgs([]string{"--descriptor", path, "--out", t.TempDir(), "--opt", "package_name=gen"})
		if err != nil {
			t.Fatal(err)
		}

		got, err := inv.ReadDescriptor(nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.GetVersion() != ir.Version {
			t.Errorf("version = %d, want %d", got.GetVersion(), ir.Version)
		}
		if len(got.GetSchemas()) != 1 {
			t.Fatalf("got %d schemas, want 1", len(got.GetSchemas()))
		}
		// docs/plugin/SPEC.md configures a generator through --opt, so the
		// vector wins over whatever the encoded descriptor happens to carry.
		want := []*avrocpb.Option{option("package_name", "gen")}
		if len(got.GetOptions()) != 1 || !proto.Equal(got.GetOptions()[0], want[0]) {
			t.Errorf("options = %v, want %v", got.GetOptions(), want)
		}
	})

	t.Run("from standard input", func(t *testing.T) {
		inv, err := ParseArgs([]string{"--descriptor", "-", "--out", t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}

		got, err := inv.ReadDescriptor(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		if got.GetVersion() != ir.Version {
			t.Errorf("version = %d, want %d", got.GetVersion(), ir.Version)
		}
	})

	t.Run("a descriptor that is not there", func(t *testing.T) {
		inv, err := ParseArgs([]string{"--descriptor", filepath.Join(t.TempDir(), "absent"), "--out", t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := inv.ReadDescriptor(nil); err == nil {
			t.Error("ReadDescriptor accepted a descriptor that does not exist")
		}
	})

	t.Run("a descriptor that does not decode", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "descriptor.binpb")
		if err := os.WriteFile(path, []byte("not a descriptor"), 0o444); err != nil {
			t.Fatal(err)
		}

		inv, err := ParseArgs([]string{"--descriptor", path, "--out", t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := inv.ReadDescriptor(nil); err == nil {
			t.Error("ReadDescriptor accepted bytes that are not a descriptor")
		}
	})
}

func TestOutputPath(t *testing.T) {
	root := t.TempDir()

	t.Run("resolves a relative path under the output directory", func(t *testing.T) {
		got, err := OutputPath(root, "pkg/user.go")
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(root, "pkg", "user.go"); got != want {
			t.Errorf("OutputPath = %q, want %q", got, want)
		}
	})

	t.Run("refuses to leave the output directory", func(t *testing.T) {
		for _, bad := range []string{"../escape.go", "a/../../escape.go", "/etc/evil.go", ".."} {
			if _, err := OutputPath(root, bad); err == nil {
				t.Errorf("OutputPath accepted %q", bad)
			}
		}
	})
}

func TestWriteFile(t *testing.T) {
	out := t.TempDir()
	inv := &Invocation{Out: out}

	if err := inv.WriteFile("pkg/user.go", []byte("package pkg\n")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(out, "pkg", "user.go"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "package pkg\n"; string(got) != want {
		t.Errorf("content = %q, want %q", string(got), want)
	}

	if err := inv.WriteFile("../escape.go", []byte("package pkg\n")); err == nil {
		t.Error("WriteFile accepted a path outside the output directory")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(out), "escape.go")); !os.IsNotExist(err) {
		t.Error("a file escaped the output directory")
	}
}

// echoGenerate is a stand-in generator: it emits every schema's full name as a
// file, in two chunks, so the reassembly the adapter performs is exercised.
func echoGenerate(req *avrocpb.GenerateRequest, stream avrocpb.Generator_GenerateServer) error {
	for _, schema := range req.GetSchemas() {
		name := schema.GetType().GetRecord().GetFullName() + ".txt"
		for i, chunk := range [][]byte{[]byte("first\n"), []byte("second\n")} {
			last := i == 1
			if err := stream.Send(&avrocpb.GenerateResponse{
				Path:    proto.String(name),
				Content: chunk,
				Last:    proto.Bool(last),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func newTestCLI(args ...string) (cli.Context, *bytes.Buffer) {
	var logs bytes.Buffer
	return cli.Context{
		Log:  slog.New(slog.NewTextHandler(&logs, nil)),
		Env:  cli.EnvironmentFunc(func(string) (string, bool) { return "", false }),
		Args: args,
	}, &logs
}

func TestMain_WritesGeneratedFiles(t *testing.T) {
	desc := testDescriptor()
	b, err := proto.Marshal(desc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "descriptor.binpb")
	if err := os.WriteFile(path, b, 0o444); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	c, _ := newTestCLI("--descriptor", path, "--out", out)

	if code := Main(t.Context(), c, "avroc-gen-test", echoGenerate); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	got, err := os.ReadFile(filepath.Join(out, "com.example.User.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "first\nsecond\n"; string(got) != want {
		t.Errorf("content = %q, want %q", string(got), want)
	}
}

func TestMain_Failures(t *testing.T) {
	descriptorPath := func(t *testing.T) string {
		t.Helper()
		b, err := proto.Marshal(testDescriptor())
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "descriptor.binpb")
		if err := os.WriteFile(path, b, 0o444); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("an argument vector the contract does not define", func(t *testing.T) {
		c, _ := newTestCLI("--out", t.TempDir())
		if code := Main(t.Context(), c, "avroc-gen-test", echoGenerate); code == 0 {
			t.Error("exit code = 0 for a vector with no descriptor")
		}
	})

	t.Run("a descriptor that is not there", func(t *testing.T) {
		c, _ := newTestCLI("--descriptor", filepath.Join(t.TempDir(), "absent"), "--out", t.TempDir())
		if code := Main(t.Context(), c, "avroc-gen-test", echoGenerate); code == 0 {
			t.Error("exit code = 0 for a descriptor that does not exist")
		}
	})

	t.Run("a generator that fails", func(t *testing.T) {
		out := t.TempDir()
		c, logs := newTestCLI("--descriptor", descriptorPath(t), "--out", out)

		failing := func(*avrocpb.GenerateRequest, avrocpb.Generator_GenerateServer) error {
			return io.ErrUnexpectedEOF
		}
		if code := Main(t.Context(), c, "avroc-gen-test", failing); code == 0 {
			t.Error("exit code = 0 for a generator that returned an error")
		}
		if !strings.Contains(logs.String(), "failed to generate") {
			t.Errorf("the failure was not reported: %s", logs.String())
		}
	})

	t.Run("a generator that stops with a file unterminated", func(t *testing.T) {
		c, _ := newTestCLI("--descriptor", descriptorPath(t), "--out", t.TempDir())

		halfEmitted := func(_ *avrocpb.GenerateRequest, stream avrocpb.Generator_GenerateServer) error {
			return stream.Send(&avrocpb.GenerateResponse{
				Path:    proto.String("user.go"),
				Content: []byte("package pkg\n"),
				Last:    proto.Bool(false),
			})
		}
		if code := Main(t.Context(), c, "avroc-gen-test", halfEmitted); code == 0 {
			t.Error("exit code = 0 for a generator that left a file unterminated")
		}
	})
}
