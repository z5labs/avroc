// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"bytes"
	"os"
	"testing"

	"github.com/z5labs/avroc/avrocpb"

	"google.golang.org/protobuf/proto"
)

// TestDeclaredOptionsAreTheOnesGenerateReads holds the capability declaration
// against the generator that has to honour it.
//
// docs/plugin/SPEC.md lets avroc reject a manifest option the plugin did not
// declare, which is only useful while the declared set is the set Generate
// actually reads. The failure this catches is the quiet one in the other
// direction: a key listed here that nothing consumes is a line in a checked-in
// manifest that reads as configuration, passes avroc's check, and does nothing —
// and the user finds out by noticing that the output never changed.
//
// Each declared key is driven through a real generation and asserted to change
// the bytes. That is the only evidence available from outside that a key is read
// at all.
func TestDeclaredOptionsAreTheOnesGenerateReads(t *testing.T) {
	baseline := generateWith(t, options("package_name", "gen"))

	changed := map[string][]*avrocpb.Option{
		"package_name": options("package_name", "other"),
		"encoding":     options("package_name", "gen", "encoding", "single_object"),
	}

	for _, key := range declaredOptions() {
		t.Run(key, func(t *testing.T) {
			opts, ok := changed[key]
			if !ok {
				t.Fatalf("option %q is declared but this test has no case exercising it; either wire it into Generate or stop declaring it", key)
			}
			if got := generateWith(t, opts); bytes.Equal(got, baseline) {
				t.Errorf("setting %q did not change the generated output, so the declaration promises avroc something Generate does not read", key)
			}
		})
	}
}

func options(pairs ...string) []*avrocpb.Option {
	opts := make([]*avrocpb.Option, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		opts = append(opts, &avrocpb.Option{
			Name:  proto.String(pairs[i]),
			Value: proto.String(pairs[i+1]),
		})
	}
	return opts
}

// generateWith runs one generation over a fixed schema and returns the single
// file it produced, so that two option sets can be compared as bytes.
func generateWith(t *testing.T, opts []*avrocpb.Option) []byte {
	t.Helper()

	res, err := generateToDir(t, &generatorService{}, t.TempDir(), &avrocpb.GenerateRequest{
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
