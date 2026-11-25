// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// This file is not part of this repository's build: Go ignores testdata. It is
// copied beside the code avroc-gen-go generates for viewSampleSchemas under
// compare=true and views=true, by TestViewsAgreeWithUnmarshalOnEveryEncoding.
//
// SampleView, WholeView and KeyedView are checked against UnmarshalAvroBinary
// on random valid encodings — Decode and every accessor — then at every
// truncation, on corrupt input, for every panic the views promise, against
// Compare<Record>Avro, for their allocations, and by a fuzz test whose seeds
// run with every go test.
package views

import (
	"bytes"
	"errors"
	"io"
	"math"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	avro "github.com/z5labs/avro-go"
)

func pick[T any](r *rand.Rand, from []T) T { return from[r.IntN(len(from))] }

// Small domains holding the edges of every encoding: the one-byte varint's
// ends and the first values past them, a length past the one-byte case, NaN
// and both zeros, an ordinal past 63.
var (
	ints    = []int64{0, 1, -1, 63, -64, 64, -65, 1000, math.MaxInt32, math.MinInt32}
	longs   = append(append([]int64(nil), ints...), math.MaxInt64, math.MinInt64)
	floats  = []float64{0, math.Copysign(0, -1), 1, -1.5, math.NaN(), math.Inf(1)}
	payload = []string{"", "a", "ab", "\xff", "\x00", strings.Repeat("a", 63), strings.Repeat("a", 64), strings.Repeat("b", 300)}
	digests = []Digest{{}, {0, 0, 0, 1}, {0xff, 0xff, 0xff, 0xff}}
	bigs    = []Big{0, 1, 63, 64, 69}
)

func randomSample(r *rand.Rand) Sample {
	return Sample{
		Flag:        r.IntN(2) == 0,
		Ratio:       float32(pick(r, floats)),
		Score:       pick(r, floats),
		Digest:      pick(r, digests),
		Pair:        Pair{On: r.IntN(2) == 0, Tag: Tag{byte(r.IntN(256)), byte(r.IntN(256))}},
		Count:       int32(pick(r, ints)),
		Total:       pick(r, longs),
		Blob:        []byte(pick(r, payload)),
		Name:        pick(r, payload),
		Color:       Color(r.IntN(3)),
		Big:         pick(r, bigs),
		Inner:       Inner{Id: pick(r, longs), Label: pick(r, payload), Data: []byte(pick(r, payload))},
		DigestAgain: pick(r, digests),
		FlagAgain:   r.IntN(2) == 0,
		RatioAgain:  float32(pick(r, floats)),
		ScoreAgain:  pick(r, floats),
		PairAgain:   Pair{On: r.IntN(2) == 0, Tag: Tag{byte(r.IntN(256))}},
		Leaf:        Leaf{N: int32(pick(r, ints)), Text: pick(r, payload)},
	}
}

func samples() []Sample {
	r := rand.New(rand.NewPCG(251, 244))
	out := make([]Sample, 200)
	for i := range out {
		out[i] = randomSample(r)
	}
	return out
}

func encode(x *Sample) []byte { return x.AppendAvro(nil) }

func unmarshal(t *testing.T, b []byte) Sample {
	t.Helper()
	var x Sample
	if err := x.UnmarshalAvroBinary(avro.NewBytesReader(b)); err != nil {
		t.Fatalf("UnmarshalAvroBinary: %v", err)
	}
	return x
}

// sameFloat is equality that a NaN satisfies with itself.
func sameFloat(x, y float64) bool { return math.Float64bits(x) == math.Float64bits(y) }

// within reports whether s is a slice of b — the zero-copy promise — with a
// capacity equal to its length.
func within(s, b []byte) bool {
	if cap(s) != len(s) {
		return false
	}
	if len(s) == 0 {
		return true
	}
	for i := range b {
		if &b[i] == &s[0] {
			return i+len(s) <= len(b)
		}
	}
	return false
}

