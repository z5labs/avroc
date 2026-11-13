// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// This file is not part of this repository's build: Go ignores testdata. It is
// copied beside the code avroc-gen-go generates for nestedIssueSchema and
// nestedShapesSchema, in a module of its own, by
// TestNestedContainersCompileAndRoundTrip, and it is what that test runs there.
package nested

import (
	"bytes"
	"reflect"
	"testing"

	avro "github.com/z5labs/avro-go"
)

// The values below hold empty inner arrays as nil and empty inner maps as
// empty-but-made, because that is what a decode produces for each: a decoded
// array is only ever appended to, and a decoded map is always made.

func outer() Outer {
	return Outer{
		Grid: [][]int32{{1, 2}, nil, {3}},
		Rows: []map[string]int32{{"a": 1}, {}},
		MaybeGrid: [][]OuterMaybeGridUnion{
			{OuterMaybeGridUnionNull{}, OuterMaybeGridUnionInt{Value: 2}},
			nil,
		},
		ByKey: map[string][]int32{"k": {1}, "empty": nil},
	}
}

func deeper() Deeper {
	return Deeper{
		Cube:       [][][]int64{{{1, -1}, nil}, {{7}}},
		NestedMaps: map[string]map[string]string{"outer": {"inner": "value"}, "empty": {}},
		Cells:      [][]Cell{{{C: 1}, {C: 2}}, {{C: 3}}},
		Tones:      []map[string]Tone{{"lo": ToneLOW, "hi": ToneHIGH}},
		Hashes:     map[string][]Hash{"h": {{0xab, 0xcd}, {0x01, 0x02}}},
		CellsByKey: map[string]map[string]Cell{"a": {"b": {C: -4}}},
		MaybeCells: []map[string][]DeeperMaybeCellsUnion{
			{"some": {DeeperMaybeCellsUnionNull{}, DeeperMaybeCellsUnionCell{Value: Cell{C: 9}}}},
		},
		Choice:    DeeperChoiceUnionArray{Value: [][]int32{{4, 5}, {6}}},
		ChoiceMap: DeeperChoiceMapUnionMap{Value: map[string][]string{"x": {"y", "z"}}},
	}
}

type avroValue interface {
	MarshalAvroBinary(w *avro.BinaryWriter) error
	UnmarshalAvroBinary(r *avro.BinaryReader) error
}

func encode(t *testing.T, v avroValue) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := v.MarshalAvroBinary(avro.NewBinaryWriter(&buf)); err != nil {
		t.Fatalf("MarshalAvroBinary: %v", err)
	}
	return buf.Bytes()
}

func decode(t *testing.T, b []byte, into avroValue) {
	t.Helper()
	r := bytes.NewReader(b)
	if err := into.UnmarshalAvroBinary(avro.NewBinaryReader(r)); err != nil {
		t.Fatalf("UnmarshalAvroBinary: %v", err)
	}
	if r.Len() != 0 {
		t.Errorf("%d byte(s) left unread", r.Len())
	}
}

func TestEveryNestedShapeRoundTrips(t *testing.T) {
	t.Run("the shapes from the issue", func(t *testing.T) {
		in := outer()
		var out Outer
		decode(t, encode(t, &in), &out)
		if !reflect.DeepEqual(out, in) {
			t.Errorf("decoded %+v, want %+v", out, in)
		}
	})

	t.Run("deeper shapes and every innermost kind", func(t *testing.T) {
		in := deeper()
		var out Deeper
		decode(t, encode(t, &in), &out)
		if !reflect.DeepEqual(out, in) {
			t.Errorf("decoded %+v, want %+v", out, in)
		}
	})
}

// TestNestedContainersAreEncodedAsAvroSpecifies checks the bytes, field by
// field, because a round trip alone would not catch a marshal and an unmarshal
// that were wrong the same way. Each map holds one key, so its encoding does
// not depend on iteration order.
func TestNestedContainersAreEncodedAsAvroSpecifies(t *testing.T) {
	in := Outer{
		Grid:      [][]int32{{1, 2}, nil, {3}},
		Rows:      []map[string]int32{{"a": 1}},
		MaybeGrid: [][]OuterMaybeGridUnion{{OuterMaybeGridUnionNull{}, OuterMaybeGridUnionInt{Value: 2}}},
		ByKey:     map[string][]int32{"k": {1}},
	}

	want := []byte{
		0x06,             // grid: a block of 3
		0x04, 0x02, 0x04, //   a block of 2: 1, 2
		0x00,       //   end
		0x00,       //   an empty array: end at once
		0x02, 0x06, //   a block of 1: 3
		0x00,      //   end
		0x00,      // grid: end
		0x02,      // rows: a block of 1
		0x02,      //   a block of 1
		0x02, 'a', //     key "a"
		0x02,       //     1
		0x00,       //   end
		0x00,       // rows: end
		0x02,       // maybeGrid: a block of 1
		0x04,       //   a block of 2
		0x00,       //     null
		0x02, 0x04, //     branch 1, int 2
		0x00,      //   end
		0x00,      // maybeGrid: end
		0x02,      // byKey: a block of 1
		0x02, 'k', //   key "k"
		0x02, 0x02, //   a block of 1: 1
		0x00, //   end
		0x00, // byKey: end
	}
	if got := encode(t, &in); !bytes.Equal(got, want) {
		t.Errorf("encoded % x, want % x", got, want)
	}
}

// TestANestedBlockCarryingItsSizeIsRead decodes bytes another writer could
// have produced: Avro lets any block carry a negative count followed by its
// size in bytes, and that applies to a nested container's blocks as much as to
// the outermost one's. Grid's inner array below is written that way.
func TestANestedBlockCarryingItsSizeIsRead(t *testing.T) {
	b := []byte{
		0x02,       // grid: a block of 1
		0x03, 0x04, //   a block of -2, 2 bytes long
		0x02, 0x04, //     1, 2
		0x00, //   end
		0x00, // grid: end
		0x00, // rows: end
		0x00, // maybeGrid: end
		0x00, // byKey: end
	}

	var out Outer
	decode(t, b, &out)
	if want := [][]int32{{1, 2}}; !reflect.DeepEqual(out.Grid, want) {
		t.Errorf("decoded grid %v, want %v", out.Grid, want)
	}
}
