// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/z5labs/avroc/avrocpb"

	"google.golang.org/protobuf/proto"
)

func chunk(path, content string, last bool) *avrocpb.GenerateResponse {
	return &avrocpb.GenerateResponse{
		Path:    proto.String(path),
		Content: []byte(content),
		Last:    proto.Bool(last),
	}
}

func TestFileStream(t *testing.T) {
	t.Run("reassembles a file from its chunks", func(t *testing.T) {
		out := t.TempDir()
		s := &fileStream{ctx: t.Context(), w: NewOutputDir(out)}

		for _, msg := range []*avrocpb.GenerateResponse{
			chunk("pkg/user.go", "package pkg\n", false),
			chunk("pkg/user.go", "// trailer\n", true),
		} {
			if err := s.Send(msg); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.finish(); err != nil {
			t.Fatal(err)
		}

		got, err := os.ReadFile(filepath.Join(out, "pkg", "user.go"))
		if err != nil {
			t.Fatal(err)
		}
		if want := "package pkg\n// trailer\n"; string(got) != want {
			t.Errorf("content = %q, want %q", string(got), want)
		}
	})

	t.Run("interleaves two files", func(t *testing.T) {
		out := t.TempDir()
		s := &fileStream{ctx: t.Context(), w: NewOutputDir(out)}

		for _, msg := range []*avrocpb.GenerateResponse{
			chunk("a.go", "a1", false),
			chunk("b.go", "b1", false),
			chunk("a.go", "a2", true),
			chunk("b.go", "b2", true),
		} {
			if err := s.Send(msg); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.finish(); err != nil {
			t.Fatal(err)
		}

		for name, want := range map[string]string{"a.go": "a1a2", "b.go": "b1b2"} {
			got, err := os.ReadFile(filepath.Join(out, name))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != want {
				t.Errorf("%s = %q, want %q", name, string(got), want)
			}
		}
	})

	t.Run("an empty file is one terminating chunk", func(t *testing.T) {
		out := t.TempDir()
		s := &fileStream{ctx: t.Context(), w: NewOutputDir(out)}

		if err := s.Send(chunk("empty.go", "", true)); err != nil {
			t.Fatal(err)
		}
		if err := s.finish(); err != nil {
			t.Fatal(err)
		}

		info, err := os.Stat(filepath.Join(out, "empty.go"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() != 0 {
			t.Errorf("size = %d, want 0", info.Size())
		}
	})

	t.Run("rejects a chunk with no path", func(t *testing.T) {
		s := &fileStream{ctx: t.Context(), w: NewOutputDir(t.TempDir())}
		if err := s.Send(chunk("", "package pkg\n", true)); err == nil {
			t.Error("Send accepted a chunk with no path")
		}
	})

	t.Run("rejects a chunk after a file was terminated", func(t *testing.T) {
		s := &fileStream{ctx: t.Context(), w: NewOutputDir(t.TempDir())}
		if err := s.Send(chunk("user.go", "package pkg\n", true)); err != nil {
			t.Fatal(err)
		}
		if err := s.Send(chunk("user.go", "// extra\n", true)); err == nil {
			t.Error("Send accepted a chunk for an already-completed file")
		}
	})

	t.Run("rejects a path outside the output directory", func(t *testing.T) {
		out := t.TempDir()
		s := &fileStream{ctx: t.Context(), w: NewOutputDir(out)}

		if err := s.Send(chunk("../escape.go", "package pkg\n", true)); err == nil {
			t.Error("Send accepted a path outside the output directory")
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(out), "escape.go")); !os.IsNotExist(err) {
			t.Error("a file escaped the output directory")
		}
	})

	t.Run("reports a file left unterminated, and never writes it", func(t *testing.T) {
		out := t.TempDir()
		s := &fileStream{ctx: t.Context(), w: NewOutputDir(out)}

		if err := s.Send(chunk("user.go", "package pkg\n", false)); err != nil {
			t.Fatal(err)
		}
		if err := s.Send(chunk("done.go", "package pkg\n", true)); err != nil {
			t.Fatal(err)
		}
		if err := s.finish(); err == nil {
			t.Error("finish accepted a stream that left a file unterminated")
		}

		// Chunks are held until the terminating one arrives, so a half-emitted
		// file never reaches the filesystem: there is no partial source file for
		// a compiler to find and no cleanup to get right.
		if _, err := os.Stat(filepath.Join(out, "user.go")); !os.IsNotExist(err) {
			t.Errorf("an unterminated file reached the output directory: %v", err)
		}
		// A file that was terminated is output, and an unterminated one
		// elsewhere in the stream does not take it back.
		if _, err := os.Stat(filepath.Join(out, "done.go")); err != nil {
			t.Errorf("a completed file was not written: %v", err)
		}
	})

	t.Run("names the first unterminated file", func(t *testing.T) {
		s := &fileStream{ctx: t.Context(), w: NewOutputDir(t.TempDir())}

		for _, msg := range []*avrocpb.GenerateResponse{
			chunk("first.go", "a", false),
			chunk("second.go", "b", false),
		} {
			if err := s.Send(msg); err != nil {
				t.Fatal(err)
			}
		}

		err := s.finish()
		if err == nil {
			t.Fatal("finish accepted a stream that left two files unterminated")
		}
		// Emission order, not map order: the report has to be the same on every
		// run or it is not a report a person can act on.
		if want := `"first.go"`; !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err.Error(), want)
		}
	})

	t.Run("hands the generator the invocation's context", func(t *testing.T) {
		// The only reason grpc.ServerStream's embedded nil does not panic on the
		// one method a generator might plausibly call.
		ctx := t.Context()
		s := &fileStream{ctx: ctx, w: NewOutputDir(t.TempDir())}
		if s.Context() != ctx {
			t.Error("Context() did not return the invocation's context")
		}
	})

	t.Run("nothing sent is not a failure", func(t *testing.T) {
		s := &fileStream{ctx: t.Context(), w: NewOutputDir(t.TempDir())}
		if err := s.finish(); err != nil {
			t.Errorf("finish reported a failure for a generator that emitted nothing: %v", err)
		}
	})
}