// checkSampleView holds every accessor of v, nested views included, to x.
func checkSampleView(t *testing.T, v *SampleView, x *Sample, enc []byte) {
	t.Helper()
	fail := func(field string, got, want any) {
		t.Helper()
		t.Fatalf("%s = %v, UnmarshalAvroBinary says %v", field, got, want)
	}
	if v.Flag() != x.Flag {
		fail("Flag", v.Flag(), x.Flag)
	}
	if !sameFloat(float64(v.Ratio()), float64(x.Ratio)) {
		fail("Ratio", v.Ratio(), x.Ratio)
	}
	if !sameFloat(v.Score(), x.Score) {
		fail("Score", v.Score(), x.Score)
	}
	if v.Digest() != x.Digest || !bytes.Equal(v.DigestBytes(), x.Digest[:]) || !within(v.DigestBytes(), enc) {
		fail("Digest", v.Digest(), x.Digest)
	}
	if v.Pair().On() != x.Pair.On || v.Pair().Tag() != x.Pair.Tag || !within(v.Pair().TagBytes(), enc) {
		fail("Pair", *v.Pair(), x.Pair)
	}
	if v.Count() != x.Count {
		fail("Count", v.Count(), x.Count)
	}
	if v.Total() != x.Total {
		fail("Total", v.Total(), x.Total)
	}
	if !bytes.Equal(v.Blob(), x.Blob) || !within(v.Blob(), enc) {
		fail("Blob", v.Blob(), x.Blob)
	}
	if v.Name() != x.Name || string(v.NameBytes()) != x.Name || !within(v.NameBytes(), enc) {
		fail("Name", v.Name(), x.Name)
	}
	if v.Color() != x.Color {
		fail("Color", v.Color(), x.Color)
	}
	if v.Big() != x.Big {
		fail("Big", v.Big(), x.Big)
	}
	in := v.Inner()
	if in.Id() != x.Inner.Id || in.Label() != x.Inner.Label || !bytes.Equal(in.Data(), x.Inner.Data) || !within(in.Data(), enc) || in.DataLen() != len(x.Inner.Data) {
		fail("Inner", in, x.Inner)
	}
	if !in.Complete() || in.Len() != x.Inner.SizeAvro() {
		t.Fatalf("Inner: Complete %v, Len %d, want true, %d", in.Complete(), in.Len(), x.Inner.SizeAvro())
	}
	if v.DigestAgain() != x.DigestAgain || !within(v.DigestAgainBytes(), enc) {
		fail("DigestAgain", v.DigestAgain(), x.DigestAgain)
	}
	if v.FlagAgain() != x.FlagAgain {
		fail("FlagAgain", v.FlagAgain(), x.FlagAgain)
	}
	if !sameFloat(float64(v.RatioAgain()), float64(x.RatioAgain)) {
		fail("RatioAgain", v.RatioAgain(), x.RatioAgain)
	}
	if !sameFloat(v.ScoreAgain(), x.ScoreAgain) {
		fail("ScoreAgain", v.ScoreAgain(), x.ScoreAgain)
	}
	if v.PairAgain().On() != x.PairAgain.On || v.PairAgain().Tag() != x.PairAgain.Tag {
		fail("PairAgain", *v.PairAgain(), x.PairAgain)
	}
	leaf := v.Leaf()
	if leaf.N() != x.Leaf.N || leaf.Text() != x.Leaf.Text || !within(leaf.TextBytes(), enc) || leaf.TextLen() != len(x.Leaf.Text) {
		fail("Leaf", leaf, x.Leaf)
	}
}

