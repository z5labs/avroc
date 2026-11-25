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
	"errors"
	"fmt"
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

// readerModes are the two ways an avro.BinaryReader reads: from an io.Reader,
// and over a byte slice it decodes in place. ReadIndex checks the range in
// both, and they are implemented apart, so every decode below runs in both.
func readerModes() []struct {
	name string
	new  func([]byte) *avro.BinaryReader
} {
	return []struct {
		name string
		new  func([]byte) *avro.BinaryReader
	}{
		{name: "io.Reader", new: func(b []byte) *avro.BinaryReader { return avro.NewBinaryReader(bytes.NewReader(b)) }},
		{name: "bytes", new: avro.NewBytesReader},
	}
}

// TestOuterDecodesEveryInRangeIndexInEitherReaderMode is the other half of
// the rejection below: every index the schema does define decodes exactly as
// it did before ReadIndex, whichever reader mode reads it (#248).
func TestOuterDecodesEveryInRangeIndexInEitherReaderMode(t *testing.T) {
	cases := []struct {
		name string
		wire []byte
		want Outer
	}{
		{
			name: "both null",
			wire: []byte{0x54, 0x00, 0x00},
			want: Outer{Id: 42, Tone: OuterToneUnionNull{}, Inner: OuterInnerUnionNull{}},
		},
		{
			name: "tone LOW",
			wire: []byte{0x54, 0x02, 0x00, 0x00},
			want: Outer{Id: 42, Tone: OuterToneUnionTone{Value: ToneLOW}, Inner: OuterInnerUnionNull{}},
		},
		{
			name: "tone HIGH and an inner record",
			wire: []byte{0x54, 0x02, 0x02, 0x02, 0x05},
			want: Outer{Id: 42, Tone: OuterToneUnionTone{Value: ToneHIGH}, Inner: OuterInnerUnionInner{Value: Inner{X: -3}}},
		},
	}

	for _, mode := range readerModes() {
		for _, tc := range cases {
			t.Run(mode.name+"/"+tc.name, func(t *testing.T) {
				var out Outer
				if err := out.UnmarshalAvroBinary(mode.new(tc.wire)); err != nil {
					t.Fatalf("UnmarshalAvroBinary: %v", err)
				}
				if !reflect.DeepEqual(out, tc.want) {
					t.Errorf("decoded %+v, want %+v", out, tc.want)
				}
			})
		}
	}
}

// TestOuterRejectsAnOutOfRangeIndex is the failure path of ReadIndex in the
// generated decoder (#248): an enum ordinal or a union branch index that the
// schema does not define, negative or past the end, fails the decode with
// avro.ErrInvalidIndex in either reader mode, rather than decoding into a
// value no symbol names or failing with an error nothing can match.
func TestOuterRejectsAnOutOfRangeIndex(t *testing.T) {
	// Every case starts with id 42, which is 0x54 as a zig-zag long. An
	// index is a zig-zag varint: -1 is 0x01, 2 is 0x04, and 1000 is d0 0f.
	cases := []struct {
		name string
		wire []byte
	}{
		{name: "tone branch -1", wire: []byte{0x54, 0x01}},
		{name: "tone branch 2", wire: []byte{0x54, 0x04}},
		{name: "tone branch 1000", wire: []byte{0x54, 0xd0, 0x0f}},
		{name: "Tone ordinal -1", wire: []byte{0x54, 0x02, 0x01}},
		{name: "Tone ordinal 2", wire: []byte{0x54, 0x02, 0x04}},
		{name: "Tone ordinal 1000", wire: []byte{0x54, 0x02, 0xd0, 0x0f}},
		{name: "inner branch -1", wire: []byte{0x54, 0x00, 0x01}},
		{name: "inner branch 2", wire: []byte{0x54, 0x00, 0x04}},
	}

	for _, mode := range readerModes() {
		for _, tc := range cases {
			t.Run(mode.name+"/"+tc.name, func(t *testing.T) {
				var out Outer
				err := out.UnmarshalAvroBinary(mode.new(tc.wire))
				if !errors.Is(err, avro.ErrInvalidIndex) {
					t.Fatalf("UnmarshalAvroBinary(% x) = %v, want an error matching avro.ErrInvalidIndex", tc.wire, err)
				}
			})
		}
	}
}

// TestToneRejectsAnOutOfRangeOrdinal decodes the enum on its own, so the
// check is seen to be the enum's and not only the union holding it.
func TestToneRejectsAnOutOfRangeOrdinal(t *testing.T) {
	for _, mode := range readerModes() {
		for _, wire := range [][]byte{{0x01}, {0x04}} {
			t.Run(fmt.Sprintf("%s/% x", mode.name, wire), func(t *testing.T) {
				var tone Tone
				err := tone.UnmarshalAvroBinary(mode.new(wire))
				if !errors.Is(err, avro.ErrInvalidIndex) {
					t.Fatalf("UnmarshalAvroBinary(% x) = %v (decoded %d), want an error matching avro.ErrInvalidIndex", wire, err, tone)
				}
			})
		}
	}
}
