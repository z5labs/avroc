// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package ir

import (
	"strings"
	"testing"

	"github.com/z5labs/avroc/avrocpb"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func TestVersionIsPositive(t *testing.T) {
	// Zero is CheckVersion's "carries no version", so a released contract can
	// never be numbered 0, and a monotonic version never runs backwards.
	if Version < 1 {
		t.Errorf("IR version must be at least 1, got %d", Version)
	}
}

func TestCheckVersionAcceptsTheCurrentVersion(t *testing.T) {
	if err := CheckVersion(Version); err != nil {
		t.Errorf("CheckVersion rejected the current version %d: %v", Version, err)
	}
}

func TestCheckVersionRejectsUnknownVersions(t *testing.T) {
	testCases := []struct {
		name    string
		version int32
		want    []string
	}{
		{
			name:    "unset",
			version: 0,
			// Printing "IR version 0" would send a user looking for a version
			// nothing ever emitted; the diagnostic names the real problem.
			want: []string{"carries no IR version", "understands IR version 1"},
		},
		{
			name:    "newer than this consumer",
			version: Version + 1,
			want:    []string{"is IR version 2", "understands IR version 1"},
		},
		{
			// There is no "older than this consumer" case to write while the
			// contract is on its first version: one below it is zero, which is
			// the unset case above. A negative stands in for the malformed
			// version a non-conforming producer could still put on the wire.
			name:    "negative",
			version: -1,
			want:    []string{"is IR version -1", "understands IR version 1"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckVersion(tc.version)
			if err == nil {
				t.Fatalf("CheckVersion(%d) accepted a version this consumer does not know", tc.version)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("diagnostic %q does not name %q", err.Error(), want)
				}
			}
		})
	}
}

// TestUnknownFieldsAreIgnored is the other half of docs/ir/SPEC.md's asymmetry
// from TestValidateRejectsUnknownSortOrder: a field a consumer has never seen is
// information it did not need, and decoding a descriptor carrying one must
// succeed with every known field intact.
//
// It appends a field number no descriptor defines to the encoded bytes rather
// than asserting that protobuf behaves this way in the abstract, because what
// the spec promises is about the descriptor as it arrives on the wire — which is
// what makes an additive change free and lets the version stay put across most
// edits.
func TestUnknownFieldsAreIgnored(t *testing.T) {
	want := &avrocpb.GenerateRequest{
		Version: proto.Int32(Version),
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("gen")}},
		Schemas: []*avrocpb.Schema{{
			Namespace: proto.String("com.example"),
			Type: &avrocpb.Type{Type: &avrocpb.Type_Record{Record: &avrocpb.Record{
				Name:      proto.String("Thing"),
				Namespace: proto.String("com.example"),
				FullName:  proto.String("com.example.Thing"),
			}}},
		}},
	}

	encoded, err := proto.Marshal(want)
	if err != nil {
		t.Fatalf("failed to marshal descriptor: %v", err)
	}

	// Field 4096, varint 7: a scalar a future IR might add and this build has
	// never heard of.
	encoded = protowire.AppendTag(encoded, 4096, protowire.VarintType)
	encoded = protowire.AppendVarint(encoded, 7)
	// Field 4097, length-delimited: a message-shaped addition, which fails
	// differently from a scalar if it is not tolerated.
	encoded = protowire.AppendTag(encoded, 4097, protowire.BytesType)
	encoded = protowire.AppendBytes(encoded, []byte("a future field's value"))

	var got avrocpb.GenerateRequest
	if err := proto.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("decoding a descriptor carrying unknown fields failed: %v", err)
	}

	if err := CheckVersion(got.GetVersion()); err != nil {
		t.Errorf("the version did not survive the unknown fields: %v", err)
	}
	if err := Validate(got.GetSchemas()[0]); err != nil {
		t.Errorf("the schema did not survive the unknown fields: %v", err)
	}

	got.ProtoReflect().SetUnknown(nil)
	if !proto.Equal(want, &got) {
		t.Errorf("known fields did not survive decoding\n got: %v\nwant: %v", &got, want)
	}
}
