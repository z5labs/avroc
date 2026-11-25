// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// SizeAvro and AppendAvro (#249) against every committed example the Go
// generator writes, and the benchmarks that measure them.
//
// It is one package of tests over all four generated packages, rather than a
// file beside each, so that the invariants are written once here. Its twin is
// internal/avroc-gen-go/testdata/directencoding, which holds the descriptors
// only a hand-built IR reaches to the same invariants; the two are separate
// because that one is copied into modules of its own that cannot import this
// one. It lives in a directory of its own, holding no generated code, so avroc
// has nothing here to prune.
package directencoding_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	avro "github.com/z5labs/avro-go"
	gen "github.com/z5labs/avroc/example/gen"
	"github.com/z5labs/avroc/example/nested"
	"github.com/z5labs/avroc/example/stream"
	"github.com/z5labs/avroc/example/unions"
)

type directEncoder interface {
	MarshalAvroBinary(w *avro.BinaryWriter) error
	SizeAvro() int
	AppendAvro(dst []byte) []byte
}

// checkDirectEncoding holds v to #249's invariants: SizeAvro() equals
// len(AppendAvro(nil)); AppendAvro(nil) is MarshalAvroBinary's bytes, or for a
// value holding a map of more than one entry bytes that decode to the same
// value; AppendAvro(dst) leaves dst[:len(dst)] as it was and extends it by
// exactly SizeAvro() bytes; and neither allocates when dst has the room.
//
// fresh returns a zero value to decode into, and is nil for a union branch,
// which has no UnmarshalAvroBinary of its own.
func checkDirectEncoding(t *testing.T, v directEncoder, fresh func() avro.BinaryUnmarshaler) {
	t.Helper()

	var buf bytes.Buffer
	if err := v.MarshalAvroBinary(avro.NewBinaryWriter(&buf)); err != nil {
		t.Fatalf("MarshalAvroBinary: %v", err)
	}
	marshaled := buf.Bytes()

	appended := v.AppendAvro(nil)
	size := v.SizeAvro()
	if size != len(appended) {
		t.Errorf("SizeAvro() = %d, but AppendAvro(nil) appended %d bytes", size, len(appended))
	}

	unordered := holdsAMapOfMoreThanOneEntry(reflect.ValueOf(v))
	if !bytes.Equal(appended, marshaled) {
		switch {
		case !unordered:
			t.Errorf("AppendAvro(nil) = % x, MarshalAvroBinary wrote % x", appended, marshaled)
		case fresh == nil:
			t.Errorf("AppendAvro(nil) and MarshalAvroBinary disagree, and there is nothing to decode them into to compare")
		default:
			fromAppend, fromMarshal := fresh(), fresh()
			decodeAll(t, appended, fromAppend)
			decodeAll(t, marshaled, fromMarshal)
			if !reflect.DeepEqual(fromAppend, fromMarshal) {
				t.Errorf("AppendAvro(nil) decodes to %+v, MarshalAvroBinary's bytes to %+v", fromAppend, fromMarshal)
			}
		}
	}

	prefix := []byte("prefix")
	for _, spare := range []int{0, size} {
		dst := make([]byte, len(prefix), len(prefix)+spare)
		copy(dst, prefix)

		got := v.AppendAvro(dst)
		if !bytes.Equal(dst, prefix) {
			t.Errorf("with %d bytes spare, AppendAvro changed dst[:len(dst)] to % x", spare, dst)
		}
		if !bytes.Equal(got[:len(prefix)], prefix) {
			t.Errorf("with %d bytes spare, AppendAvro returned % x, which does not begin with dst", spare, got)
		}
		if len(got) != len(prefix)+size {
			t.Errorf("with %d bytes spare, AppendAvro extended dst by %d bytes, want SizeAvro() = %d", spare, len(got)-len(prefix), size)
		}
		if !unordered && !bytes.Equal(got[len(prefix):], appended) {
			t.Errorf("with %d bytes spare, AppendAvro appended % x, want % x", spare, got[len(prefix):], appended)
		}
		if spare > 0 && &got[0] != &dst[0] {
			t.Errorf("with %d bytes spare, AppendAvro reallocated rather than appending in place", spare)
		}
	}

	var n int
	if allocs := testing.AllocsPerRun(100, func() { n = v.SizeAvro() }); allocs != 0 {
		t.Errorf("SizeAvro made %v allocations per run, want 0", allocs)
	}
	_ = n

	room := make([]byte, 0, size)
	if allocs := testing.AllocsPerRun(100, func() { room = v.AppendAvro(room[:0]) }); allocs != 0 {
		t.Errorf("AppendAvro into a dst with room made %v allocations per run, want 0", allocs)
	}
}

func decodeAll(t *testing.T, b []byte, into avro.BinaryUnmarshaler) {
	t.Helper()
	r := bytes.NewReader(b)
	if err := into.UnmarshalAvroBinary(avro.NewBinaryReader(r)); err != nil {
		t.Fatalf("UnmarshalAvroBinary: %v", err)
	}
	if r.Len() != 0 {
		t.Errorf("%d byte(s) left unread", r.Len())
	}
}

// holdsAMapOfMoreThanOneEntry reports whether a map of more than one entry is
// reachable from v, which is the one case two encodings of a value may order
// differently.
func holdsAMapOfMoreThanOneEntry(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		return !v.IsNil() && holdsAMapOfMoreThanOneEntry(v.Elem())
	case reflect.Struct:
		for i := range v.NumField() {
			if holdsAMapOfMoreThanOneEntry(v.Field(i)) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			if holdsAMapOfMoreThanOneEntry(v.Index(i)) {
				return true
			}
		}
	case reflect.Map:
		if v.Len() > 1 {
			return true
		}
		for it := v.MapRange(); it.Next(); {
			if holdsAMapOfMoreThanOneEntry(it.Value()) {
				return true
			}
		}
	}
	return false
}

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
