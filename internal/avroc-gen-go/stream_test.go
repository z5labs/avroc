// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/z5labs/avroc/internal/avrocpb"

	"google.golang.org/grpc"
)

// captureStream is a server stream that records the GenerateResponse chunks a
// generator sends, for use in tests.
type captureStream struct {
	grpc.ServerStream
	ctx  context.Context
	msgs []*avrocpb.GenerateResponse
}

func (c *captureStream) Send(m *avrocpb.GenerateResponse) error {
	c.msgs = append(c.msgs, m)
	return nil
}

func (c *captureStream) Context() context.Context { return c.ctx }

// genResult mirrors the file paths a generation produced, matching the shape
// the tests previously asserted against.
type genResult struct {
	OutputFiles []string
}

// generateToDir runs svc.Generate against a capture stream, reassembles the
// streamed chunks per path, and writes the files under dir exactly as avroc
// would, returning their full paths in emission order.
func generateToDir(t *testing.T, svc *generatorService, dir string, req *avrocpb.GenerateRequest) (*genResult, error) {
	t.Helper()

	cs := &captureStream{ctx: context.Background()}
	if err := svc.Generate(req, cs); err != nil {
		return nil, err
	}

	contents := make(map[string][]byte)
	var order []string
	done := make(map[string]bool)
	for _, m := range cs.msgs {
		p := m.GetPath()
		// Enforce the streaming contract: last=true terminates a file, and no
		// further chunks may reference that path afterwards.
		if done[p] {
			t.Fatalf("generator sent a chunk for path %q after it was terminated with last=true", p)
		}
		if _, ok := contents[p]; !ok {
			order = append(order, p)
		}
		contents[p] = append(contents[p], m.GetContent()...)
		if m.GetLast() {
			done[p] = true
		}
	}
	for _, p := range order {
		if !done[p] {
			t.Fatalf("generator never terminated path %q with last=true", p)
		}
	}

	res := &genResult{}
	for _, p := range order {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(full, contents[p], 0o644); err != nil {
			return nil, err
		}
		res.OutputFiles = append(res.OutputFiles, full)
	}

	return res, nil
}
