// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// This file is not part of this repository's build: Go ignores testdata. It is
// copied beside the code avroc-gen-go generates for compareSampleSchema under
// compare=true, by TestCompareAgreesWithTheReferenceOnEveryPair.
//
// CompareSampleAvro is checked against refSample, a comparison over decoded
// values written from the specification's sort order rather than from the
// generator, on pairs that tie on every leading prefix of fields; then every
// truncation of both sides, the error codes for corrupt input, the allocation
// count and a fuzz test whose seeds run with every go test.
package compare

import (
	"bytes"
	"cmp"
	"errors"
	"io"
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	avro "github.com/z5labs/avro-go"
)

func cmpBool(x, y bool) int {
	b := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	return cmp.Compare(b(x), b(y))
}

// refInner is Inner's sort order: id ascending, label descending, blob
// ignored, n ascending.
func refInner(x, y *Inner) int {
	if c := cmp.Compare(x.Id, y.Id); c != 0 {
		return c
	}
	if c := -strings.Compare(x.Label, y.Label); c != 0 {
		return c
	}
	return cmp.Compare(x.N, y.N)
}

// refSample is Sample's sort order: each type ascending, then descending, then
// ignored, and the trailing map, array and union ignored.
func refSample(x, y *Sample) int {
	for _, c := range []func() int{
		func() int { return cmpBool(x.BooleanValue, y.BooleanValue) },
		func() int { return -cmpBool(x.BooleanValueDown, y.BooleanValueDown) },
		func() int { return cmp.Compare(x.IntValue, y.IntValue) },
		func() int { return -cmp.Compare(x.IntValueDown, y.IntValueDown) },
		func() int { return cmp.Compare(x.LongValue, y.LongValue) },
		func() int { return -cmp.Compare(x.LongValueDown, y.LongValueDown) },
		func() int { return cmp.Compare(x.FloatValue, y.FloatValue) },
		func() int { return -cmp.Compare(x.FloatValueDown, y.FloatValueDown) },
		func() int { return cmp.Compare(x.DoubleValue, y.DoubleValue) },
		func() int { return -cmp.Compare(x.DoubleValueDown, y.DoubleValueDown) },
		func() int { return bytes.Compare(x.BytesValue, y.BytesValue) },
		func() int { return -bytes.Compare(x.BytesValueDown, y.BytesValueDown) },
		func() int { return strings.Compare(x.StringValue, y.StringValue) },
		func() int { return -strings.Compare(x.StringValueDown, y.StringValueDown) },
		func() int { return bytes.Compare(x.Digest[:], y.Digest[:]) },
		func() int { return -bytes.Compare(x.DigestDown[:], y.DigestDown[:]) },
		func() int { return bytes.Compare(x.Wide[:], y.Wide[:]) },
		func() int { return -bytes.Compare(x.WideDown[:], y.WideDown[:]) },
		func() int { return bytes.Compare(x.Huge[:], y.Huge[:]) },
		func() int { return -bytes.Compare(x.HugeDown[:], y.HugeDown[:]) },
		func() int { return cmp.Compare(x.Color, y.Color) },
		func() int { return -cmp.Compare(x.ColorDown, y.ColorDown) },
		func() int { return cmp.Compare(x.Big, y.Big) },
		func() int { return -cmp.Compare(x.BigDown, y.BigDown) },
		func() int { return refInner(&x.Inner, &y.Inner) },
		func() int { return -refInner(&x.InnerDown, &y.InnerDown) },
	} {
		if c := c(); c != 0 {
			return c
		}
	}
	return 0
}

// Small domains, so that two random values tie often, holding the edges of
// every encoding: the one-byte varint's ends (-64, 63) and the first values
// past them, a length past the one-byte case, NaN, both zeros and the
// infinities, an ordinal past 63.
var (
	ints    = []int64{0, 1, -1, 63, -64, 64, -65, 1000, -1000, math.MaxInt32, math.MinInt32}
	longs   = append(append([]int64(nil), ints...), math.MaxInt64, math.MinInt64)
	floats  = []float64{0, math.Copysign(0, -1), 1, -1, 1.5, math.NaN(), math.Inf(1), math.Inf(-1), math.SmallestNonzeroFloat32}
	payload = []string{"", "a", "ab", "b", "\xff", "\x00", strings.Repeat("a", 100), strings.Repeat("a", 100) + "b", strings.Repeat("a", 64)}
	digests = []Digest{{}, {0, 0, 0, 1}, {0xff}, {1}, {0xff, 0xff, 0xff, 0xff}}
	bigs    = []Big{0, 1, 63, 64, 69}
)

