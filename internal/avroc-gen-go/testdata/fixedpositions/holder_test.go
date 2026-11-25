// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// This file is not part of this repository's build: Go ignores testdata. It is
// copied beside the code avroc-gen-go generates for fixedPositionsSchemas, in a
// module of its own, by TestEveryFixedPositionCompilesAndRoundTrips, and it is
// what that test runs there.
package fixedpositions

import (
	"bytes"
	"reflect"
	"testing"

	avro "github.com/z5labs/avro-go"
)

type avroValue interface {
	MarshalAvroBinary(w *avro.BinaryWriter) error
	UnmarshalAvroBinary(r *avro.BinaryReader) error
}

func roundTrip(t *testing.T, in, out avroValue) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := in.MarshalAvroBinary(avro.NewBinaryWriter(&buf)); err != nil {
		t.Fatalf("MarshalAvroBinary: %v", err)
	}
	if err := out.UnmarshalAvroBinary(avro.NewBinaryReader(bytes.NewReader(buf.Bytes()))); err != nil {
		t.Fatalf("UnmarshalAvroBinary: %v", err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Errorf("round trip gave %+v, want %+v", out, in)
	}
	return buf.Bytes()
}

// TestAFixedInEveryPositionRoundTrips holds a distinct fixed value in every
// position, so a value written from the wrong place, or not at all, decodes
// into a different Holder. Each map holds one key, so the encoding does not
// depend on iteration order, and each empty inner array is nil because a
// decoded array is only ever appended to.
func TestAFixedInEveryPositionRoundTrips(t *testing.T) {
	in := &Holder{
		Hash:       Hash{1, 1, 1, 1},
		Again:      Hash{2, 2, 2, 2},
		Hashes:     []Hash{{3, 3, 3, 3}, {4, 4, 4, 4}},
		Grid:       [][]Hash{{{5, 5, 5, 5}}, nil, {{6, 6, 6, 6}, {7, 7, 7, 7}}},
		ListsByKey: map[string][]Hash{"k": {{8, 8, 8, 8}, {9, 9, 9, 9}}},
		ByKey:      map[string]Hash{"b": {10, 10, 10, 10}},
		Maybe:      HolderMaybeUnionHash{Value: Hash{11, 11, 11, 11}},
		Listed:     HolderListedUnionArray{Value: []Hash{{12, 12, 12, 12}, {13, 13, 13, 13}}},
		Tone:       ToneHIGH,
		Inner:      Inner{I: 14},
		InnerAgain: Inner{I: 15},
		Inners:     []Inner{{I: 16}, {I: 17}},
	}
	roundTrip(t, in, &Holder{})
	checkDirectEncoding(t, in, func() avro.BinaryUnmarshaler { return &Holder{} })
}

// TestAFixedInEveryPositionIsSizedAndAppended holds every type the two
// schemas generate to #249's invariants on its own, beside the Holder above
// that reaches all of them: the fixed's own methods, the union branches whose
// fixed is their Value, and the record another schema's fixed is written into.
func TestAFixedInEveryPositionIsSizedAndAppended(t *testing.T) {
	testCases := []struct {
		name  string
		v     directEncoder
		fresh func() avro.BinaryUnmarshaler
	}{
		{name: "Hash", v: Hash{1, 2, 3, 4}, fresh: func() avro.BinaryUnmarshaler { return &Hash{} }},
		{name: "Tone", v: ToneHIGH, fresh: func() avro.BinaryUnmarshaler { return new(Tone) }},
		{name: "Inner", v: &Inner{I: -300}, fresh: func() avro.BinaryUnmarshaler { return &Inner{} }},
		{name: "Later", v: &Later{Hash: Hash{0xde, 0xad, 0xbe, 0xef}}, fresh: func() avro.BinaryUnmarshaler { return &Later{} }},
		{name: "a null branch", v: HolderMaybeUnionNull{}},
		{name: "a fixed branch", v: HolderMaybeUnionHash{Value: Hash{5, 6, 7, 8}}},
		{name: "an array of fixed branch", v: HolderListedUnionArray{Value: []Hash{{1, 1, 1, 1}, {2, 2, 2, 2}}}},
		{name: "a Holder holding maps of several entries", v: &Holder{
			ListsByKey: map[string][]Hash{"a": {{1, 1, 1, 1}}, "b": nil, "c": {{2, 2, 2, 2}, {3, 3, 3, 3}}},
			ByKey:      map[string]Hash{"x": {4, 4, 4, 4}, "y": {5, 5, 5, 5}},
			Maybe:      HolderMaybeUnionNull{},
			Listed:     HolderListedUnionNull{},
		}, fresh: func() avro.BinaryUnmarshaler { return &Holder{} }},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			checkDirectEncoding(t, tc.v, tc.fresh)
		})
	}
}

// TestAFixedAnotherSchemaDefinesIsWrittenAsAvroEncodesIt pins the bytes of
// the record whose fixed is defined by the other schema in the descriptor.
func TestAFixedAnotherSchemaDefinesIsWrittenAsAvroEncodesIt(t *testing.T) {
	got := roundTrip(t, &Later{Hash: Hash{0xde, 0xad, 0xbe, 0xef}}, &Later{})
	if appended := (&Later{Hash: Hash{0xde, 0xad, 0xbe, 0xef}}).AppendAvro(nil); !bytes.Equal(appended, got) {
		t.Errorf("AppendAvro(nil) = %x, MarshalAvroBinary wrote %x", appended, got)
	}
	if want := []byte{0xde, 0xad, 0xbe, 0xef}; !bytes.Equal(got, want) {
		t.Errorf("encoded %x, want %x", got, want)
	}
}
