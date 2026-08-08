// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgenpcf

import (
	"bytes"
	"os"
	"testing"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/plugin"

	"google.golang.org/protobuf/proto"
)

// TestDeclaredOptionsAreTheOnesGenerateReads holds the capability declaration
// against the generator that has to honour it.
//
// The declaration this generator writes is the empty vocabulary, so the
// assertion is the mirror image of the one avroc-gen-go makes: every declared
// key must change the output there, and here there is no key that may. An
// option Generate quietly read would be configuration avroc rejects at the
// handshake and the generator honours anyway — reachable only by hand, and
// therefore untested surface. It would also be worse here than elsewhere: these
// bytes are fingerprinted, so an option that moved one would produce a canonical
// form no other Avro implementation agrees with.
func TestDeclaredOptionsAreTheOnesGenerateReads(t *testing.T) {
	if got := declaredOptions(); len(got) != 0 {
		t.Fatalf("declaredOptions() is %v; this generator declares none, so a key here needs wiring into Generate or removing", got)
	}

	baseline := generateWith(t, nil)

	// Not a vocabulary this generator could grow into: the point is that an
	// option of any spelling leaves the bytes alone.
	opts := []*avrocpb.Option{
		{Name: proto.String("encoding"), Value: proto.String("single_object")},
		{Name: proto.String("package_name"), Value: proto.String("gen")},
		{Name: proto.String("indent"), Value: proto.String("\t")},
	}
	if got := generateWith(t, opts); !bytes.Equal(got, baseline) {
		t.Errorf("options changed the generated output, so this generator reads a key it declares no support for:\n--- without ---\n%s\n--- with ---\n%s", baseline, got)
	}
}

// TestDeclaredVocabularyIsPresentAndEmpty pins the distinction avroc acts on:
// an empty options member says "I accept none" and lets avroc reject a stray
// manifest option before anything runs, where an absent one says "pass them
// through and I will decide". A nil slice marshals to JSON null, which is
// neither.
func TestDeclaredVocabularyIsPresentAndEmpty(t *testing.T) {
	info := plugin.NewInfo("pcf", declaredOptions()...)

	if info.Options == nil {
		t.Fatal("the declaration's options member is absent; this generator accepts none and has to say so")
	}
	if len(info.Options) != 0 {
		t.Errorf("the declaration names options %v, want none", info.Options)
	}
}

// generateWith runs one generation over a fixed schema and returns the single
// file it produced, so that two option sets can be compared as bytes.
func generateWith(t *testing.T, opts []*avrocpb.Option) []byte {
	t.Helper()

	res, err := generateToDir(t, t.TempDir(), &avrocpb.GenerateRequest{
		Options: opts,
		Schemas: []*avrocpb.Schema{versionTestSchema()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.OutputFiles) != 1 {
		t.Fatalf("generator produced %d files, want 1", len(res.OutputFiles))
	}

	b, err := os.ReadFile(res.OutputFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	return b
}
