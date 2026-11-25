// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// CompareRecordAvro (#250) against the committed generated code: a reference
// comparison over decoded Records, every truncation, the allocation count, a
// fuzz test, and the benchmarks the pull request's figures come from.
//
// It lives beside the generated code for the reason stream/event_stream_test.go
// does: avroc never removes a file it has no record of generating.
package record

import (
	"bytes"
	"cmp"
	"errors"
	"io"
	"math/rand/v2"
	"testing"

	avro "github.com/z5labs/avro-go"
)

// refRecord is Record's sort order, written from the schema: hash and key by
// unsigned bytes, side by ordinal, value not at all.
func refRecord(x, y *Record) int {
	if c := bytes.Compare(x.Hash[:], y.Hash[:]); c != 0 {
		return c
	}
	if c := bytes.Compare(x.Key, y.Key); c != 0 {
		return c
	}
	return cmp.Compare(x.Side, y.Side)
}

// sized is the proposal's shape (#244): a 16-byte key and a 200-byte value.
func sized(hash Hash, side Side) *Record {
	return &Record{
		Hash:  hash,
		Key:   bytes.Repeat([]byte{'k'}, 16),
		Side:  side,
		Value: bytes.Repeat([]byte{'v'}, 200),
	}
}

// recordPairs ties each pair on a leading run of fields — none, the hash, the
// hash and the key, all three — over small domains, with keys long enough to
// take a two-byte length.
func recordPairs() [][2]Record {
	r := rand.New(rand.NewPCG(250, 244))
	hashes := []Hash{{}, {0, 0, 0, 0, 0, 0, 0, 1}, {1}, {0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}}
	keys := [][]byte{{}, []byte("a"), []byte("ab"), []byte("b"), {0xff}, bytes.Repeat([]byte("a"), 64), bytes.Repeat([]byte("a"), 100)}
	random := func(x *Record, from int) {
		if from <= 0 {
			x.Hash = hashes[r.IntN(len(hashes))]
		}
		if from <= 1 {
			x.Key = keys[r.IntN(len(keys))]
		}
		if from <= 2 {
			x.Side = Side(r.IntN(2))
		}
		x.Value = keys[r.IntN(len(keys))]
	}

	var out [][2]Record
	for range 200 {
		var a Record
		random(&a, 0)
		for tie := range 4 {
			b := a
			random(&b, tie)
			out = append(out, [2]Record{a, b})
		}
	}
	return out
}

func TestCompareRecordAvroAgreesWithTheReference(t *testing.T) {
	for i, p := range recordPairs() {
		a, b := p[0].AppendAvro(nil), p[1].AppendAvro(nil)
		c, na, nb := CompareRecordAvro(a, b)
		if na < 0 || nb < 0 {
			t.Fatalf("pair %d: %v, %v on valid encodings", i, avro.ConsumeError(na), avro.ConsumeError(nb))
		}
		if want := refRecord(&p[0], &p[1]); c != want {
			t.Fatalf("pair %d: CompareRecordAvro = %d, the reference says %d\na: %+v\nb: %+v", i, c, want, p[0], p[1])
		}
		if rc, _, _ := CompareRecordAvro(b, a); rc != -c {
			t.Fatalf("pair %d: reversed, CompareRecordAvro = %d, want %d", i, rc, -c)
		}
	}
}

// TestCompareRecordAvroNeverReadsTheValue holds the records' prefixes only —
// everything through side — which is what a reader holding a record's head
// in a block has before the value arrives.
func TestCompareRecordAvroNeverReadsTheValue(t *testing.T) {
	x := sized(Hash{1}, SideLEFT)
	full := x.AppendAvro(nil)
	prefix := len(full) - avro.SizeBytes(len(x.Value))

	if c, na, nb := CompareRecordAvro(full[:prefix], full[:prefix]); c != 0 || na != prefix || nb != prefix {
		t.Errorf("CompareRecordAvro(prefix, prefix) = %d, %d, %d, want 0, %d, %d", c, na, nb, prefix, prefix)
	}
}

func TestTruncatingARecordReportsNeedMoreOnThatSide(t *testing.T) {
	for i, p := range recordPairs() {
		a, b := p[0].AppendAvro(nil), p[1].AppendAvro(nil)
		c, na, nb := CompareRecordAvro(a, b)
		for cut := range len(a) {
			gc, gna, gnb := CompareRecordAvro(a[:cut], b)
			switch {
			case cut >= na:
				if gc != c || gna != na || gnb != nb {
					t.Fatalf("pair %d: a cut at %d, after the decision at %d, changed the result", i, cut, na)
				}
			case gc != 0 || !errors.Is(avro.ConsumeError(gna), io.ErrUnexpectedEOF):
				t.Fatalf("pair %d: a cut at %d of %d: got %d, %v, want 0, need-more", i, cut, na, gc, avro.ConsumeError(gna))
			}
		}
		for cut := range len(b) {
			gc, gna, gnb := CompareRecordAvro(a, b[:cut])
			switch {
			case cut >= nb:
				if gc != c || gna != na || gnb != nb {
					t.Fatalf("pair %d: b cut at %d, after the decision at %d, changed the result", i, cut, nb)
				}
			case gc != 0 || !errors.Is(avro.ConsumeError(gnb), io.ErrUnexpectedEOF):
				t.Fatalf("pair %d: b cut at %d of %d: got %d, %v, want 0, need-more", i, cut, nb, gc, avro.ConsumeError(gnb))
			}
		}
	}
}

