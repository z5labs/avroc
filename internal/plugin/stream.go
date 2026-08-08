// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package plugin

import (
	"context"
	"fmt"

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
// of which has more to do than this — the chunks are reassembled in-process by
// the one adapter here.
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

	// pending holds the bytes of files whose terminating chunk has not arrived.
	pending map[string][]byte
	// finalized names the files already written, so a chunk arriving after a
	// file's terminating one is an error rather than a silent overwrite.
	finalized map[string]struct{}
	// order preserves emission order so the written paths read the way the
	// generator produced them rather than the way a map iterates.
	order   []string
	written []string
}

func (s *fileStream) Context() context.Context { return s.ctx }

// Send accumulates one chunk, writing the file out once its terminating chunk
// arrives.
func (s *fileStream) Send(msg *avrocpb.GenerateResponse) error {
	path := msg.GetPath()
	if path == "" {
		return fmt.Errorf("generator emitted a chunk with no path")
	}
	if _, done := s.finalized[path]; done {
		return fmt.Errorf("generator emitted a chunk for already-completed file %q", path)
	}

	if s.pending == nil {
		s.pending = make(map[string][]byte)
		s.finalized = make(map[string]struct{})
	}
	if _, open := s.pending[path]; !open {
		s.order = append(s.order, path)
	}
	s.pending[path] = append(s.pending[path], msg.GetContent()...)

	if !msg.GetLast() {
		return nil
	}

	content := s.pending[path]
	delete(s.pending, path)
	if err := s.inv.WriteFile(path, content); err != nil {
		return err
	}
	s.finalized[path] = struct{}{}
	s.written = append(s.written, path)
	return nil
}

// finish reports a generator that stopped with a file still unterminated. A
// half-emitted file is a bug in the generator, and reporting it is what stops
// the missing bytes turning up later as a compile error in the user's tree.
func (s *fileStream) finish() error {
	if len(s.pending) == 0 {
		return nil
	}
	for _, path := range s.order {
		if _, open := s.pending[path]; open {
			return fmt.Errorf("generator finished with %d unterminated file(s), the first being %q", len(s.pending), path)
		}
	}
	return fmt.Errorf("generator finished with %d unterminated file(s)", len(s.pending))
}
