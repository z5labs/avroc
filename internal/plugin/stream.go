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

// fileStream turns the chunk stream some of the generators in this repository
// still emit into the whole files a FileWriter takes.
//
// It exists because avroc no longer consumes those chunks: they used to cross a
// socket and be written by avroc, and under docs/plugin/SPEC.md the generator
// writes its own files. avroc-gen-go no longer needs it — it writes through
// FileWriter directly (#121) — and #122 and #123 do the same for the other two,
// after which #124 deletes the service the type comes from and this file with
// it.
//
// Chunks are accumulated per path and handed over whole once the terminating
// chunk arrives, rather than written through as they come. That gives back the
// bounded peak memory chunking exists for, and it is the right trade here for
// two reasons: every generator in this repository builds a whole file in memory
// and then slices it into chunks, so the bound was never real, and buffering is
// what lets a file be written once and completely, which is the property
// FileWriter is built on. A half-emitted file now reaches the filesystem not at
// all, rather than reaching it and being removed again.
//
// grpc.ServerStream is embedded to satisfy avrocpb.Generator_GenerateServer and
// is deliberately nil: no gRPC call is in flight, nothing here reads it, and a
// method that reached for it would be asking a question this contract has no
// answer to. Context is overridden so the one method a generator might
// plausibly call does not panic.
type fileStream struct {
	grpc.ServerStream

	ctx context.Context
	w   FileWriter

	// open holds the accumulated content of every file whose terminating chunk
	// has not arrived.
	open map[string][]byte
	// finalized names the files already written, so a chunk arriving after a
	// file's terminating one is an error rather than a silent append.
	finalized map[string]struct{}
	// order preserves emission order, so a report about an unterminated file
	// names the first one the generator started rather than whichever the map
	// happens to yield.
	order []string
}

func (s *fileStream) Context() context.Context { return s.ctx }

// Send takes one chunk, writing the file out once its terminating chunk
// arrives.
func (s *fileStream) Send(msg *avrocpb.GenerateResponse) error {
	path := msg.GetPath()
	if path == "" {
		return fmt.Errorf("generator emitted a chunk with no path")
	}
	if _, done := s.finalized[path]; done {
		return fmt.Errorf("generator emitted a chunk for already-completed file %q", path)
	}

	if s.open == nil {
		s.open = make(map[string][]byte)
		s.finalized = make(map[string]struct{})
	}

	if _, started := s.open[path]; !started {
		s.order = append(s.order, path)
	}
	s.open[path] = append(s.open[path], msg.GetContent()...)

	if !msg.GetLast() {
		return nil
	}

	content := s.open[path]
	delete(s.open, path)
	s.finalized[path] = struct{}{}
	return s.w.WriteFile(path, content)
}

// finish reports a generator that stopped with a file still unterminated. A
// half-emitted file is a bug in the generator, and reporting it is what stops
// the missing bytes turning up later as a compile error in the user's tree;
// nothing was written for it, so there is nothing to clean up.
func (s *fileStream) finish() error {
	if len(s.open) == 0 {
		return nil
	}

	var first string
	for _, path := range s.order {
		if _, open := s.open[path]; open {
			first = path
			break
		}
	}

	return fmt.Errorf("generator finished with %d unterminated file(s), the first being %q", len(s.open), first)
}