// randomFixed fills a fixed from a few bytes, so that two values often agree
// on a long prefix and differ in any one word — the last byte included.
func randomFixed(r *rand.Rand, dst []byte) {
	fill := pick(r, []byte{0, 0xff, 1})
	for i := range dst {
		dst[i] = fill
	}
	if r.IntN(2) == 0 {
		dst[r.IntN(len(dst))] = pick(r, []byte{0, 0x7f, 0x80, 0xff})
	}
}

func pick[T any](r *rand.Rand, from []T) T { return from[r.IntN(len(from))] }

func randomBytes(r *rand.Rand) []byte { return []byte(pick(r, payload)) }

// innerSetters set each field of an Inner, in schema order.
func innerSetters() []func(*Inner, *rand.Rand) {
	return []func(*Inner, *rand.Rand){
		func(x *Inner, r *rand.Rand) { x.Id = pick(r, longs) },
		func(x *Inner, r *rand.Rand) { x.Label = pick(r, payload) },
		func(x *Inner, r *rand.Rand) { x.Blob = randomBytes(r) },
		func(x *Inner, r *rand.Rand) { x.N = int32(pick(r, ints)) },
	}
}

func randomInner(r *rand.Rand) Inner {
	var x Inner
	for _, set := range innerSetters() {
		set(&x, r)
	}
	return x
}

// sampleSetters set each field of a Sample, in schema order; the indexes of
// Inner and InnerDown are innerAt and innerDownAt.
func sampleSetters() []func(*Sample, *rand.Rand) {
	return []func(*Sample, *rand.Rand){
		func(x *Sample, r *rand.Rand) { x.BooleanValue = r.IntN(2) == 0 },
		func(x *Sample, r *rand.Rand) { x.BooleanValueDown = r.IntN(2) == 0 },
		func(x *Sample, r *rand.Rand) { x.BooleanValueIgnored = r.IntN(2) == 0 },
		func(x *Sample, r *rand.Rand) { x.IntValue = int32(pick(r, ints)) },
		func(x *Sample, r *rand.Rand) { x.IntValueDown = int32(pick(r, ints)) },
		func(x *Sample, r *rand.Rand) { x.IntValueIgnored = int32(pick(r, ints)) },
		func(x *Sample, r *rand.Rand) { x.LongValue = pick(r, longs) },
		func(x *Sample, r *rand.Rand) { x.LongValueDown = pick(r, longs) },
		func(x *Sample, r *rand.Rand) { x.LongValueIgnored = pick(r, longs) },
		func(x *Sample, r *rand.Rand) { x.FloatValue = float32(pick(r, floats)) },
		func(x *Sample, r *rand.Rand) { x.FloatValueDown = float32(pick(r, floats)) },
		func(x *Sample, r *rand.Rand) { x.FloatValueIgnored = float32(pick(r, floats)) },
		func(x *Sample, r *rand.Rand) { x.DoubleValue = pick(r, floats) },
		func(x *Sample, r *rand.Rand) { x.DoubleValueDown = pick(r, floats) },
		func(x *Sample, r *rand.Rand) { x.DoubleValueIgnored = pick(r, floats) },
		func(x *Sample, r *rand.Rand) { x.BytesValue = randomBytes(r) },
		func(x *Sample, r *rand.Rand) { x.BytesValueDown = randomBytes(r) },
		func(x *Sample, r *rand.Rand) { x.BytesValueIgnored = randomBytes(r) },
		func(x *Sample, r *rand.Rand) { x.StringValue = pick(r, payload) },
		func(x *Sample, r *rand.Rand) { x.StringValueDown = pick(r, payload) },
		func(x *Sample, r *rand.Rand) { x.StringValueIgnored = pick(r, payload) },
		func(x *Sample, r *rand.Rand) { x.Digest = pick(r, digests) },
		func(x *Sample, r *rand.Rand) { x.DigestDown = pick(r, digests) },
		func(x *Sample, r *rand.Rand) { x.DigestIgnored = pick(r, digests) },
		func(x *Sample, r *rand.Rand) { randomFixed(r, x.Wide[:]) },
		func(x *Sample, r *rand.Rand) { randomFixed(r, x.WideDown[:]) },
		func(x *Sample, r *rand.Rand) { randomFixed(r, x.WideIgnored[:]) },
		func(x *Sample, r *rand.Rand) { randomFixed(r, x.Huge[:]) },
		func(x *Sample, r *rand.Rand) { randomFixed(r, x.HugeDown[:]) },
		func(x *Sample, r *rand.Rand) { randomFixed(r, x.HugeIgnored[:]) },
		func(x *Sample, r *rand.Rand) { x.Color = Color(r.IntN(3)) },
		func(x *Sample, r *rand.Rand) { x.ColorDown = Color(r.IntN(3)) },
		func(x *Sample, r *rand.Rand) { x.ColorIgnored = Color(r.IntN(3)) },
		func(x *Sample, r *rand.Rand) { x.Big = pick(r, bigs) },
		func(x *Sample, r *rand.Rand) { x.BigDown = pick(r, bigs) },
		func(x *Sample, r *rand.Rand) { x.BigIgnored = pick(r, bigs) },
		func(x *Sample, r *rand.Rand) { x.Inner = randomInner(r) },
		func(x *Sample, r *rand.Rand) { x.InnerDown = randomInner(r) },
		func(x *Sample, r *rand.Rand) { x.InnerIgnored = randomInner(r) },
		func(x *Sample, r *rand.Rand) {
			x.Tags = map[string]string{}
			for range r.IntN(3) {
				x.Tags[pick(r, payload)] = pick(r, payload)
			}
		},
		func(x *Sample, r *rand.Rand) {
			x.List = nil
			for range r.IntN(3) {
				x.List = append(x.List, pick(r, longs))
			}
		},
		func(x *Sample, r *rand.Rand) {
			if r.IntN(2) == 0 {
				x.Choice = SampleChoiceUnionNull{}
			} else {
				x.Choice = SampleChoiceUnionString{Value: pick(r, payload)}
			}
		},
	}
}