// TestDecodeAndEveryAccessorAgreeWithUnmarshal is the reference: on every
// valid encoding, a whole buffer parses, is Complete, has the encoding's
// length, and Decode and each accessor give what UnmarshalAvroBinary gives.
func TestDecodeAndEveryAccessorAgreeWithUnmarshal(t *testing.T) {
	for i, s := range samples() {
		enc := encode(&s)
		want := unmarshal(t, enc)

		var v SampleView
		if err := v.Parse(enc); err != nil {
			t.Fatalf("sample %d: Parse: %v", i, err)
		}
		if !v.Complete() || v.Len() != len(enc) || v.PrefixLen() != len(enc)-len(s.Leaf.Text) || v.Need() != v.PrefixLen() {
			t.Fatalf("sample %d: Complete %v, Len %d, PrefixLen %d, Need %d over %d bytes", i, v.Complete(), v.Len(), v.PrefixLen(), v.Need(), len(enc))
		}
		if !bytes.Equal(v.Bytes(), enc) || !within(v.Bytes(), enc) {
			t.Fatalf("sample %d: Bytes is not the encoding", i)
		}
		checkSampleView(t, &v, &want, enc)

		var got Sample
		v.Decode(&got)
		if !bytes.Equal(encode(&got), encode(&want)) || !within(got.Blob, enc) || !within(got.Inner.Data, enc) {
			t.Fatalf("sample %d: Decode = %+v, UnmarshalAvroBinary says %+v", i, got, want)
		}

		// Anything after the record is left alone.
		trailing := append(append([]byte(nil), enc...), 0xff, 0xff)
		if err := v.Parse(trailing); err != nil || v.Len() != len(enc) {
			t.Fatalf("sample %d: with trailing bytes, Parse = %v, Len %d, want nil, %d", i, err, v.Len(), len(enc))
		}
	}
}

// TestEveryTruncationIsNeedMoreWithinTheBounds cuts every encoding at every
// position. Short of the prefix it is io.ErrUnexpectedEOF, the view empty, and
// cut < Need <= PrefixLen; retrying with Need bytes parses or needs strictly
// more. From the prefix on it parses, and is Complete only from Len on.
func TestEveryTruncationIsNeedMoreWithinTheBounds(t *testing.T) {
	for i, s := range samples() {
		enc := encode(&s)
		var full SampleView
		if err := full.Parse(enc); err != nil {
			t.Fatal(err)
		}
		prefix, size := full.PrefixLen(), full.Len()

		for cut := range len(enc) + 1 {
			var v SampleView
			err := v.Parse(enc[:cut])
			if cut >= prefix {
				if err != nil || v.PrefixLen() != prefix || v.Len() != size || v.Complete() != (cut >= size) {
					t.Fatalf("sample %d: cut at %d, past the prefix's %d: Parse = %v", i, cut, prefix, err)
				}
				continue
			}
			if !errors.Is(err, io.ErrUnexpectedEOF) || err != io.ErrUnexpectedEOF {
				t.Fatalf("sample %d: cut at %d of a %d-byte prefix: Parse = %v, want io.ErrUnexpectedEOF unwrapped", i, cut, prefix, err)
			}
			if v.Need() <= cut || v.Need() > prefix {
				t.Fatalf("sample %d: cut at %d of a %d-byte prefix: Need = %d", i, cut, prefix, v.Need())
			}
			if v.Complete() {
				t.Fatalf("sample %d: cut at %d: an empty view is Complete", i, cut)
			}
			mustPanic(t, "Flag on a failed Parse", func() { v.Flag() })

			// Retrying with Need bytes makes progress until the prefix parses.
			for need := v.Need(); ; {
				err := v.Parse(enc[:need])
				if err == nil {
					break
				}
				if err != io.ErrUnexpectedEOF || v.Need() <= need {
					t.Fatalf("sample %d: retrying at %d: Parse = %v, Need %d", i, need, err, v.Need())
				}
				need = v.Need()
			}
		}
	}
}

// TestWholeIsCompleteWheneverItParses: a record whose last field is not bytes,
// string or a record ending in one has the whole record as its prefix.
func TestWholeIsCompleteWheneverItParses(t *testing.T) {
	for _, x := range []Whole{{}, {Id: math.MinInt64, Color: ColorBLUE, Digest: Digest{1, 2, 3, 4}}, {Id: 64}} {
		enc := x.AppendAvro(nil)
		var v WholeView
		if err := v.Parse(enc); err != nil {
			t.Fatal(err)
		}
		if !v.Complete() || v.PrefixLen() != len(enc) || v.Len() != len(enc) {
			t.Fatalf("WholeView: Complete %v, PrefixLen %d, Len %d over %d bytes", v.Complete(), v.PrefixLen(), v.Len(), len(enc))
		}
		var got Whole
		v.Decode(&got)
		if got != x || v.Id() != x.Id || v.Color() != x.Color || v.Digest() != x.Digest || v.Nothing().Len() != 0 {
			t.Fatalf("WholeView: Decode = %+v, want %+v", got, x)
		}
		for cut := range len(enc) {
			if err := v.Parse(enc[:cut]); err != io.ErrUnexpectedEOF || v.Need() <= cut || v.Need() > len(enc) {
				t.Fatalf("WholeView: cut at %d of %d: Parse = %v, Need %d", cut, len(enc), err, v.Need())
			}
		}
	}
}

