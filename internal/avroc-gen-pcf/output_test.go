// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgenpcf

import (
	"bytes"
	"errors"
	"os"
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
// twice — and, for this generator above all, the same bytes, because a recording
// stand-in could not show that the write itself normalises nothing.
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
	if err := Generate(req, w); err != nil {
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
	err := Generate(&avrocpb.GenerateRequest{
		Version: proto.Int32(ir.Version),
		Schemas: []*avrocpb.Schema{versionTestSchema()},
	}, failingWriter{})

	if !errors.Is(err, errWriteRefused) {
		t.Errorf("Generate returned %v, want the writer's failure", err)
	}
}

// TestGenerateWritesOneFilePerSchema pins what the streaming emission used to
// make implicit: one schema in the descriptor is one whole file beneath --out,
// named for the schema and written exactly once.
func TestGenerateWritesOneFilePerSchema(t *testing.T) {
	dir := t.TempDir()

	res, err := generateToDir(t, dir, &avrocpb.GenerateRequest{
		Schemas: []*avrocpb.Schema{resolvedTestRecord(), determinismSecondSchema()},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	want := []string{filepath.Join(dir, "test_record.avsc"), filepath.Join(dir, "simple.avsc")}
	if len(res.OutputFiles) != len(want) {
		t.Fatalf("generation produced %v, want %v", res.OutputFiles, want)
	}
	for i, got := range res.OutputFiles {
		if got != want[i] {
			t.Errorf("output file %d is %q, want %q", i, got, want[i])
		}
	}
}

// TestGeneratedFileIsExactlyTheCanonicalForm is this generator's reason for
// existing, asserted where it can actually fail: on disk, after the write.
//
// A Parsing Canonical Form is not source that a tool may tidy — it is the input
// to a CRC-64-AVRO fingerprint, so a byte added anywhere on the path from
// ir.CanonicalJSON to the file is a different fingerprint and a reader that can
// no longer identify a writer's messages. The comparison is therefore against
// ir.CanonicalJSON's own bytes rather than against a literal copied into this
// test: those are the bytes avroc-gen-go fingerprints, and the assertion is that
// the published file is them and not a rendering of them.
//
// internal/ir.TestGeneratorsAgreeOnCanonicalBytes closes the loop across the two
// generators' committed output; this one is the same rule one process wide, so a
// normalising write is caught here rather than as a mismatched fingerprint in
// example/.
func TestGeneratedFileIsExactlyTheCanonicalForm(t *testing.T) {
	for _, schema := range []*avrocpb.Schema{resolvedTestRecord(), determinismSecondSchema()} {
		want, err := ir.CanonicalJSON(schema)
		if err != nil {
			t.Fatalf("ir.CanonicalJSON failed: %v", err)
		}

		dir := t.TempDir()
		res, err := generateToDir(t, dir, &avrocpb.GenerateRequest{
			Schemas: []*avrocpb.Schema{schema},
		})
		if err != nil {
			t.Fatalf("Generate failed: %v", err)
		}
		if len(res.OutputFiles) != 1 {
			t.Fatalf("generator produced %d files, want 1", len(res.OutputFiles))
		}

		got, err := os.ReadFile(res.OutputFiles[0])
		if err != nil {
			t.Fatalf("failed to read the generated file: %v", err)
		}

		if !bytes.Equal(got, want) {
			t.Errorf("the file written for %s is not the canonical form:\n--- ir.CanonicalJSON ---\n%s\n--- file ---\n%s",
				schema.GetType().GetRecord().GetFullName(), want, got)
		}
	}
}

// TestGeneratedFileHasNoTrailingNewline states the one difference from every
// other generator here outright, because it is the difference a reviewer or a
// formatter is most likely to erase.
//
// avroc-gen-json appends a newline to its .avsc files and is right to: they are
// a rendering for a person. These are not. Avro's Parsing Canonical Form is
// defined without one, and the file is hashed rather than read, so a trailing
// newline would silently change every fingerprint computed over it — including
// by another implementation, which is where the disagreement would surface.
func TestGeneratedFileHasNoTrailingNewline(t *testing.T) {
	dir := t.TempDir()

	res, err := generateToDir(t, dir, &avrocpb.GenerateRequest{
		Schemas: []*avrocpb.Schema{resolvedTestRecord()},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if len(res.OutputFiles) != 1 {
		t.Fatalf("generator produced %d files, want 1", len(res.OutputFiles))
	}

	content, err := os.ReadFile(res.OutputFiles[0])
	if err != nil {
		t.Fatalf("failed to read the generated file: %v", err)
	}
	if len(content) == 0 {
		t.Fatal("the generated file is empty: the case under test is empty")
	}
	if last := content[len(content)-1]; last != '}' {
		t.Errorf("the canonical form ends with %q, want the closing brace and nothing after it: %s", last, content)
	}
}
