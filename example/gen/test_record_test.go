// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// A round trip of the record avroc-gen-go emits for schema.avdl, against the
// committed generated code. Its fixed field is written inline rather than
// through MD5's own method (#245), and its fixed union branch still goes
// through the method, so the bytes are pinned as well as the round trip: the
// change was to how a fixed is written and not to what. It lives beside the
// generated code for the reason stream/event_stream_test.go does: avroc never
// removes a file it has no record of generating.
package avro

import (
	"bytes"
	"reflect"
	"testing"

	avro "github.com/z5labs/avro-go"
)

func TestTestRecordRoundTripsItsFixedValues(t *testing.T) {
	hash := MD5{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	other := MD5{15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1, 0}
	in := TestRecord{
		Name:         "ab",
		Kind:         KindBAZ,
		Hash:         hash,
		NullableHash: TestRecordNullableHashUnionMD5{Value: other},
	}

	var buf bytes.Buffer
	if err := in.MarshalAvroBinary(avro.NewBinaryWriter(&buf)); err != nil {
		t.Fatalf("MarshalAvroBinary: %v", err)
	}

	var want []byte
	want = append(want, 0x04, 'a', 'b') // name: length 2 (zigzag), then its bytes
	want = append(want, 0x04)           // kind: BAZ is symbol 2 (zigzag)
	want = append(want, hash[:]...)     // hash: sixteen bytes, no length
	want = append(want, 0x02)           // nullableHash: branch 1 (zigzag)
	want = append(want, other[:]...)    // nullableHash: the MD5
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("encoded %x, want %x", buf.Bytes(), want)
	}

	var out TestRecord
	if err := out.UnmarshalAvroBinary(avro.NewBinaryReader(bytes.NewReader(buf.Bytes()))); err != nil {
		t.Fatalf("UnmarshalAvroBinary: %v", err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Errorf("round trip gave %+v, want %+v", out, in)
	}
}
