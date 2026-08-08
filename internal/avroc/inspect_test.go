// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
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

func inspectContext(args ...string) cli.Context {
	return cli.Context{
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		OpenDir:    func(dir string) fs.FS { return os.DirFS(dir) },
		WorkingDir: ".",
		Args:       args,
	}
}

// writeInspectFixture writes a descriptor to a temporary file the way avroc
// writes one, and returns both the path and the descriptor.
func writeInspectFixture(t *testing.T) (string, *avrocpb.GenerateRequest) {
	t.Helper()

	desc := newDescriptor(
		[]*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("models")}},
		[]*avrocpb.Schema{
			{
				Namespace: proto.String("com.example"),
				Type: &avrocpb.Type{
					Type: &avrocpb.Type_Record{
						Record: &avrocpb.Record{
							Name:     proto.String("User"),
							FullName: proto.String("com.example.User"),
							Fields: []*avrocpb.Field{
								{
									Name: proto.String("id"),
									Type: &avrocpb.Type{
										Type: &avrocpb.Type_Reference{
											Reference: &avrocpb.Reference{
												Name: proto.String("long"),
												Kind: avrocpb.TypeRefKind_TYPE_REF_KIND_PRIMITIVE.Enum(),
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	)

	path, err := writeDescriptor(t.TempDir(), desc)
	if err != nil {
		t.Fatal(err)
	}
	return path, desc
}

func TestInspectDescriptor(t *testing.T) {
	t.Run("renders a descriptor avroc wrote", func(t *testing.T) {
		// End to end over a real descriptor file: the subcommand must read what
		// writeDescriptor produced, which is the artifact a person actually has
		// in hand when they reach for this.
		path, desc := writeInspectFixture(t)

		var out bytes.Buffer
		if code := inspectDescriptor(context.Background(), inspectContext(), path, nil, &out); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}

		want, err := ir.MarshalDescriptorJSON(desc)
		if err != nil {
			t.Fatal(err)
		}
		if got := out.String(); got != string(want)+"\n" {
			t.Errorf("rendering differs\n got:\n%s\nwant:\n%s\n", got, want)
		}
		if !json.Valid(out.Bytes()) {
			t.Errorf("output is not valid JSON:\n%s", out.String())
		}
		if !strings.HasSuffix(out.String(), "}\n") {
			t.Errorf("output does not end in a single newline: %q", out.String())
		}
	})

	t.Run("reads the descriptor from stdin", func(t *testing.T) {
		path, _ := writeInspectFixture(t)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		var fromStdin, fromFile bytes.Buffer
		if code := inspectDescriptor(context.Background(), inspectContext(), stdinPath, bytes.NewReader(b), &fromStdin); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if code := inspectDescriptor(context.Background(), inspectContext(), path, nil, &fromFile); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}

		if fromStdin.String() != fromFile.String() {
			t.Errorf("piped descriptor rendered differently\n stdin:\n%s\n file:\n%s", fromStdin.String(), fromFile.String())
		}
	})

	t.Run("renders the same bytes every time", func(t *testing.T) {
		// The output is a thing to diff across runs, so two renderings of one
		// file must not differ.
		path, _ := writeInspectFixture(t)

		var first bytes.Buffer
		if code := inspectDescriptor(context.Background(), inspectContext(), path, nil, &first); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		for i := range 8 {
			var next bytes.Buffer
			if code := inspectDescriptor(context.Background(), inspectContext(), path, nil, &next); code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}
			if next.String() != first.String() {
				t.Fatalf("rendering differs on iteration %d\n got:\n%s\nwant:\n%s", i, next.String(), first.String())
			}
		}
	})

	t.Run("fails on a missing file", func(t *testing.T) {
		var out bytes.Buffer
		path := filepath.Join(t.TempDir(), "absent.binpb")

		if code := inspectDescriptor(context.Background(), inspectContext(), path, nil, &out); code == 0 {
			t.Error("exit code = 0, want non-zero for a missing descriptor")
		}
		if out.Len() != 0 {
			t.Errorf("wrote output for a missing descriptor:\n%s", out.String())
		}
	})

	t.Run("fails on bytes that are not a descriptor", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "junk.binpb")
		if err := os.WriteFile(path, []byte("not a descriptor"), 0o644); err != nil {
			t.Fatal(err)
		}

		var out bytes.Buffer
		if code := inspectDescriptor(context.Background(), inspectContext(), path, nil, &out); code == 0 {
			t.Error("exit code = 0, want non-zero for a file that is not a descriptor")
		}
		if out.Len() != 0 {
			t.Errorf("wrote output for a file that is not a descriptor:\n%s", out.String())
		}
	})

	t.Run("renders a descriptor from an unknown IR version", func(t *testing.T) {
		// The case the subcommand exists for: a descriptor this build would
		// refuse to generate from is still one a person can read.
		desc := newDescriptor(nil, nil)
		desc.Version = proto.Int32(ir.Version + 41)
		b, err := ir.MarshalDescriptor(desc)
		if err != nil {
			t.Fatal(err)
		}

		var out bytes.Buffer
		if code := inspectDescriptor(context.Background(), inspectContext(), stdinPath, bytes.NewReader(b), &out); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if !strings.Contains(out.String(), `"version"`) {
			t.Errorf("rendering does not carry the version:\n%s", out.String())
		}
	})
}

func TestRunInspect(t *testing.T) {
	t.Run("renders the descriptor it is given", func(t *testing.T) {
		path, _ := writeInspectFixture(t)

		if code := runInspect(context.Background(), inspectContext("inspect", path)); code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
	})

	t.Run("rejects an invocation naming no descriptor", func(t *testing.T) {
		if code := runInspect(context.Background(), inspectContext("inspect")); code == 0 {
			t.Error("exit code = 0, want non-zero when no descriptor is named")
		}
	})

	t.Run("rejects an invocation naming more than one descriptor", func(t *testing.T) {
		// Two paths is a mistake with two plausible readings — render both, or
		// render the first — so it fails instead of picking one.
		path, _ := writeInspectFixture(t)

		if code := runInspect(context.Background(), inspectContext("inspect", path, path)); code == 0 {
			t.Error("exit code = 0, want non-zero when two descriptors are named")
		}
	})
}
