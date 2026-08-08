// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"strings"
	"testing"

	"github.com/z5labs/avroc/internal/avrocpb"
	"github.com/z5labs/avroc/internal/ir"

	"google.golang.org/protobuf/proto"
)

// TestGenerateRejectsUnknownIRVersion is docs/ir/SPEC.md's version rule seen
// from the consumer's side: a descriptor written against a contract this
// generator does not know fails the invocation with a diagnostic naming both
// versions, rather than being read for the parts that still look familiar.
func TestGenerateRejectsUnknownIRVersion(t *testing.T) {
	testCases := []struct {
		name    string
		version *int32
	}{
		{name: "no version at all", version: nil},
		{name: "explicitly zero", version: proto.Int32(0)},
		{name: "newer than this generator", version: proto.Int32(ir.Version + 1)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &generatorService{}

			// Deliberately not routed through generateToDir: that helper stamps
			// a valid version onto a request that carries none, which is the
			// one thing this test needs left alone.
			cs := &captureStream{ctx: t.Context()}
			err := svc.Generate(&avrocpb.GenerateRequest{
				Version: tc.version,
				Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("gen")}},
				Schemas: []*avrocpb.Schema{versionTestSchema()},
			}, cs)

			if err == nil {
				t.Fatal("Generate accepted a descriptor whose IR version it does not know")
			}
			if !strings.Contains(err.Error(), "IR version") {
				t.Errorf("diagnostic %q does not name the IR version", err.Error())
			}
			if len(cs.msgs) != 0 {
				t.Errorf("generator emitted %d chunks from a descriptor it refused", len(cs.msgs))
			}
		})
	}
}

// TestGenerateAcceptsTheCurrentIRVersion is the guard on the test above: a check
// that refused everything would pass it.
func TestGenerateAcceptsTheCurrentIRVersion(t *testing.T) {
	svc := &generatorService{}

	_, err := generateToDir(t, svc, t.TempDir(), &avrocpb.GenerateRequest{
		Version: proto.Int32(ir.Version),
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("gen")}},
		Schemas: []*avrocpb.Schema{versionTestSchema()},
	})
	if err != nil {
		t.Fatalf("Generate rejected the current IR version %d: %v", ir.Version, err)
	}
}

// TestGenerateToDirPreservesAnExplicitVersion pins generateToDir's one piece of
// judgment: it defaults a version onto a request that carries none, and must
// leave alone one a test set deliberately. Zero is the case that distinguishes
// the two, because it is a reserved value a test may legitimately want to send
// and is indistinguishable from an unset field through the getter — defaulting
// it away would quietly rewrite a failing case into a passing one.
func TestGenerateToDirPreservesAnExplicitVersion(t *testing.T) {
	svc := &generatorService{}

	_, err := generateToDir(t, svc, t.TempDir(), &avrocpb.GenerateRequest{
		Version: proto.Int32(0),
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("gen")}},
		Schemas: []*avrocpb.Schema{versionTestSchema()},
	})
	if err == nil {
		t.Fatal("generateToDir replaced an explicitly reserved version with a valid one")
	}
}

// versionTestSchema is a schema this generator handles without complaint, so a
// failure above is the version check and nothing else.
func versionTestSchema() *avrocpb.Schema {
	return &avrocpb.Schema{
		Namespace: proto.String("com.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name:      proto.String("Person"),
					Namespace: proto.String("com.example"),
					FullName:  proto.String("com.example.Person"),
					Fields: []*avrocpb.Field{
						{
							Name: proto.String("name"),
							Type: &avrocpb.Type{
								Type: &avrocpb.Type_Reference{
									Reference: &avrocpb.Reference{
										Name: proto.String("string"),
										Kind: avrocpb.TypeRefKind_TYPE_REF_KIND_PRIMITIVE.Enum(),
									},
								},
							},
						},
					},
				},
			},
		},
	}
}