// The offsets the constants name, in the encoding of any Sample: a boolean, a
// float, a double, a 4-byte fixed and a 3-byte record come before Count.
func TestTheOffsetConstantsAreTheOffsets(t *testing.T) {
	got := []int{SampleFlagOffset, SampleRatioOffset, SampleScoreOffset, SampleDigestOffset, SamplePairOffset, PairOnOffset, PairTagOffset}
	want := []int{0, 1, 5, 13, 17, 0, 1}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("offset constant %d = %d, want %d", i, got[i], want[i])
		}
	}
	x := Sample{Digest: Digest{9, 8, 7, 6}}
	if enc := encode(&x); !bytes.Equal(enc[SampleDigestOffset:SampleDigestOffset+4], x.Digest[:]) {
		t.Errorf("the digest is not at SampleDigestOffset")
	}
}

// countAt is the offset of Count in the encoding of a Sample with no
// variable-size field before it, and blobAt of Blob when Count and Total are
// one byte each.
const (
	countAt = 20
	totalAt = 21
	blobAt  = 22
)

func splice(b []byte, at, width int, with []byte) []byte {
	out := append([]byte(nil), b[:at]...)
	out = append(out, with...)
	return append(out, b[at+width:]...)
}

// TestCorruptInputIsReportedAndMoreBytesNeverMendIt is the corruption
// sentinels, each the one UnmarshalAvroBinary reports for the same input, and
// none of them cured by appending more bytes.
func TestCorruptInputIsReportedAndMoreBytesNeverMendIt(t *testing.T) {
	var zero Sample
	valid := encode(&zero)
	if valid[countAt] != 0 || valid[blobAt] != 0 {
		t.Fatalf("the zero Sample's Count and Blob are not at %d and %d", countAt, blobAt)
	}
	withColor := func(c Color) []byte { x := zero; x.Color = c; return encode(&x) }
	withBig := func(v Big) []byte { x := zero; x.Big = v; return encode(&x) }

	for _, tc := range []struct {
		name    string
		corrupt []byte
		want    error
	}{
		{"an enum ordinal past the symbols", withColor(3), avro.ErrInvalidIndex},
		{"a negative enum ordinal", withColor(-1), avro.ErrInvalidIndex},
		{"a two-byte ordinal past the symbols", withBig(70), avro.ErrInvalidIndex},
		{"a negative length", splice(valid, blobAt, 1, []byte{0x01}), avro.ErrNegativeLength},
		{"a two-byte negative length", splice(valid, blobAt, 1, []byte{0x81, 0x01}), avro.ErrNegativeLength},
		{"a length past 32 bits", splice(valid, blobAt, 1, avro.AppendLong(nil, 1<<32)), avro.ErrOverflow},
		{"an int past 32 bits", splice(valid, countAt, 1, avro.AppendLong(nil, 1<<32)), avro.ErrOverflow},
		{"an overlong varint", splice(valid, totalAt, 1, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f}), avro.ErrOverflow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var x Sample
			uerr := x.UnmarshalAvroBinary(avro.NewBytesReader(tc.corrupt))
			if !errors.Is(uerr, tc.want) {
				t.Fatalf("UnmarshalAvroBinary = %v, want %v", uerr, tc.want)
			}
			for extra := range 4 {
				b := append(append([]byte(nil), tc.corrupt...), bytes.Repeat([]byte{0}, extra*100)...)
				var v SampleView
				if err := v.Parse(b); err != tc.want {
					t.Fatalf("with %d more bytes: Parse = %v, want %v unwrapped", extra*100, err, tc.want)
				}
				if v.Need() != 0 || v.Complete() {
					t.Fatalf("a corrupt Parse left Need %d, Complete %v", v.Need(), v.Complete())
				}
				mustPanic(t, "PrefixLen on a corrupt Parse", func() { v.PrefixLen() })
			}
		})
	}
}

