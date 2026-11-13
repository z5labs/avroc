// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// This file is not part of this repository's build: Go ignores testdata. It is
// copied beside the code avroc-gen-go generates for unionHolderSchema, in a
// module of its own, by TestUnionBranchDefinitionsCompileAndRoundTrip, and it is
// what that test runs there.
package holder

import (
	"bytes"
	"reflect"
	"testing"

	avro "github.com/z5labs/avro-go"
)

// base holds the null branch of every union and empty collections, so each
// case below changes exactly one field. Every union field has to hold a
// branch: a nil union interface has no MarshalAvroBinary to call. The maps are
// empty rather than nil because a decoded map is always made, and the arrays
// are nil because a decoded empty array is never appended to.
func base() Holder {
	return Holder{
		Id:     7,
		Tone:   HolderToneUnionNull{},
		Hash:   HolderHashUnionNull{},
		Wrap:   HolderWrapUnionNull{},
		Keyed:  map[string]HolderKeyedUnion{},
		Values: map[string]MapVal{},
		Listed: HolderListedUnionNull{},
		Mapped: HolderMappedUnionNull{},
	}
}

func TestEveryBranchOfEveryUnionRoundTrips(t *testing.T) {
	testCases := []struct {
		name string
		set  func(*Holder)
	}{
		{name: "every null branch", set: func(*Holder) {}},
		{name: "an enum branch", set: func(h *Holder) { h.Tone = HolderToneUnionTone{Value: ToneHIGH} }},
		{name: "a fixed branch", set: func(h *Holder) { h.Hash = HolderHashUnionHash{Value: Hash{1, 2, 3, 4}} }},
		{name: "a record branch holding a null union", set: func(h *Holder) {
			h.Wrap = HolderWrapUnionWrapper{Value: Wrapper{Deep: WrapperDeepUnionNull{}}}
		}},
		{name: "a record branch holding a record branch", set: func(h *Holder) {
			h.Wrap = HolderWrapUnionWrapper{Value: Wrapper{Deep: WrapperDeepUnionDeep{Value: Deep{D: "deep"}}}}
		}},
		{name: "a union as an array's items", set: func(h *Holder) {
			h.Items = []HolderItemsUnion{HolderItemsUnionNull{}, HolderItemsUnionInArr{Value: InArr{A: -5}}, HolderItemsUnionNull{}}
		}},
		{name: "a union as a map's values", set: func(h *Holder) {
			h.Keyed = map[string]HolderKeyedUnion{"none": HolderKeyedUnionNull{}, "some": HolderKeyedUnionInMap{Value: InMap{M: 9}}}
		}},
		{name: "a record as a map's values", set: func(h *Holder) {
			h.Values = map[string]MapVal{"a": {V: 1}, "b": {V: -1}}
		}},
		{name: "an array in a union branch", set: func(h *Holder) {
			h.Listed = HolderListedUnionArray{Value: []InList{InListB, InListA}}
		}},
		{name: "a map in a union branch", set: func(h *Holder) {
			h.Mapped = HolderMappedUnionMap{Value: map[string]InUMap{"k": {0xab, 0xcd}}}
		}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			tc.set(&in)

			var buf bytes.Buffer
			if err := in.MarshalAvroBinary(avro.NewBinaryWriter(&buf)); err != nil {
				t.Fatalf("MarshalAvroBinary: %v", err)
			}

			var out Holder
			r := bytes.NewReader(buf.Bytes())
			if err := out.UnmarshalAvroBinary(avro.NewBinaryReader(r)); err != nil {
				t.Fatalf("UnmarshalAvroBinary: %v", err)
			}
			if r.Len() != 0 {
				t.Errorf("%d byte(s) left unread", r.Len())
			}
			if !reflect.DeepEqual(out, in) {
				t.Errorf("decoded %+v, want %+v", out, in)
			}
		})
	}
}

// TestAUnionAsAnArraysItemsIsEncodedAsAvroSpecifies checks the bytes for the
// one position that had no encoding at all before: an array whose items were a
// union wrote its count and then nothing for each item. A round trip alone
// would not catch a marshal and an unmarshal that were wrong the same way.
func TestAUnionAsAnArraysItemsIsEncodedAsAvroSpecifies(t *testing.T) {
	in := base()
	in.Items = []HolderItemsUnion{HolderItemsUnionNull{}, HolderItemsUnionInArr{Value: InArr{A: 1}}}

	var buf bytes.Buffer
	if err := in.MarshalAvroBinary(avro.NewBinaryWriter(&buf)); err != nil {
		t.Fatalf("MarshalAvroBinary: %v", err)
	}

	want := []byte{
		0x0e,       // id 7
		0x00,       // tone: null
		0x00,       // hash: null
		0x00,       // wrap: null
		0x04,       // items: a block of 2
		0x00,       //   null
		0x02, 0x02, //   branch 1, InArr{A: 1}
		0x00, // items: end
		0x00, // keyed: empty
		0x00, // values: empty
		0x00, // listed: null
		0x00, // mapped: null
	}
	if got := buf.Bytes(); !bytes.Equal(got, want) {
		t.Errorf("encoded % x, want % x", got, want)
	}
}