const innerAt, innerDownAt = 36, 37

func randomSample(r *rand.Rand) Sample {
	var x Sample
	for _, set := range sampleSetters() {
		set(&x, r)
	}
	return x
}

// pairs is every pair the tests compare: for each random base, one partner
// per field that ties the base on every field before it and is random from it
// on, and one per field of Inner and of InnerDown that ties on everything
// before that.
func pairs() [][2]Sample {
	r := rand.New(rand.NewPCG(250, 244))
	setters := sampleSetters()
	inner := innerSetters()

	var out [][2]Sample
	for range 100 {
		a := randomSample(r)
		out = append(out, [2]Sample{a, a})
		for k := range setters {
			b := a
			for _, set := range setters[k:] {
				set(&b, r)
			}
			out = append(out, [2]Sample{a, b})
		}
		for _, at := range []int{innerAt, innerDownAt} {
			for m := range inner {
				b := a
				target := &b.Inner
				if at == innerDownAt {
					target = &b.InnerDown
				}
				for _, set := range inner[m:] {
					set(target, r)
				}
				for _, set := range setters[at+1:] {
					set(&b, r)
				}
				out = append(out, [2]Sample{a, b})
			}
		}
	}
	return out
}

func encode(x *Sample) []byte { return x.AppendAvro(nil) }

// TestCompareSampleAgreesWithTheReference is the reference comparison, on
// every pair, in both directions.
func TestCompareSampleAgreesWithTheReference(t *testing.T) {
	var decidedBy [3]int
	for i, p := range pairs() {
		a, b := encode(&p[0]), encode(&p[1])
		want := refSample(&p[0], &p[1])
		decidedBy[want+1]++

		c, na, nb := CompareSampleAvro(a, b)
		if na < 0 || nb < 0 {
			t.Fatalf("pair %d: CompareSampleAvro reported %v, %v on valid encodings", i, avro.ConsumeError(na), avro.ConsumeError(nb))
		}
		if na > len(a) || nb > len(b) {
			t.Fatalf("pair %d: counts %d, %d exceed the encodings' %d, %d", i, na, nb, len(a), len(b))
		}
		if c != want {
			t.Fatalf("pair %d: CompareSampleAvro = %d, the reference says %d\na: %+v\nb: %+v", i, c, want, p[0], p[1])
		}
		if rc, rna, rnb := CompareSampleAvro(b, a); rc != -c || rna != nb || rnb != na {
			t.Fatalf("pair %d: reversed, CompareSampleAvro = %d, %d, %d, want %d, %d, %d", i, rc, rna, rnb, -c, nb, na)
		}
	}
	// The pairs are only evidence if they reach every outcome.
	for i, n := range decidedBy {
		if n == 0 {
			t.Errorf("no pair compared %d", i-1)
		}
	}
}

