// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// RecordView (#251) against the committed generated code: Decode and every
// accessor against UnmarshalAvroBinary, every truncation and the Need bounds,
// corrupt input, the allocation counts, a fuzz test, and the benchmarks the
// pull request's figures come from.
package record

import (
	"bytes"
	"errors"
	"io"
	"testing"

	avro "github.com/z5labs/avro-go"
)

func TestRecordViewAgreesWithUnmarshal(t *testing.T) {
	for i, p := range recordPairs() {
		enc := p[0].AppendAvro(nil)
		var want Record
		if err := want.UnmarshalAvroBinary(avro.NewBytesReader(enc)); err != nil {
			t.Fatal(err)
		}

		var v RecordView
		if err := v.Parse(enc); err != nil {
			t.Fatalf("record %d: Parse: %v", i, err)
		}
		if !v.Complete() || v.Len() != len(enc) || v.PrefixLen() != len(enc)-len(want.Value) {
			t.Fatalf("record %d: Complete %v, Len %d, PrefixLen %d over %d bytes", i, v.Complete(), v.Len(), v.PrefixLen(), len(enc))
		}
		if v.Hash() != want.Hash || !bytes.Equal(v.HashBytes(), want.Hash[:]) ||
			!bytes.Equal(v.Key(), want.Key) || cap(v.Key()) != len(v.Key()) || v.Side() != want.Side ||
			!bytes.Equal(v.Value(), want.Value) || v.ValueLen() != len(want.Value) || !bytes.Equal(v.Bytes(), enc) {
			t.Fatalf("record %d: an accessor disagrees with %+v", i, want)
		}

		var got Record
		v.Decode(&got)
		if !bytes.Equal(got.AppendAvro(nil), enc) {
			t.Fatalf("record %d: Decode = %+v, want %+v", i, got, want)
		}
	}
}

// TestRecordViewNeedsOnlyTheHead is what a reader of blocks uses it for: with
// every byte of the record but its value, the key, the side and the value's
// size are all there.
func TestRecordViewNeedsOnlyTheHead(t *testing.T) {
	x := sized(Hash{1}, SideRIGHT)
	enc := x.AppendAvro(nil)
	head := len(enc) - len(x.Value)

	for cut := range head {
		var v RecordView
		err := v.Parse(enc[:cut])
		if err != io.ErrUnexpectedEOF || v.Need() <= cut || v.Need() > head {
			t.Fatalf("cut at %d of a %d-byte head: Parse = %v, Need %d", cut, head, err, v.Need())
		}
	}

	var v RecordView
	if err := v.Parse(enc[:head]); err != nil {
		t.Fatal(err)
	}
	if v.Complete() || !bytes.Equal(v.Key(), x.Key) || v.Side() != x.Side || v.ValueLen() != len(x.Value) || v.Len() != len(enc) {
		t.Fatalf("the head alone: Complete %v, Key %q, Side %v, ValueLen %d, Len %d", v.Complete(), v.Key(), v.Side(), v.ValueLen(), v.Len())
	}
	defer func() {
		if recover() == nil {
			t.Error("Value on a view without the value did not panic")
		}
	}()
	v.Value()
}

