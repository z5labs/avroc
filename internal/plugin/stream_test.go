// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package plugin

import (
	"os"
	"path/filepath"
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
		s := &fileStream{ctx: t.Context(), inv: &Invocation{Out: out}}

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
		s := &fileStream{ctx: t.Context(), inv: &Invocation{Out: out}}

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
		s := &fileStream{ctx: t.Context(), inv: &Invocation{Out: out}}

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
		s := &fileStream{ctx: t.Context(), inv: &Invocation{Out: t.TempDir()}}
		if err := s.Send(chunk("", "package pkg\n", true)); err == nil {
			t.Error("Send accepted a chunk with no path")
		}
	})

	t.Run("rejects a chunk after a file was terminated", func(t *testing.T) {
		s := &fileStream{ctx: t.Context(), inv: &Invocation{Out: t.TempDir()}}
		if err := s.Send(chunk("user.go", "package pkg\n", true)); err != nil {
			t.Fatal(err)
		}
		if err := s.Send(chunk("user.go", "// extra\n", true)); err == nil {
			t.Error("Send accepted a chunk for an already-completed file")
		}
	})

	t.Run("rejects a path outside the output directory", func(t *testing.T) {
		out := t.TempDir()
		s := &fileStream{ctx: t.Context(), inv: &Invocation{Out: out}}

		if err := s.Send(chunk("../escape.go", "package pkg\n", true)); err == nil {
			t.Error("Send accepted a path outside the output directory")
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(out), "escape.go")); !os.IsNotExist(err) {
			t.Error("a file escaped the output directory")
		}
	})

	t.Run("reports a file left unterminated", func(t *testing.T) {
		s := &fileStream{ctx: t.Context(), inv: &Invocation{Out: t.TempDir()}}
		if err := s.Send(chunk("user.go", "package pkg\n", false)); err != nil {
			t.Fatal(err)
		}
		if err := s.finish(); err == nil {
			t.Error("finish accepted a stream that left a file unterminated")
		}
	})

	t.Run("nothing sent is not a failure", func(t *testing.T) {
		s := &fileStream{ctx: t.Context(), inv: &Invocation{Out: t.TempDir()}}
		if err := s.finish(); err != nil {
			t.Errorf("finish reported a failure for a generator that emitted nothing: %v", err)
		}
	})
}