// TestCompareNeverReadsTheTrailingIgnoredFields is where reading stops: on
// equal records it is the end of InnerDown, the last compared field, so the
// trailing run of ignored fields — InnerIgnored, Tags, List and Choice — need
// not be there at all.
func TestCompareNeverReadsTheTrailingIgnoredFields(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	x := randomSample(r)
	x.Tags, x.List, x.Choice = nil, nil, SampleChoiceUnionNull{}
	full := encode(&x)
	// An empty map, an empty array and union branch 0 are a byte each.
	prefix := len(full) - 3 - x.InnerIgnored.SizeAvro()

	c, na, nb := CompareSampleAvro(full[:prefix], full[:prefix])
	if c != 0 || na != prefix || nb != prefix {
		t.Errorf("CompareSampleAvro(prefix, prefix) = %d, %d, %d, want 0, %d, %d", c, na, nb, prefix, prefix)
	}
}

// TestTruncatingEitherSideReportsNeedMoreOnThatSide cuts each side of every
// pair at every position. A cut at or past where the comparison was decided
// changes nothing; any earlier one is io.ErrUnexpectedEOF on the side that was
// cut, with c 0.
func TestTruncatingEitherSideReportsNeedMoreOnThatSide(t *testing.T) {
	for i, p := range pairs() {
		a, b := encode(&p[0]), encode(&p[1])
		c, na, nb := CompareSampleAvro(a, b)

		for cut := range len(a) {
			gc, gna, gnb := CompareSampleAvro(a[:cut], b)
			if cut >= na {
				if gc != c || gna != na || gnb != nb {
					t.Fatalf("pair %d: a cut at %d, after the decision at %d: got %d, %d, %d, want %d, %d, %d", i, cut, na, gc, gna, gnb, c, na, nb)
				}
				continue
			}
			if gc != 0 || !errors.Is(avro.ConsumeError(gna), io.ErrUnexpectedEOF) {
				t.Fatalf("pair %d: a cut at %d of %d: got %d, %v, want 0, need-more", i, cut, na, gc, avro.ConsumeError(gna))
			}
			if gnb > len(b) {
				t.Fatalf("pair %d: a cut at %d: b's count %d exceeds its %d bytes", i, cut, gnb, len(b))
			}
		}
		for cut := range len(b) {
			gc, gna, gnb := CompareSampleAvro(a, b[:cut])
			if cut >= nb {
				if gc != c || gna != na || gnb != nb {
					t.Fatalf("pair %d: b cut at %d, after the decision at %d: got %d, %d, %d, want %d, %d, %d", i, cut, nb, gc, gna, gnb, c, na, nb)
				}
				continue
			}
			if gc != 0 || !errors.Is(avro.ConsumeError(gnb), io.ErrUnexpectedEOF) {
				t.Fatalf("pair %d: b cut at %d of %d: got %d, %v, want 0, need-more", i, cut, nb, gc, avro.ConsumeError(gnb))
			}
		}
	}
}

// The offsets of fields in the encoding of a zero Sample: three booleans, three
// one-byte ints, three one-byte longs, three floats and three doubles come
// before BytesValue.
const (
	intAt   = 3
	longAt  = 6
	bytesAt = 3 + 3 + 3 + 3*4 + 3*8
)

// zeroSample is the zero Sample with its union set, which a nil interface
// cannot be encoded as.
func zeroSample() Sample { return Sample{Choice: SampleChoiceUnionNull{}} }

func splice(b []byte, at, width int, with []byte) []byte {
	out := append([]byte(nil), b[:at]...)
	out = append(out, with...)
	return append(out, b[at+width:]...)
}

