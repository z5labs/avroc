// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package plugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/z5labs/avroc/avrocpb"

	"google.golang.org/grpc"
)

// fileStream turns the chunk stream the generators in this repository still
// emit into files beneath --out.
//
// It exists because avroc no longer consumes those chunks: they used to cross a
// socket and be written by avroc, and under docs/plugin/SPEC.md the generator
// writes its own files. Rather than rewrite three generators' emission paths in
// the same change that moves the invocation — that is #121, #122 and #123, each
// of which has more to do than this — the chunks are reassembled by the one
// adapter here.
//
// Each chunk is written straight through to an open file rather than
// accumulated in memory, so peak memory does not scale with the size of a
// generated file. Chunking exists precisely to keep it bounded, and buffering
// would give that back while keeping the cost.
//
// grpc.ServerStream is embedded to satisfy avrocpb.Generator_GenerateServer and
// is deliberately nil: no gRPC call is in flight, nothing here reads it, and a
// method that reached for it would be asking a question this contract has no
// answer to. Context is overridden so the one method a generator might
// plausibly call does not panic.
type fileStream struct {
	grpc.ServerStream

	ctx context.Context
	inv *Invocation

	// open holds the files whose terminating chunk has not arrived.
	open map[string]*os.File
	// finalized names the files already closed, so a chunk arriving after a
	// file's terminating one is an error rather than a silent append.
	finalized map[string]struct{}
	// order preserves emission order, so a report about an unterminated file
	// names the first one the generator started rather than whichever the map
	// happens to yield.
	order   []string
	written []string
}

func (s *fileStream) Context() context.Context { return s.ctx }

// Send writes one chunk, closing the file once its terminating chunk arrives.
func (s *fileStream) Send(msg *avrocpb.GenerateResponse) error {
	path := msg.GetPath()
	if path == "" {
		return fmt.Errorf("generator emitted a chunk with no path")
	}
	if _, done := s.finalized[path]; done {
		return fmt.Errorf("generator emitted a chunk for already-completed file %q", path)
	}

	if s.open == nil {
		s.open = make(map[string]*os.File)
		s.finalized = make(map[string]struct{})
	}

	f, ok := s.open[path]
	if !ok {
		dst, err := OutputPath(s.inv.Out, path)
		if err != nil {
			return fmt.Errorf("refusing to write %q: %w", path, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("failed to create output directory for %q: %w", dst, err)
		}
		f, err = os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("failed to create %q: %w", dst, err)
		}
		s.open[path] = f
		s.order = append(s.order, path)
	}

	if _, err := f.Write(msg.GetContent()); err != nil {
		return fmt.Errorf("failed to write %q: %w", f.Name(), err)
	}

	if !msg.GetLast() {
		return nil
	}

	name := f.Name()
	delete(s.open, path)
	s.finalized[path] = struct{}{}
	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close %q: %w", name, err)
	}
	s.written = append(s.written, path)
	return nil
}

// discard closes and removes every file whose terminating chunk never arrived,
// so a half-written file is not left behind for a compiler to find later. It is
// idempotent, and is deferred by Main so that it also covers a generator that
// returned an error partway through.
func (s *fileStream) discard() {
	for path, f := range s.open {
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		delete(s.open, path)
	}
}

// finish reports a generator that stopped with a file still unterminated, and
// discards it. A half-emitted file is a bug in the generator, and reporting it
// is what stops the missing bytes turning up later as a compile error in the
// user's tree.
func (s *fileStream) finish() error {
	if len(s.open) == 0 {
		return nil
	}

	n := len(s.open)
	var first string
	for _, path := range s.order {
		if _, open := s.open[path]; open {
			first = path
			break
		}
	}
	s.discard()

	return fmt.Errorf("generator finished with %d unterminated file(s), the first being %q", n, first)
}