// TestAnOutOfRangeSideIsAnInvalidIndex is the enum row of the ordering table:
// an ordinal no symbol names is avro.ErrInvalidIndex, on that side alone.
func TestAnOutOfRangeSideIsAnInvalidIndex(t *testing.T) {
	valid := sized(Hash{}, SideRIGHT).AppendAvro(nil)
	corrupt := sized(Hash{}, Side(2)).AppendAvro(nil)

	c, na, nb := CompareRecordAvro(corrupt, valid)
	if c != 0 || !errors.Is(avro.ConsumeError(na), avro.ErrInvalidIndex) || nb < 0 {
		t.Errorf("CompareRecordAvro(corrupt, valid) = %d, %v, %d; want 0, ErrInvalidIndex, a count", c, avro.ConsumeError(na), nb)
	}
}

func TestCompareRecordAvroMakesNoAllocations(t *testing.T) {
	a, b := sized(Hash{1}, SideLEFT).AppendAvro(nil), sized(Hash{1}, SideRIGHT).AppendAvro(nil)
	if n := testing.AllocsPerRun(100, func() { CompareRecordAvro(a, b) }); n != 0 {
		t.Errorf("CompareRecordAvro made %v allocations, want 0", n)
	}
}

// FuzzCompareRecordAvro: no input makes it panic or read out of bounds, and
// whenever both sides decode it agrees with the reference.
func FuzzCompareRecordAvro(f *testing.F) {
	for i, p := range recordPairs() {
		if i%40 == 0 {
			f.Add(p[0].AppendAvro(nil), p[1].AppendAvro(nil))
		}
	}
	f.Add([]byte{}, []byte{0, 0, 0, 0, 0, 0, 0, 0, 0x01})

	f.Fuzz(func(t *testing.T, a, b []byte) {
		c, na, nb := CompareRecordAvro(a, b)
		if c < -1 || c > 1 || ((na < 0 || nb < 0) && c != 0) || na > len(a) || nb > len(b) {
			t.Fatalf("CompareRecordAvro = %d, %d, %d over %d and %d bytes", c, na, nb, len(a), len(b))
		}
		var x, y Record
		if x.UnmarshalAvroBinary(avro.NewBytesReader(a)) != nil || y.UnmarshalAvroBinary(avro.NewBytesReader(b)) != nil {
			return
		}
		if na < 0 || nb < 0 {
			t.Fatalf("both sides decode, and CompareRecordAvro reported %v, %v", avro.ConsumeError(na), avro.ConsumeError(nb))
		}
		if want := refRecord(&x, &y); c != want {
			t.Fatalf("CompareRecordAvro = %d, the reference says %d", c, want)
		}
	})
}

// BenchmarkCompareRecordAvro is the two cases a sort spends its time in: a
// hash tie, decided on the key, and differing hashes, decided on the first
// field.
//
//	go test -run '^$' -bench CompareRecordAvro ./record
func BenchmarkCompareRecordAvro(b *testing.B) {
	for _, bc := range []struct {
		name string
		x, y *Record
	}{
		{"hash tie", sized(Hash{0, 0, 0, 0, 0, 0, 0, 7}, SideLEFT), &Record{Hash: Hash{0, 0, 0, 0, 0, 0, 0, 7}, Key: append(bytes.Repeat([]byte{'k'}, 15), 'l'), Value: bytes.Repeat([]byte{'v'}, 200)}},
		{"hashes differ", sized(Hash{0, 0, 0, 0, 0, 0, 0, 7}, SideLEFT), sized(Hash{0, 0, 0, 0, 0, 0, 0, 8}, SideLEFT)},
	} {
		b.Run(bc.name, func(b *testing.B) {
			x, y := bc.x.AppendAvro(nil), bc.y.AppendAvro(nil)
			b.ReportAllocs()
			var c int
			for b.Loop() {
				c, _, _ = CompareRecordAvro(x, y)
			}
			if c >= 0 {
				b.Fatalf("CompareRecordAvro = %d, want -1", c)
			}
		})
	}
}
