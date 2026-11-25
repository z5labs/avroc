// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// This file is not part of this repository's build: Go ignores testdata. It is
// copied beside the code avroc-gen-go generates for every descriptor
// testGeneratedPackage builds, with its package clause rewritten to that
// package's, so that each of those packages' own test file can hold the values
// it already builds to #249's invariants.
package directencoding

import (
	"bytes"
	"reflect"
	"testing"

	avro "github.com/z5labs/avro-go"
)

// directEncoder is everything #249 gives a type this generator writes
// MarshalAvroBinary for.
type directEncoder interface {
	MarshalAvroBinary(w *avro.BinaryWriter) error
	SizeAvro() int
	AppendAvro(dst []byte) []byte
}

// checkDirectEncoding holds v to #249's invariants:
//
//   - SizeAvro() == len(AppendAvro(nil));
//   - AppendAvro(nil) is MarshalAvroBinary's bytes — or, for a value holding a
//     map of more than one entry, which two encodings can order differently,
//     bytes that decode to the same value;
//   - AppendAvro(dst) leaves dst[:len(dst)] as it was and extends it by exactly
//     SizeAvro() bytes, with and without the room to do it in place;
//   - SizeAvro makes no allocation, and nor does AppendAvro into a dst that
//     already has the room.
//
// fresh returns a zero value to decode into, and is only called for a value
// holding such a map. It is nil for a value with no UnmarshalAvroBinary of its
// own, a union branch.
func checkDirectEncoding(t *testing.T, v directEncoder, fresh func() avro.BinaryUnmarshaler) {
	t.Helper()

	var buf bytes.Buffer
	if err := v.MarshalAvroBinary(avro.NewBinaryWriter(&buf)); err != nil {
		t.Fatalf("MarshalAvroBinary: %v", err)
	}
	marshaled := buf.Bytes()

	appended := v.AppendAvro(nil)
	size := v.SizeAvro()
	if size != len(appended) {
		t.Errorf("SizeAvro() = %d, but AppendAvro(nil) appended %d bytes", size, len(appended))
	}

	unordered := holdsAMapOfMoreThanOneEntry(reflect.ValueOf(v))
	if !bytes.Equal(appended, marshaled) {
		switch {
		case !unordered:
			t.Errorf("AppendAvro(nil) = % x, MarshalAvroBinary wrote % x", appended, marshaled)
		case fresh == nil:
			t.Errorf("AppendAvro(nil) and MarshalAvroBinary disagree, and there is nothing to decode them into to compare")
		default:
			fromAppend, fromMarshal := fresh(), fresh()
			decodeAll(t, appended, fromAppend)
			decodeAll(t, marshaled, fromMarshal)
			if !reflect.DeepEqual(fromAppend, fromMarshal) {
				t.Errorf("AppendAvro(nil) decodes to %+v, MarshalAvroBinary's bytes to %+v", fromAppend, fromMarshal)
			}
		}
	}

	prefix := []byte("prefix")
	for _, spare := range []int{0, size} {
		dst := make([]byte, len(prefix), len(prefix)+spare)
		copy(dst, prefix)

		got := v.AppendAvro(dst)
		if !bytes.Equal(dst, prefix) {
			t.Errorf("with %d bytes spare, AppendAvro changed dst[:len(dst)] to % x", spare, dst)
		}
		if !bytes.Equal(got[:len(prefix)], prefix) {
			t.Errorf("with %d bytes spare, AppendAvro returned % x, which does not begin with dst", spare, got)
		}
		if len(got) != len(prefix)+size {
			t.Errorf("with %d bytes spare, AppendAvro extended dst by %d bytes, want SizeAvro() = %d", spare, len(got)-len(prefix), size)
		}
		if !unordered && !bytes.Equal(got[len(prefix):], appended) {
			t.Errorf("with %d bytes spare, AppendAvro appended % x, want % x", spare, got[len(prefix):], appended)
		}
		if spare > 0 && &got[0] != &dst[0] {
			t.Errorf("with %d bytes spare, AppendAvro reallocated rather than appending in place", spare)
		}
	}

	var n int
	if allocs := testing.AllocsPerRun(100, func() { n = v.SizeAvro() }); allocs != 0 {
		t.Errorf("SizeAvro made %v allocations per run, want 0", allocs)
	}
	_ = n

	room := make([]byte, 0, size)
	if allocs := testing.AllocsPerRun(100, func() { room = v.AppendAvro(room[:0]) }); allocs != 0 {
		t.Errorf("AppendAvro into a dst with room made %v allocations per run, want 0", allocs)
	}
}

func decodeAll(t *testing.T, b []byte, into avro.BinaryUnmarshaler) {
	t.Helper()
	r := bytes.NewReader(b)
	if err := into.UnmarshalAvroBinary(avro.NewBinaryReader(r)); err != nil {
		t.Fatalf("UnmarshalAvroBinary: %v", err)
	}
	if r.Len() != 0 {
		t.Errorf("%d byte(s) left unread", r.Len())
	}
}

// holdsAMapOfMoreThanOneEntry reports whether a map of more than one entry is
// reachable from v, which is the one case two encodings of a value may order
// differently.
func holdsAMapOfMoreThanOneEntry(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		return !v.IsNil() && holdsAMapOfMoreThanOneEntry(v.Elem())
	case reflect.Struct:
		for i := range v.NumField() {
			if holdsAMapOfMoreThanOneEntry(v.Field(i)) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			if holdsAMapOfMoreThanOneEntry(v.Index(i)) {
				return true
			}
		}
	case reflect.Map:
		if v.Len() > 1 {
			return true
		}
		for it := v.MapRange(); it.Next(); {
			if holdsAMapOfMoreThanOneEntry(it.Value()) {
				return true
			}
		}
	}
	return false
}