// TestCorruptInputIsReportedOnItsOwnSide is the error codes: each is the one
// avro-go's Consume functions give, reported on the corrupt side alone, with
// the other side's count through the field and c 0.
func TestCorruptInputIsReportedOnItsOwnSide(t *testing.T) {
	zero := zeroSample()
	valid := encode(&zero)
	if valid[bytesAt] != 0 {
		t.Fatalf("the zero Sample's BytesValue is not at offset %d", bytesAt)
	}

	withColor := func(c Color) []byte { x := zero; x.Color = c; return encode(&x) }
	withBig := func(v Big) []byte { x := zero; x.Big = v; return encode(&x) }

	testCases := []struct {
		name    string
		corrupt []byte
		want    error
	}{
		{"an enum ordinal past the symbols", withColor(3), avro.ErrInvalidIndex},
		{"a negative enum ordinal", withColor(-1), avro.ErrInvalidIndex},
		{"a two-byte ordinal past the symbols", withBig(70), avro.ErrInvalidIndex},
		{"a negative length", splice(valid, bytesAt, 1, []byte{0x01}), avro.ErrNegativeLength},
		{"a two-byte negative length", splice(valid, bytesAt, 1, []byte{0x81, 0x01}), avro.ErrNegativeLength},
		{"an int past 32 bits", splice(valid, intAt, 1, avro.AppendLong(nil, 1<<32)), avro.ErrOverflow},
		{"a long past 64 bits", splice(valid, longAt, 1, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f}), avro.ErrOverflow},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			c, na, nb := CompareSampleAvro(tc.corrupt, valid)
			if c != 0 || !errors.Is(avro.ConsumeError(na), tc.want) || nb < 0 {
				t.Errorf("CompareSampleAvro(corrupt, valid) = %d, %v, %d; want 0, %v, a count", c, avro.ConsumeError(na), nb, tc.want)
			}
			c, na, nb = CompareSampleAvro(valid, tc.corrupt)
			if c != 0 || na < 0 || !errors.Is(avro.ConsumeError(nb), tc.want) {
				t.Errorf("CompareSampleAvro(valid, corrupt) = %d, %d, %v; want 0, a count, %v", c, na, avro.ConsumeError(nb), tc.want)
			}
			c, na, nb = CompareSampleAvro(tc.corrupt, tc.corrupt)
			if c != 0 || !errors.Is(avro.ConsumeError(na), tc.want) || !errors.Is(avro.ConsumeError(nb), tc.want) {
				t.Errorf("CompareSampleAvro(corrupt, corrupt) = %d, %v, %v; want 0, %v on both", c, avro.ConsumeError(na), avro.ConsumeError(nb), tc.want)
			}
		})
	}
}

// TestCompareSampleMakesNoAllocations is the allocation guarantee.
func TestCompareSampleMakesNoAllocations(t *testing.T) {
	ps := pairs()
	a, b := encode(&ps[len(ps)-1][0]), encode(&ps[len(ps)-1][1])
	if n := testing.AllocsPerRun(100, func() { CompareSampleAvro(a, b) }); n != 0 {
		t.Errorf("CompareSampleAvro made %v allocations, want 0", n)
	}
}

// FuzzCompareSampleAvro holds CompareSampleAvro to two things on any input:
// it does not panic — which covers reading out of bounds — and returns
// results in range; and whenever both sides decode, it agrees with the
// reference over the decoded values.
func FuzzCompareSampleAvro(f *testing.F) {
	for i, p := range pairs() {
		if i%17 == 0 {
			f.Add(encode(&p[0]), encode(&p[1]))
		}
	}
	zero := zeroSample()
	valid := encode(&zero)
	f.Add(valid, splice(valid, bytesAt, 1, []byte{0x01}))
	f.Add(splice(valid, intAt, 1, avro.AppendLong(nil, 1<<32)), valid[:10])
	f.Add([]byte{}, []byte(nil))

	f.Fuzz(func(t *testing.T, a, b []byte) {
		c, na, nb := CompareSampleAvro(a, b)
		if c < -1 || c > 1 {
			t.Fatalf("c = %d", c)
		}
		if (na < 0 || nb < 0) && c != 0 {
			t.Fatalf("c = %d beside counts %d, %d", c, na, nb)
		}
		if na > len(a) || nb > len(b) {
			t.Fatalf("counts %d, %d exceed the inputs' %d, %d", na, nb, len(a), len(b))
		}

		var x, y Sample
		if x.UnmarshalAvroBinary(avro.NewBytesReader(a)) != nil || y.UnmarshalAvroBinary(avro.NewBytesReader(b)) != nil {
			return
		}
		if na < 0 || nb < 0 {
			t.Fatalf("both sides decode, and CompareSampleAvro reported %v, %v", avro.ConsumeError(na), avro.ConsumeError(nb))
		}
		if want := refSample(&x, &y); c != want {
			t.Fatalf("CompareSampleAvro = %d, the reference over the decoded values says %d", c, want)
		}
	})
}
