// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package ir

import (
	"bytes"
	"testing"

	"github.com/z5labs/avroc/avrocpb"

	"google.golang.org/protobuf/proto"
)

func descriptorFixture() *avrocpb.GenerateRequest {
	return &avrocpb.GenerateRequest{
		Version: proto.Int32(Version),
		Options: []*avrocpb.Option{
			{Name: proto.String("module"), Value: proto.String("example.com/m")},
			{Name: proto.String("package"), Value: proto.String("models")},
		},
		Schemas: []*avrocpb.Schema{
			{
				Namespace: proto.String("com.example"),
				Type: &avrocpb.Type{
					Type: &avrocpb.Type_Record{
						Record: &avrocpb.Record{
							Name:     proto.String("User"),
							FullName: proto.String("com.example.User"),
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
			},
		},
	}
}

func TestMarshalDescriptor(t *testing.T) {
	t.Run("round-trips through the binary wire encoding", func(t *testing.T) {
		desc := descriptorFixture()

		b, err := MarshalDescriptor(desc)
		if err != nil {
			t.Fatal(err)
		}

		var got avrocpb.GenerateRequest
		if err := proto.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(&got, desc) {
			t.Errorf("decoded descriptor differs from the one encoded\n got: %v\nwant: %v", &got, desc)
		}
	})

	t.Run("encodes equal messages to equal bytes", func(t *testing.T) {
		// Two independently built values that are proto.Equal must encode
		// identically. That is the property the descriptor file rests on: the
		// bytes are a function of the message, not of the process that built it.
		var want []byte
		for i := range 64 {
			got, err := MarshalDescriptor(descriptorFixture())
			if err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				want = got
				continue
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("encoding differs on iteration %d", i)
			}
		}
	})

	t.Run("distinguishes descriptors that differ", func(t *testing.T) {
		// Determinism is worth nothing if the encoding is insensitive to the
		// inputs, so check the other direction too: a changed option changes the
		// bytes, which is what makes a byte comparison a meaningful check.
		a, err := MarshalDescriptor(descriptorFixture())
		if err != nil {
			t.Fatal(err)
		}

		changed := descriptorFixture()
		changed.Options[1].Value = proto.String("types")
		b, err := MarshalDescriptor(changed)
		if err != nil {
			t.Fatal(err)
		}

		if bytes.Equal(a, b) {
			t.Error("descriptors differing in an option encoded to the same bytes")
		}
	})
}
