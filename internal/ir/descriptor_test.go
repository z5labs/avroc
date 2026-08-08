// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package ir

import (
	"bytes"
	"fmt"
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

func TestUnmarshalDescriptor(t *testing.T) {
	t.Run("decodes what MarshalDescriptor encoded", func(t *testing.T) {
		desc := descriptorFixture()

		b, err := MarshalDescriptor(desc)
		if err != nil {
			t.Fatal(err)
		}

		got, err := UnmarshalDescriptor(b)
		if err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(got, desc) {
			t.Errorf("decoded descriptor differs from the one encoded\n got: %v\nwant: %v", got, desc)
		}
	})

	t.Run("rejects bytes that are not a descriptor", func(t *testing.T) {
		// A truncated or unrelated file must fail here rather than decode into
		// an empty descriptor: a caller shown "version 0, no schemas" would go
		// looking for a producer bug that is really a wrong path.
		_, err := UnmarshalDescriptor([]byte("this is not protobuf at all"))
		if err == nil {
			t.Error("expected an error decoding non-descriptor bytes")
		}
	})
}

func TestMarshalDescriptorJSON(t *testing.T) {
	t.Run("renders the descriptor with protobuf field names", func(t *testing.T) {
		// A golden rendering, because every part of it is a thing a reader
		// depends on: snake_case names matching proto/ and docs/ir/SPEC.md, the
		// enum spelled rather than numbered, two-space indentation, and
		// protobuf's field-number ordering (full_name after fields).
		const want = `{
  "version": 1,
  "options": [
    {
      "name": "module",
      "value": "example.com/m"
    },
    {
      "name": "package",
      "value": "models"
    }
  ],
  "schemas": [
    {
      "namespace": "com.example",
      "type": {
        "record": {
          "name": "User",
          "fields": [
            {
              "name": "name",
              "type": {
                "reference": {
                  "name": "string",
                  "kind": "TYPE_REF_KIND_PRIMITIVE"
                }
              }
            }
          ],
          "full_name": "com.example.User"
        }
      }
    }
  ]
}`

		got, err := MarshalDescriptorJSON(descriptorFixture())
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("rendering differs\n got:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("renders equal descriptors identically", func(t *testing.T) {
		var want []byte
		for i := range 64 {
			got, err := MarshalDescriptorJSON(descriptorFixture())
			if err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				want = got
				continue
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("rendering differs on iteration %d", i)
			}
		}
	})

	t.Run("distinguishes descriptors that differ", func(t *testing.T) {
		a, err := MarshalDescriptorJSON(descriptorFixture())
		if err != nil {
			t.Fatal(err)
		}

		changed := descriptorFixture()
		changed.Options[1].Value = proto.String("types")
		b, err := MarshalDescriptorJSON(changed)
		if err != nil {
			t.Fatal(err)
		}

		if bytes.Equal(a, b) {
			t.Error("descriptors differing in an option rendered identically")
		}
	})

	t.Run("renders a descriptor carrying an unknown version", func(t *testing.T) {
		// Rendering is looking, not consuming. A descriptor from a contract
		// this build does not know is the one somebody most needs to read, and
		// the version it claims is right there in the output.
		desc := descriptorFixture()
		desc.Version = proto.Int32(Version + 41)

		got, err := MarshalDescriptorJSON(desc)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(got, fmt.Appendf(nil, `"version": %d`, Version+41)) {
			t.Errorf("rendering does not report the descriptor's own version:\n%s", got)
		}
	})
}

func TestIndentJSON(t *testing.T) {
	t.Run("is insensitive to the whitespace it is handed", func(t *testing.T) {
		// This is the property MarshalDescriptorJSON rests on, and it cannot be
		// observed through protojson from inside one test binary: protojson's
		// extra spaces are chosen by a hash of the running binary, so they are
		// fixed for this process and differ in the next build. The two inputs
		// below are the two shapes it emits — a space after a comma and a
		// second space after a "key": — written out by hand so that a
		// regression here fails every time rather than in half of all builds.
		compact := []byte(`{"version":1,"options":[{"name":"module","value":"a, b"}]}`)
		spaced := []byte(`{"version":  1, "options": [{"name":  "module", "value":  "a, b"}]}`)

		fromCompact, err := indentJSON(compact)
		if err != nil {
			t.Fatal(err)
		}
		fromSpaced, err := indentJSON(spaced)
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(fromCompact, fromSpaced) {
			t.Errorf("indentation depends on the input's whitespace\n from compact:\n%s\n from spaced:\n%s", fromCompact, fromSpaced)
		}
		// A string's own spacing is content, not whitespace between tokens, and
		// must survive untouched.
		if !bytes.Contains(fromCompact, []byte(`"a, b"`)) {
			t.Errorf("string content was reformatted:\n%s", fromCompact)
		}
	})

	t.Run("reports invalid JSON", func(t *testing.T) {
		if _, err := indentJSON([]byte(`{"unterminated":`)); err == nil {
			t.Error("expected an error indenting invalid JSON")
		}
	})
}
