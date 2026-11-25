// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// This file is not part of this repository's build: Go ignores testdata. It is
// copied beside the code avroc-gen-go generates for fixedInlineSchema, in a
// module of its own, by TestFixedValuesAreWrittenWithoutAllocating, and it is
// what that test runs there.
package fixedinline

import (
	"bytes"
	"io"
	"reflect"
	"testing"

	avro "github.com/z5labs/avro-go"
)

// A fixed type keeps its own MarshalAvroBinary even though a record no longer
// calls it: it is what makes the type usable on its own.
var _ avro.BinaryMarshaler = Hash{}

func packet() *Packet {
	return &Packet{
		Hash:   Hash{0x01, 0x02, 0x03, 0x04},
		Hashes: []Hash{{0xa0, 0xa1, 0xa2, 0xa3}, {0xb0, 0xb1, 0xb2, 0xb3}},
		Blob:   []byte{0xff, 0x00},
	}
}

// TestMarshalAvroBinaryMakesNoAllocations is #245's acceptance criterion: a
// fixed field, an array of fixed and a bytes field, encoded into a writer over
// io.Discard, put nothing on the heap.
func TestMarshalAvroBinaryMakesNoAllocations(t *testing.T) {
	p := packet()
	w := avro.NewBinaryWriter(io.Discard)

	var err error
	allocs := testing.AllocsPerRun(100, func() {
		err = p.MarshalAvroBinary(w)
	})
	if err != nil {
		t.Fatalf("MarshalAvroBinary: %v", err)
	}
	if allocs != 0 {
		t.Errorf("MarshalAvroBinary made %v allocations per run, want 0", allocs)
	}
}

// TestTheBytesAreAvrosBinaryEncoding pins the encoding itself, so that writing
// the fixed values inline is a change in how they are written and not in what.
func TestTheBytesAreAvrosBinaryEncoding(t *testing.T) {
	var buf bytes.Buffer
	if err := packet().MarshalAvroBinary(avro.NewBinaryWriter(&buf)); err != nil {
		t.Fatalf("MarshalAvroBinary: %v", err)
	}

	want := []byte{
		0x01, 0x02, 0x03, 0x04, // hash: four bytes, no length
		0x04, // hashes: one block of two items (zigzag 2)
		0xa0, 0xa1, 0xa2, 0xa3,
		0xb0, 0xb1, 0xb2, 0xb3,
		0x00,       // hashes: the terminating zero-count block
		0x04,       // blob: length 2 (zigzag)
		0xff, 0x00, // blob: its bytes
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("encoded %x, want %x", buf.Bytes(), want)
	}

	var got Packet
	if err := got.UnmarshalAvroBinary(avro.NewBinaryReader(bytes.NewReader(buf.Bytes()))); err != nil {
		t.Fatalf("UnmarshalAvroBinary: %v", err)
	}
	if !reflect.DeepEqual(&got, packet()) {
		t.Errorf("round trip gave %+v, want %+v", got, *packet())
	}
}

// TestSizeAvroAndAppendAvroAgreeWithMarshalAvroBinary holds the record and
// its fixed type to #249's invariants. The record is #245's: a fixed, an array
// of fixed and a bytes, which is the shape whose MarshalAvroBinary had to be
// rewritten to stop allocating, so it is the one AppendAvro has to get right
// from the start.
func TestSizeAvroAndAppendAvroAgreeWithMarshalAvroBinary(t *testing.T) {
	t.Run("Packet", func(t *testing.T) {
		checkDirectEncoding(t, packet(), func() avro.BinaryUnmarshaler { return &Packet{} })
	})
	t.Run("an empty Packet", func(t *testing.T) {
		checkDirectEncoding(t, &Packet{}, func() avro.BinaryUnmarshaler { return &Packet{} })
	})
	t.Run("Hash", func(t *testing.T) {
		checkDirectEncoding(t, Hash{0xca, 0xfe, 0xba, 0xbe}, func() avro.BinaryUnmarshaler { return &Hash{} })
	})
}
