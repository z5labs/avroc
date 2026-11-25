// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// This file is not part of this repository's build: Go ignores testdata. It is
// copied beside the code avroc-gen-go generates for constantSizeSchema, in a
// module of its own, by TestContainersOfConstantSizeValuesCompileAndAreSized,
// and it is what that test runs there.
package constantsize

import (
	"bytes"
	"testing"

	avro "github.com/z5labs/avro-go"
)

func readings() *Readings {
	return &Readings{
		Flags:       []bool{true, false, true},
		Ratios:      []float32{0.5, -1.25},
		Weights:     []float64{3.5},
		WeightByKey: map[string]float64{"a": 1, "bb": 2, "ccc": 3},
		FlagByKey:   map[string]bool{"on": true, "off": false},
		FlagGrid:    [][]bool{{true}, nil, {false, true}},
		RatioRows:   []map[string]float32{{"x": 1, "yy": 2}, {}},
		MaybeRatios: ReadingsMaybeRatiosUnionArray{Value: []float32{9, 8, 7}},
	}
}

func TestEveryContainerOfConstantSizeValuesIsSizedAndAppended(t *testing.T) {
	testCases := []struct {
		name  string
		v     directEncoder
		fresh func() avro.BinaryUnmarshaler
	}{
		{name: "Readings", v: readings(), fresh: func() avro.BinaryUnmarshaler { return &Readings{} }},
		{name: "empty Readings", v: &Readings{MaybeRatios: ReadingsMaybeRatiosUnionNull{}},
			fresh: func() avro.BinaryUnmarshaler { return &Readings{} }},
		{name: "the array branch", v: ReadingsMaybeRatiosUnionArray{Value: []float32{1, 2}}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			checkDirectEncoding(t, tc.v, tc.fresh)
		})
	}
}

// TestTheBytesAreAvrosBinaryEncoding pins what is appended for one value of
// each single-key shape, so that sizing by multiplying is checked against the
// encoding itself and not only against the method beside it.
func TestTheBytesAreAvrosBinaryEncoding(t *testing.T) {
	in := &Readings{
		Flags:       []bool{true, false},
		Weights:     []float64{0},
		WeightByKey: map[string]float64{"k": 0},
		FlagGrid:    [][]bool{{true}},
		MaybeRatios: ReadingsMaybeRatiosUnionNull{},
	}
	want := []byte{
		0x04, 0x01, 0x00, // flags: a block of 2, true, false
		0x00,                   // flags: end
		0x00,                   // ratios: empty
		0x02,                   // weights: a block of 1
		0, 0, 0, 0, 0, 0, 0, 0, //   0.0
		0x00,      // weights: end
		0x02,      // weightByKey: a block of 1
		0x02, 'k', //   key "k"
		0, 0, 0, 0, 0, 0, 0, 0, //   0.0
		0x00,       // weightByKey: end
		0x00,       // flagByKey: empty
		0x02,       // flagGrid: a block of 1
		0x02, 0x01, //   a block of 1: true
		0x00, //   end
		0x00, // flagGrid: end
		0x00, // ratioRows: empty
		0x00, // maybeRatios: null
	}
	if got := in.AppendAvro(nil); !bytes.Equal(got, want) {
		t.Errorf("AppendAvro(nil) = % x, want % x", got, want)
	}
	if got := in.SizeAvro(); got != len(want) {
		t.Errorf("SizeAvro() = %d, want %d", got, len(want))
	}
}