// everyAccessorPanics calls every exported method of the view v but Parse,
// Need and Complete, each of which answers on an empty view, and requires each
// to panic; Complete must be false and, on a view never parsed, Need 0.
func everyAccessorPanics(t *testing.T, name string, v any) {
	t.Helper()
	rv := reflect.ValueOf(v)
	called := 0
	for i := range rv.NumMethod() {
		m := rv.Type().Method(i)
		switch m.Name {
		case "Parse", "Need", "Complete":
			continue
		}
		args := make([]reflect.Value, m.Type.NumIn()-1)
		for j := range args {
			switch in := m.Type.In(j + 1); {
			case in == rv.Type():
				args[j] = rv
			case in.Kind() == reflect.Pointer:
				args[j] = reflect.New(in.Elem())
			default:
				args[j] = reflect.Zero(in)
			}
		}
		mustPanic(t, name+"."+m.Name+" on an empty view", func() { rv.Method(i).Call(args) })
		called++
	}
	// PrefixLen, Len, Bytes and Decode at least, and an accessor per field.
	if called < 4 {
		t.Errorf("%s: only %d methods were called", name, called)
	}
	if rv.MethodByName("Complete").Call(nil)[0].Bool() {
		t.Errorf("%s is Complete", name)
	}
}

func mustPanic(t *testing.T, what string, f func()) {
	t.Helper()
	defer func() {
		t.Helper()
		if recover() == nil {
			t.Fatalf("%s did not panic", what)
		}
	}()
	f()
}

// TestAnEmptyOrIncompleteViewPanics is every panic the views promise: every
// accessor on an empty view; the tail's accessors, Bytes and Decode on an
// incomplete one; and Compare on either, unless the tail is never compared.
func TestAnEmptyOrIncompleteViewPanics(t *testing.T) {
	var zero Sample
	valid := encode(&zero)
	corrupt := splice(valid, blobAt, 1, []byte{0x01})

	// Every view, zero and after a failed Parse — corrupt where its input
	// can be, and truncated where it cannot.
	failed := func(v interface{ Parse([]byte) error }, b []byte) any {
		if v.Parse(b) == nil {
			t.Fatalf("%T parsed %x", v, b)
		}
		return v
	}
	for name, v := range map[string]any{
		"SampleView":           &SampleView{},
		"PairView":             &PairView{},
		"InnerView":            &InnerView{},
		"LeafView":             &LeafView{},
		"WholeView":            &WholeView{},
		"NothingView":          &NothingView{},
		"KeyedView":            &KeyedView{},
		"corrupt SampleView":   failed(&SampleView{}, corrupt),
		"truncated SampleView": failed(&SampleView{}, valid[:countAt]),
		"truncated PairView":   failed(&PairView{}, []byte{1}),
		"truncated InnerView":  failed(&InnerView{}, []byte{2}),
		"truncated LeafView":   failed(&LeafView{}, nil),
		"truncated WholeView":  failed(&WholeView{}, []byte{0, 0}),
		"truncated KeyedView":  failed(&KeyedView{}, []byte{4, 'k'}),
	} {
		everyAccessorPanics(t, name, v)
	}

	x := randomSample(rand.New(rand.NewPCG(1, 2)))
	x.Leaf.Text = "a tail long enough to cut"
	enc := encode(&x)
	var v SampleView
	if err := v.Parse(enc[:len(enc)-1]); err != nil {
		t.Fatal(err)
	}
	if v.Complete() || v.Leaf().Complete() {
		t.Fatal("a view short of its tail is Complete")
	}
	if v.Leaf().TextLen() != len(x.Leaf.Text) || v.Name() != x.Name || v.Leaf().N() != x.Leaf.N {
		t.Fatal("an incomplete view does not answer from its prefix")
	}
	for name, f := range map[string]func(){
		"Bytes": func() { v.Bytes() }, "Decode": func() { v.Decode(&Sample{}) },
		"Leaf().Text": func() { v.Leaf().Text() }, "Leaf().TextBytes": func() { v.Leaf().TextBytes() },
		"Compare": func() { v.Compare(&v) },
	} {
		mustPanic(t, "SampleView."+name+" on an incomplete view", f)
	}

	// Keyed's tail is ignored, so its Compare needs only the prefixes.
	k := Keyed{Key: []byte("k"), Value: []byte("value")}
	kenc := k.AppendAvro(nil)
	var kv KeyedView
	if err := kv.Parse(kenc[:3]); err != nil {
		t.Fatal(err)
	}
	if kv.Complete() || kv.Compare(&kv) != 0 || kv.ValueLen() != len(k.Value) {
		t.Errorf("KeyedView on its prefix: Complete %v, Compare %d, ValueLen %d", kv.Complete(), kv.Compare(&kv), kv.ValueLen())
	}
	mustPanic(t, "KeyedView.Value on an incomplete view", func() { kv.Value() })
}

