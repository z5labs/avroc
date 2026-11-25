// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// SizeAvro and AppendAvro (#249) against every committed example the Go
// generator writes, and the benchmarks that measure them.
//
// It is one package of tests over all four generated packages, rather than a
// file beside each, so that the invariants are written once for them, in
// invariants_test.go. That file is internal/avroc-gen-go/testdata/
// directencoding's under another package clause, which is how the descriptors
// only a hand-built IR reaches are held to exactly the same invariants. It
// lives in a directory of its own, holding no generated code, so avroc has
// nothing here to prune.
package directencoding_test

import (
	"bytes"
	"strings"
	"testing"

	avro "github.com/z5labs/avro-go"
	gen "github.com/z5labs/avroc/example/gen"
	"github.com/z5labs/avroc/example/nested"
	"github.com/z5labs/avroc/example/stream"
	"github.com/z5labs/avroc/example/unions"
)

// testRecord is schema.avdl's record with the proposal's sizes (#244): a
// 200-byte string and a 16-byte fixed, beside an enum and a fixed union
// branch.
func testRecord() *gen.TestRecord {
	return &gen.TestRecord{
		Name:         strings.Repeat("v", 200),
		Kind:         gen.KindBAZ,
		Hash:         gen.MD5{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
		NullableHash: gen.TestRecordNullableHashUnionMD5{Value: gen.MD5{15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1, 0}},
	}
}

func nestedOuter() *nested.Outer {
	return &nested.Outer{
		Grid: [][]int32{{1, 2}, nil, {3, -300000}},
		Rows: []map[string]int32{{"a": 1, "b": 2, "c": 3}, {}, {"only": -1}},
		MaybeGrid: [][]nested.OuterMaybeGridUnion{
			{nested.OuterMaybeGridUnionNull{}, nested.OuterMaybeGridUnionInt{Value: 2}},
			nil,
		},
	}
}

func unionsOuter() *unions.Outer {
	return &unions.Outer{
		Id:    -1 << 40,
		Tone:  unions.OuterToneUnionTone{Value: unions.ToneHIGH},
		Inner: unions.OuterInnerUnionInner{Value: unions.Inner{X: 42}},
	}
}

func streamEvent() *stream.Event {
	return &stream.Event{Id: 1 << 50, Value: 3.25, Ok: true}
}

func TestEveryExampleTypeIsSizedAndAppendedAsItIsMarshaled(t *testing.T) {
	testCases := []struct {
		name  string
		v     directEncoder
		fresh func() avro.BinaryUnmarshaler
	}{
		{name: "gen.TestRecord", v: testRecord(), fresh: func() avro.BinaryUnmarshaler { return &gen.TestRecord{} }},
		{name: "gen.TestRecord with a null branch", v: &gen.TestRecord{NullableHash: gen.TestRecordNullableHashUnionNull{}},
			fresh: func() avro.BinaryUnmarshaler { return &gen.TestRecord{} }},
		{name: "gen.Kind", v: gen.KindBAR, fresh: func() avro.BinaryUnmarshaler { return new(gen.Kind) }},
		{name: "gen.MD5", v: gen.MD5{0xff}, fresh: func() avro.BinaryUnmarshaler { return &gen.MD5{} }},
		{name: "gen.TestRecordNullableHashUnionNull", v: gen.TestRecordNullableHashUnionNull{}},
		{name: "gen.TestRecordNullableHashUnionMD5", v: gen.TestRecordNullableHashUnionMD5{Value: gen.MD5{1, 2}}},

		{name: "stream.Event", v: streamEvent(), fresh: func() avro.BinaryUnmarshaler { return &stream.Event{} }},
		{name: "a zero stream.Event", v: &stream.Event{}, fresh: func() avro.BinaryUnmarshaler { return &stream.Event{} }},

		{name: "unions.Outer", v: unionsOuter(), fresh: func() avro.BinaryUnmarshaler { return &unions.Outer{} }},
		{name: "unions.Outer with null branches", v: &unions.Outer{Tone: unions.OuterToneUnionNull{}, Inner: unions.OuterInnerUnionNull{}},
			fresh: func() avro.BinaryUnmarshaler { return &unions.Outer{} }},
		{name: "unions.Tone", v: unions.ToneLOW, fresh: func() avro.BinaryUnmarshaler { return new(unions.Tone) }},
		{name: "unions.Inner", v: &unions.Inner{X: -7}, fresh: func() avro.BinaryUnmarshaler { return &unions.Inner{} }},
		{name: "unions.OuterToneUnionTone", v: unions.OuterToneUnionTone{Value: unions.ToneHIGH}},
		{name: "unions.OuterInnerUnionInner", v: unions.OuterInnerUnionInner{Value: unions.Inner{X: 1}}},

		{name: "nested.Outer", v: nestedOuter(), fresh: func() avro.BinaryUnmarshaler { return &nested.Outer{} }},
		{name: "an empty nested.Outer", v: &nested.Outer{}, fresh: func() avro.BinaryUnmarshaler { return &nested.Outer{} }},
		{name: "nested.OuterMaybeGridUnionInt", v: nested.OuterMaybeGridUnionInt{Value: -64}},
		{name: "nested.OuterMaybeGridUnionNull", v: nested.OuterMaybeGridUnionNull{}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			checkDirectEncoding(t, tc.v, tc.fresh)
		})
	}
}

// TestTheUnionInterfacesCarryBothMethods is the union half of #249: a field
// holding a union is sized and appended through its interface, so each
// interface has to declare both.
func TestTheUnionInterfacesCarryBothMethods(t *testing.T) {
	var (
		_ directEncoder = gen.TestRecordNullableHashUnion(nil)
		_ directEncoder = unions.OuterToneUnion(nil)
		_ directEncoder = unions.OuterInnerUnion(nil)
		_ directEncoder = nested.OuterMaybeGridUnion(nil)
	)
}

// benchmarkDirect reports SizeAvro, AppendAvro into a buffer with room, and
// MarshalAvroBinary into a reused bytes.Buffer for comparison.
func benchmarkDirect(b *testing.B, v directEncoder) {
	b.Run("SizeAvro", func(b *testing.B) {
		b.ReportAllocs()
		var n int
		for b.Loop() {
			n = v.SizeAvro()
		}
		_ = n
	})
	b.Run("AppendAvro", func(b *testing.B) {
		dst := make([]byte, 0, v.SizeAvro())
		b.SetBytes(int64(cap(dst)))
		b.ReportAllocs()
		for b.Loop() {
			dst = v.AppendAvro(dst[:0])
		}
	})
	b.Run("MarshalAvroBinary", func(b *testing.B) {
		var buf bytes.Buffer
		buf.Grow(v.SizeAvro())
		w := avro.NewBinaryWriter(&buf)
		b.SetBytes(int64(v.SizeAvro()))
		b.ReportAllocs()
		for b.Loop() {
			buf.Reset()
			if err := v.MarshalAvroBinary(w); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkTestRecord(b *testing.B)  { benchmarkDirect(b, testRecord()) }
func BenchmarkStreamEvent(b *testing.B) { benchmarkDirect(b, streamEvent()) }
func BenchmarkUnionsOuter(b *testing.B) { benchmarkDirect(b, unionsOuter()) }
func BenchmarkNestedOuter(b *testing.B) { benchmarkDirect(b, nestedOuter()) }