func TestRecordViewReportsCorruption(t *testing.T) {
	valid := sized(Hash{}, SideLEFT).AppendAvro(nil)
	const keyAt = RecordHashOffset + 8

	for _, tc := range []struct {
		name    string
		corrupt []byte
		want    error
	}{
		{"an out-of-range side", sized(Hash{}, Side(2)).AppendAvro(nil), avro.ErrInvalidIndex},
		{"a negative key length", append(append(append([]byte(nil), valid[:keyAt]...), 0x01), valid[keyAt+1:]...), avro.ErrNegativeLength},
		{"an overlong key length", append(append(append([]byte(nil), valid[:keyAt]...), 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f), valid[keyAt+1:]...), avro.ErrOverflow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var x Record
			if err := x.UnmarshalAvroBinary(avro.NewBytesReader(tc.corrupt)); !errors.Is(err, tc.want) {
				t.Fatalf("UnmarshalAvroBinary = %v, want %v", err, tc.want)
			}
			var v RecordView
			if err := v.Parse(append(tc.corrupt, make([]byte, 64)...)); err != tc.want {
				t.Fatalf("Parse = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRecordViewMakesNoAllocations(t *testing.T) {
	a, b := sized(Hash{1}, SideLEFT).AppendAvro(nil), sized(Hash{1}, SideRIGHT).AppendAvro(nil)
	// Both parsed before the loop, which runs its cases in map order: Compare
	// may come first.
	var v, w RecordView
	if v.Parse(a) != nil || w.Parse(b) != nil {
		t.Fatal("Parse failed on a valid encoding")
	}
	var into Record
	var sink int
	for name, f := range map[string]func(){
		"Parse and every accessor": func() {
			_ = v.Parse(a)
			sink += int(v.Hash()[0]) + len(v.HashBytes()) + len(v.Key()) + int(v.Side()) + len(v.Value()) + v.ValueLen()
		},
		"Compare": func() { sink += v.Compare(&w) },
		"Decode":  func() { v.Decode(&into) },
	} {
		if n := testing.AllocsPerRun(100, f); n != 0 {
			t.Errorf("%s made %v allocations, want 0", name, n)
		}
	}
	_ = sink
}

func TestRecordViewCompareAgreesWithCompareRecordAvro(t *testing.T) {
	for i, p := range recordPairs() {
		a, b := p[0].AppendAvro(nil), p[1].AppendAvro(nil)
		var va, vb RecordView
		if va.Parse(a) != nil || vb.Parse(b) != nil {
			t.Fatal("Parse failed on a valid encoding")
		}
		// The value is ignored, so the heads alone are enough.
		var ha, hb RecordView
		if ha.Parse(a[:va.PrefixLen()]) != nil || hb.Parse(b[:vb.PrefixLen()]) != nil {
			t.Fatal("Parse failed on a head")
		}
		want, _, _ := CompareRecordAvro(a, b)
		if va.Compare(&vb) != want || ha.Compare(&hb) != want {
			t.Fatalf("pair %d: View.Compare = %d, on the heads %d, CompareRecordAvro = %d", i, va.Compare(&vb), ha.Compare(&hb), want)
		}
	}
}

// FuzzRecordViewParse: no input makes Parse panic, and it agrees with
// UnmarshalAvroBinary on what is corrupt and on what decodes.
func FuzzRecordViewParse(f *testing.F) {
	for i, p := range recordPairs() {
		if i%40 == 0 {
			f.Add(p[0].AppendAvro(nil))
		}
	}
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0x01})

	corrupt := func(err error) bool {
		return errors.Is(err, avro.ErrOverflow) || errors.Is(err, avro.ErrNegativeLength) || errors.Is(err, avro.ErrInvalidIndex)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		var v RecordView
		perr := v.Parse(b)
		var x Record
		uerr := x.UnmarshalAvroBinary(avro.NewBytesReader(b))
		switch {
		case corrupt(perr) || corrupt(uerr):
			if !errors.Is(uerr, perr) {
				t.Fatalf("Parse = %v, UnmarshalAvroBinary = %v", perr, uerr)
			}
		case uerr == nil:
			var got Record
			if perr != nil || !v.Complete() {
				t.Fatalf("UnmarshalAvroBinary succeeded, and Parse = %v", perr)
			}
			v.Decode(&got)
			if !bytes.Equal(got.AppendAvro(nil), x.AppendAvro(nil)) {
				t.Fatalf("Decode = %+v, UnmarshalAvroBinary says %+v", got, x)
			}
		case perr == nil:
			if v.Complete() {
				t.Fatalf("Parse succeeded Complete, and UnmarshalAvroBinary = %v", uerr)
			}
		case perr != io.ErrUnexpectedEOF || v.Need() <= len(b):
			t.Fatalf("Parse = %v, Need %d over %d bytes", perr, v.Need(), len(b))
		}
	})
}

// BenchmarkRecordView is a reader's two operations on a record in a block:
// parsing its head and reading the key, side and value size from it, and
// ordering two parsed records — on a hash tie, decided on the key.
//
//	go test -run '^$' -bench RecordView ./record
func BenchmarkRecordView(b *testing.B) {
	x := sized(Hash{0, 0, 0, 0, 0, 0, 0, 7}, SideLEFT)
	y := &Record{Hash: x.Hash, Key: append(bytes.Repeat([]byte{'k'}, 15), 'l'), Value: x.Value}
	ex, ey := x.AppendAvro(nil), y.AppendAvro(nil)

	b.Run("Parse and accessors", func(b *testing.B) {
		b.ReportAllocs()
		var v RecordView
		var n int
		for b.Loop() {
			if err := v.Parse(ex); err != nil {
				b.Fatal(err)
			}
			n += len(v.Key()) + int(v.Side()) + v.ValueLen() + int(v.Hash()[7])
		}
		if n == 0 {
			b.Fatal("nothing read")
		}
	})
	b.Run("Compare on a hash tie", func(b *testing.B) {
		b.ReportAllocs()
		var vx, vy RecordView
		if vx.Parse(ex) != nil || vy.Parse(ey) != nil {
			b.Fatal("Parse failed")
		}
		var c int
		for b.Loop() {
			c = vx.Compare(&vy)
		}
		if c >= 0 {
			b.Fatalf("Compare = %d, want -1", c)
		}
	})
}