// TestViewCompareAgreesWithCompareAvro: on every pair, View.Compare is the
// encodings' Compare<Record>Avro.
func TestViewCompareAgreesWithCompareAvro(t *testing.T) {
	ss := samples()
	var outcomes [3]int
	for i := range ss {
		for _, j := range []int{i, (i + 1) % len(ss), (i * 7) % len(ss)} {
			a, b := encode(&ss[i]), encode(&ss[j])
			var va, vb SampleView
			if va.Parse(a) != nil || vb.Parse(b) != nil {
				t.Fatal("Parse failed on a valid encoding")
			}
			want, _, _ := CompareSampleAvro(a, b)
			if got := va.Compare(&vb); got != want {
				t.Fatalf("samples %d, %d: View.Compare = %d, CompareSampleAvro = %d", i, j, got, want)
			}
			outcomes[want+1]++
		}
	}
	for i, n := range outcomes {
		if n == 0 {
			t.Errorf("no pair compared %d", i-1)
		}
	}

	keys := [][]byte{{}, []byte("a"), []byte("b"), bytes.Repeat([]byte("a"), 100)}
	for _, x := range keys {
		for _, y := range keys {
			a, b := (&Keyed{Key: x, Value: y}).AppendAvro(nil), (&Keyed{Key: y, Value: x}).AppendAvro(nil)
			var va, vb KeyedView
			if va.Parse(a) != nil || vb.Parse(b) != nil {
				t.Fatal("Parse failed on a valid encoding")
			}
			if want, _, _ := CompareKeyedAvro(a, b); va.Compare(&vb) != want {
				t.Fatalf("KeyedView.Compare = %d, CompareKeyedAvro = %d", va.Compare(&vb), want)
			}
		}
	}
}

