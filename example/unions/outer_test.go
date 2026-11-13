// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// Round trips of the types avroc-gen-go emits for a schema whose named types
// are first referenced from a union branch (#235), against the committed
// generated code rather than against a string of it.
//
// That the package compiles at all is the first half of the assertion: before
// #235, Tone and Inner were defined only inside union branches in the
// descriptor, the generator never walked those branches, and outer.go named two
// types it never declared. It lives beside the generated code for the reason
// stream/event_stream_test.go does: avroc never removes a file it has no record
// of generating.
package unions

import (
	"bytes"
	"reflect"
	"testing"

	avro "github.com/z5labs/avro-go"
)

// TestOuterRoundTripsEveryBranchOfEveryUnion encodes an Outer holding each
// combination of the two unions' branches, compares the bytes against the Avro
// binary encoding written out by hand, and decodes them back.
//
// The bytes are checked as well as the round trip because a marshal and an
// unmarshal that were wrong in the same way would agree with each other.
func TestOuterRoundTripsEveryBranchOfEveryUnion(t *testing.T) {
	tones := []struct {
		name  string
		value OuterToneUnion
		wire  []byte
	}{
		// A union is its branch index as a zig-zag long, then the branch's
		// value: null is the index alone, and an enum is its symbol's index as
		// a zig-zag int.
		{name: "tone null", value: OuterToneUnionNull{}, wire: []byte{0x00}},
		{name: "tone LOW", value: OuterToneUnionTone{Value: ToneLOW}, wire: []byte{0x02, 0x00}},
		{name: "tone HIGH", value: OuterToneUnionTone{Value: ToneHIGH}, wire: []byte{0x02, 0x02}},
	}
	inners := []struct {
		name  string
		value OuterInnerUnion
		wire  []byte
	}{
		{name: "inner null", value: OuterInnerUnionNull{}, wire: []byte{0x00}},
		{name: "inner record", value: OuterInnerUnionInner{Value: Inner{X: -3}}, wire: []byte{0x02, 0x05}},
	}

	for _, tone := range tones {
		for _, inner := range inners {
			t.Run(tone.name+"/"+inner.name, func(t *testing.T) {
				in := &Outer{Id: 42, Tone: tone.value, Inner: inner.value}

				var buf bytes.Buffer
				if err := in.MarshalAvroBinary(avro.NewBinaryWriter(&buf)); err != nil {
					t.Fatalf("MarshalAvroBinary: %v", err)
				}

				// 42 as a zig-zag long is 84, which fits in one byte.
				want := append([]byte{0x54}, tone.wire...)
				want = append(want, inner.wire...)
				if got := buf.Bytes(); !bytes.Equal(got, want) {
					t.Fatalf("encoded % x, want % x", got, want)
				}

				var out Outer
				r := bytes.NewReader(buf.Bytes())
				if err := out.UnmarshalAvroBinary(avro.NewBinaryReader(r)); err != nil {
					t.Fatalf("UnmarshalAvroBinary: %v", err)
				}
				if r.Len() != 0 {
					t.Errorf("%d byte(s) left unread", r.Len())
				}
				if !reflect.DeepEqual(&out, in) {
					t.Errorf("decoded %+v, want %+v", out, *in)
				}
			})
		}
	}
}

// TestOuterRejectsAnUnknownBranch is the failure path of the decoder the
// generator now emits for a union whose branch it defines: an index past the
// last branch is an error rather than a zero value.
func TestOuterRejectsAnUnknownBranch(t *testing.T) {
	// id 42, then branch index 2 of a two-branch union.
	r := avro.NewBinaryReader(bytes.NewReader([]byte{0x54, 0x04}))

	var out Outer
	if err := out.UnmarshalAvroBinary(r); err == nil {
		t.Fatalf("decoded %+v from a union index past the last branch, want an error", out)
	}
}
