// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/ir"
	"github.com/z5labs/avroc/internal/plugin"

	"google.golang.org/protobuf/proto"
)

// genResult mirrors the file paths a generation produced.
type genResult struct {
	OutputFiles []string
}

// generateToDir runs one generation into dir and returns the full path of every
// file it wrote, in the order it wrote them.
//
// The writer is plugin.OutputDir, the one plugin.Main hands this generator in a
// real invocation, so what a test asserts about is what a run produces: the same
// path checking, the same directory creation, the same refusal to write a path
// twice. A recording stand-in would have to re-implement all three to be worth
// asserting against, and would then be the thing under test.
func generateToDir(t *testing.T, dir string, req *avrocpb.GenerateRequest) (*genResult, error) {
	t.Helper()

	// avroc stamps every descriptor it emits with the IR version, so a request
	// built in a test without one is a test artefact rather than the case under
	// test. Defaulting it here keeps the version rule tested where it is the
	// subject — see TestGenerateRejectsUnknownIRVersion — instead of restated at
	// every call site. A test that sets a version of its own keeps it.
	//
	// The field, not GetVersion: an explicit zero is a reserved value a test may
	// legitimately want to send, and GetVersion cannot tell it from a field that
	// was never set. Defaulting that away would rewrite the case under test into
	// a passing one.
	if req.Version == nil {
		req = proto.CloneOf(req)
		req.Version = proto.Int32(ir.Version)
	}

	w := plugin.NewOutputDir(dir)
	if err := Generate(t.Context(), req, w); err != nil {
		return nil, err
	}

	res := &genResult{}
	for _, p := range w.Written() {
		res.OutputFiles = append(res.OutputFiles, filepath.Join(dir, filepath.FromSlash(p)))
	}
	return res, nil
}

// failingWriter refuses every file.
type failingWriter struct{}

func (failingWriter) WriteFile(string, []byte) error { return errWriteRefused }

var errWriteRefused = errors.New("refused")

// TestGenerateReportsAFailedWrite is the other half of plugin.Main's check on
// OutputDir.Err: the generator has to hand a failed write back rather than
// carry on, because a zero exit is the whole of the success signal and avroc
// would adopt the output directory with the file missing from it.
func TestGenerateReportsAFailedWrite(t *testing.T) {
	err := Generate(t.Context(), &avrocpb.GenerateRequest{
		Version: proto.Int32(ir.Version),
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("gen")}},
		Schemas: []*avrocpb.Schema{versionTestSchema()},
	}, failingWriter{})

	if !errors.Is(err, errWriteRefused) {
		t.Errorf("Generate returned %v, want the writer's failure", err)
	}
}