// TestTheViewsMakeNoAllocations is the allocation guarantee: Parse, every
// accessor that returns no string, and Compare make none; Decode makes none
// beyond its strings.
func TestTheViewsMakeNoAllocations(t *testing.T) {
	x := randomSample(rand.New(rand.NewPCG(3, 4)))
	enc := encode(&x)
	// Both parsed before the loop, which runs its cases in map order: Compare
	// may come first.
	var v, w SampleView
	if v.Parse(enc) != nil || w.Parse(enc) != nil {
		t.Fatal("Parse failed on a valid encoding")
	}

	var wv WholeView
	wenc := (&Whole{Id: 1000, Color: ColorGREEN}).AppendAvro(nil)
	var kv KeyedView
	kenc := (&Keyed{Key: []byte("key"), Value: []byte("value")}).AppendAvro(nil)

	var sink int
	for name, f := range map[string]func(){
		"Parse": func() { _ = v.Parse(enc) },
		"accessors": func() {
			_ = v.Parse(enc)
			if v.Flag() || v.FlagAgain() {
				sink++
			}
			sink += int(v.Ratio()) + int(v.Score()) + int(v.RatioAgain()) + int(v.ScoreAgain())
			sink += int(v.Digest()[0]) + len(v.DigestBytes()) + int(v.DigestAgain()[0]) + len(v.DigestAgainBytes())
			sink += len(v.Pair().TagBytes()) + int(v.PairAgain().Tag()[0]) + int(v.Count()) + int(v.Total())
			sink += len(v.Blob()) + len(v.NameBytes()) + int(v.Color()) + int(v.Big())
			sink += int(v.Inner().Id()) + len(v.Inner().LabelBytes()) + len(v.Inner().Data()) + v.Inner().DataLen()
			sink += int(v.Leaf().N()) + len(v.Leaf().TextBytes()) + v.Leaf().TextLen()
			if v.Pair().On() || v.PairAgain().On() {
				sink++
			}
			sink += v.PrefixLen() + v.Len() + v.Need() + len(v.Bytes())
		},
		"Compare": func() { sink += v.Compare(&w) },
		"WholeView": func() {
			_ = wv.Parse(wenc)
			sink += int(wv.Id()) + int(wv.Color()) + int(wv.Digest()[0]) + len(wv.DigestBytes()) + wv.Nothing().Len() + len(wv.Bytes())
			sink += wv.Compare(&wv)
		},
		"KeyedView": func() {
			_ = kv.Parse(kenc)
			sink += len(kv.Key()) + len(kv.Value()) + kv.ValueLen() + len(kv.Bytes()) + kv.Compare(&kv)
		},
	} {
		if n := testing.AllocsPerRun(100, f); n != 0 {
			t.Errorf("%s made %v allocations, want 0", name, n)
		}
	}

	var into Sample
	x.Name, x.Inner.Label, x.Leaf.Text = "", "", ""
	noStrings := encode(&x)
	if err := v.Parse(noStrings); err != nil {
		t.Fatal(err)
	}
	if n := testing.AllocsPerRun(100, func() { v.Decode(&into) }); n != 0 {
		t.Errorf("Decode with every string empty made %v allocations, want 0", n)
	}
	x.Name, x.Inner.Label, x.Leaf.Text = "name", "label", "text"
	if err := v.Parse(encode(&x)); err != nil {
		t.Fatal(err)
	}
	if n := testing.AllocsPerRun(100, func() { v.Decode(&into) }); n > 3 {
		t.Errorf("Decode with three strings made %v allocations, want at most 3", n)
	}
	_ = sink
}

// FuzzSampleViewParse holds Parse to UnmarshalAvroBinary on any input: it
// never panics; it reports corruption exactly when UnmarshalAvroBinary does,
// with the same sentinel; whenever UnmarshalAvroBinary succeeds it succeeds
// and Decode agrees; and whenever it succeeds Complete, UnmarshalAvroBinary
// does too.
func FuzzSampleViewParse(f *testing.F) {
	for i, s := range samples() {
		if i%20 == 0 {
			f.Add(encode(&s))
		}
	}
	var zero Sample
	valid := encode(&zero)
	f.Add(splice(valid, blobAt, 1, []byte{0x01}))
	f.Add(splice(valid, countAt, 1, avro.AppendLong(nil, 1<<32)))
	f.Add(valid[:30])
	f.Add([]byte{})

	corrupt := func(err error) bool {
		return errors.Is(err, avro.ErrOverflow) || errors.Is(err, avro.ErrNegativeLength) || errors.Is(err, avro.ErrInvalidIndex)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		var v SampleView
		perr := v.Parse(b)
		var x Sample
		uerr := x.UnmarshalAvroBinary(avro.NewBytesReader(b))

		switch {
		case perr != nil && perr != io.ErrUnexpectedEOF && !corrupt(perr):
			t.Fatalf("Parse returned %v, which is none of avro-go's sentinels", perr)
		case corrupt(perr) || corrupt(uerr):
			if !errors.Is(uerr, perr) {
				t.Fatalf("Parse = %v, UnmarshalAvroBinary = %v", perr, uerr)
			}
		case uerr == nil:
			if perr != nil || !v.Complete() {
				t.Fatalf("UnmarshalAvroBinary succeeded, and Parse = %v, Complete %v", perr, v.Complete())
			}
			var got Sample
			v.Decode(&got)
			if !bytes.Equal(encode(&got), encode(&x)) {
				t.Fatalf("Decode = %+v, UnmarshalAvroBinary says %+v", got, x)
			}
		case perr == nil:
			if v.Complete() {
				t.Fatalf("Parse succeeded Complete, and UnmarshalAvroBinary = %v", uerr)
			}
		default:
			if v.Need() <= len(b) {
				t.Fatalf("Parse = %v with Need %d over %d bytes", perr, v.Need(), len(b))
			}
		}
	})
}
