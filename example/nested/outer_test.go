// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// Round trips of the types avroc-gen-go emits for a schema whose arrays hold
// containers of their own (#237), against the committed generated code rather
// than against a string of it.
//
// Before #237 the generator wrote each nested container's block count and then
// nothing for its items. The package failed to compile only because the empty
// marshal loop left its index unused; had it compiled, every nested container
// would have been encoded as empty. It lives beside the generated code for the
// reason stream/event_stream_test.go does: avroc never removes a file it has no
// record of generating.
package nested

import (
	"bytes"
	"reflect"
	"testing"

	avro "github.com/z5labs/avro-go"
)

// TestOuterRoundTripsItsNestedContainers encodes an Outer whose every field
// holds non-empty containers, compares the bytes against the Avro binary
// encoding written out by hand, and decodes them back.
//
// The bytes are checked as well as the round trip because a marshal and an
// unmarshal that were wrong in the same way would agree with each other. Each
// map holds one key, so its encoding does not depend on iteration order. The
// empty inner array is nil because a decoded array is only ever appended to.
func TestOuterRoundTripsItsNestedContainers(t *testing.T) {
	in := Outer{
		Grid: [][]int32{{1, 2}, nil, {3}},
		Rows: []map[string]int32{{"a": 1}},
		MaybeGrid: [][]OuterMaybeGridUnion{
			{OuterMaybeGridUnionNull{}, OuterMaybeGridUnionInt{Value: 2}},
		},
	}

	// Every array and map is a sequence of blocks, each a zig-zag long count
	// followed by that many items, ending with a block of zero; a nested one
	// is exactly that, written as its enclosing container's item.
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
		0x00, //   end
		0x00, // maybeGrid: end
	}

	var buf bytes.Buffer
	if err := in.MarshalAvroBinary(avro.NewBinaryWriter(&buf)); err != nil {
		t.Fatalf("MarshalAvroBinary: %v", err)
	}
	if got := buf.Bytes(); !bytes.Equal(got, want) {
		t.Errorf("encoded % x, want % x", got, want)
	}

	var out Outer
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
}
