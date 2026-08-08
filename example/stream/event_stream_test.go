// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// The behaviour of the streaming reader avroc-gen-go emits for an array-rooted
// schema, exercised against the committed generated code rather than against a
// string of it.
//
// It lives here, in the generator's own output directory, because that is the
// only place the generated code exists as code: everything in
// internal/avroc-gen-go is assertions about source text, which can say that
// StreamEvents was written and cannot say that it reads a stream. avroc never
// removes a file it has no record of generating, so a hand-written test beside
// event.go survives every regeneration; example/README.md says the same about
// the JSON generator's output directory.
package stream

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"runtime"
	"testing"

	avro "github.com/z5labs/avro-go"
)

// event is the i'th item of every stream below, so a decoded item can be
// checked against the position it came back at.
func event(i int) *Event {
	return &Event{Id: int64(i), Value: float64(i) / 2, Ok: i%2 == 0}
}

// encodeEvents returns n items encoded as one complete Avro array.
func encodeEvents(t *testing.T, n int, opts ...avro.ArrayWriterOption) []byte {
	t.Helper()

	var buf bytes.Buffer
	w := avro.NewBinaryWriter(&buf)
	err := avro.WriteArray(w, func(a *avro.ArrayWriter) error {
		for i := range n {
			if err := a.Write(event(i)); err != nil {
				return err
			}
		}
		return nil
	}, opts...)
	if err != nil {
		t.Fatalf("failed to encode %d event(s): %v", n, err)
	}
	return buf.Bytes()
}

// pipeBufferSize is the only buffer anything in this file allocates for the
// stream itself. Every test that cares about not materialising the array writes
// far more than this through it, so the bytes cannot all have been in memory at
// once on either side of the pipe.
const pipeBufferSize = 4 << 10

// pipeEvents encodes n items on a goroutine and returns the reading end of a
// pipe they are written into, so the stream is produced as it is consumed and
// never exists in full anywhere.
func pipeEvents(t *testing.T, n int, opts ...avro.ArrayWriterOption) io.Reader {
	t.Helper()

	pr, pw := io.Pipe()
	go func() {
		buffered := bufio.NewWriterSize(pw, pipeBufferSize)
		w := avro.NewBinaryWriter(buffered)
		err := avro.WriteArray(w, func(a *avro.ArrayWriter) error {
			for i := range n {
				if err := a.Write(event(i)); err != nil {
					return err
				}
			}
			return nil
		}, opts...)
		if err == nil {
			err = buffered.Flush()
		}
		// The reading end sees this as the end of the stream, or as the error
		// that stopped it; either way it is never left blocked.
		_ = pw.CloseWithError(err)
	}()
	return pr
}

// countingReader reports how many bytes were actually pulled through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// blockShapes are the two ways a writer may frame an array: one block per item
// with no size prefix, and batched blocks whose negated count is followed by
// the block's encoded size. A reader has to handle both, because emitting the
// size is the writer's choice.
var blockShapes = []struct {
	name  string
	opts  []avro.ArrayWriterOption
	sized bool
}{
	{name: "unsized blocks", opts: nil, sized: false},
	{name: "sized blocks", opts: []avro.ArrayWriterOption{avro.WithSizedBlocks(1 << 10)}, sized: true},
}

// TestStreamEventsYieldsEveryItemInOrder is the ordinary way to consume a
// stream, over both block shapes and over an array long enough to span many
// blocks of either.
func TestStreamEventsYieldsEveryItemInOrder(t *testing.T) {
	const items = 5_000

	for _, shape := range blockShapes {
		t.Run(shape.name, func(t *testing.T) {
			r := &countingReader{r: pipeEvents(t, items, shape.opts...)}

			got := 0
			for v, err := range StreamEvents(r) {
				if err != nil {
					t.Fatalf("item %d: %v", got, err)
				}
				if want := event(got); *v != *want {
					t.Fatalf("item %d is %+v, want %+v", got, *v, *want)
				}
				got++
			}
			if got != items {
				t.Errorf("read %d item(s), want %d", got, items)
			}
			if r.n <= pipeBufferSize {
				t.Errorf("only %d byte(s) crossed the pipe, which is not more than the %d byte buffer: the stream is too small to say anything about materialising it", r.n, pipeBufferSize)
			}
		})
	}
}

// TestStreamEventsOnAnEmptyArray covers the array that is present and holds
// nothing, which is a terminating block and no items — not an absent stream and
// not an error.
func TestStreamEventsOnAnEmptyArray(t *testing.T) {
	for _, shape := range blockShapes {
		t.Run(shape.name, func(t *testing.T) {
			for v, err := range StreamEvents(bytes.NewReader(encodeEvents(t, 0, shape.opts...))) {
				t.Fatalf("an empty array yielded %+v, %v", v, err)
			}
		})
	}
}

// TestStreamEventsOnATruncatedStream is the case a short read would silently
// turn into a complete-looking one: iteration has to end in an error rather
// than in the items that happened to arrive.
func TestStreamEventsOnATruncatedStream(t *testing.T) {
	const items = 64

	// An array whose terminating block never arrives — what an ArrayWriter that
	// was never closed leaves behind.
	unterminated := func() []byte {
		var buf bytes.Buffer
		w := avro.NewBinaryWriter(&buf)
		a := avro.NewArrayWriter(w)
		for i := range items {
			if err := a.Write(event(i)); err != nil {
				t.Fatalf("failed to encode event %d: %v", i, err)
			}
		}
		return buf.Bytes()
	}()

	complete := encodeEvents(t, items)

	testCases := []struct {
		name string
		data []byte
		want error
		// maxItems is what the stream may yield before it fails. A stream cut
		// short must not deliver the whole array; one that delivered every item
		// and then ran out before its terminating block legitimately does.
		maxItems int
	}{
		{
			// Three bytes in is inside the first item's fields, so the reader is
			// part way through a value rather than between two of them: a clean
			// end of input there is a short read and is reported as one.
			name:     "cut inside an item",
			data:     complete[:3],
			want:     io.ErrUnexpectedEOF,
			maxItems: 0,
		},
		{
			// Halfway through an array framed one block per item, which is a
			// block boundary: the count of the next block is simply missing.
			name:     "cut at a block boundary",
			data:     complete[:len(complete)/2],
			want:     avro.ErrTruncatedArray,
			maxItems: items - 1,
		},
		{
			// Every item arrived and the terminating block never did, which is
			// what an ArrayWriter that was never closed leaves behind. It is a
			// truncated array and not an empty tail, and telling those apart is
			// the whole point of the terminator.
			name:     "never terminated",
			data:     unterminated,
			want:     avro.ErrTruncatedArray,
			maxItems: items,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var last error
			read := 0
			for _, err := range StreamEvents(bytes.NewReader(tc.data)) {
				last = err
				if err != nil {
					break
				}
				read++
			}
			if last == nil {
				t.Fatalf("a truncated stream ended cleanly after %d item(s)", read)
			}
			if !errors.Is(last, tc.want) {
				t.Errorf("error is %v, want one wrapping %v", last, tc.want)
			}
			if read > tc.maxItems {
				t.Errorf("a truncated stream yielded %d item(s), want at most %d", read, tc.maxItems)
			}
		})
	}
}

// TestTheReaderSkipsASizedBlockWithoutDecodingIt is the half of "parse only
// what you want" that is more than a phrase: a block that declared its encoded
// size is discarded straight from the underlying reader, and a block that did
// not cannot be, because a record carries no length of its own.
func TestTheReaderSkipsASizedBlockWithoutDecodingIt(t *testing.T) {
	const items = 1_000

	for _, shape := range blockShapes {
		t.Run(shape.name, func(t *testing.T) {
			r := NewEventReader(bytes.NewReader(encodeEvents(t, items, shape.opts...)))

			// The first item is decoded so the reader is inside a block, and the
			// rest of that block is then thrown away.
			var v Event
			ok, err := r.Next(&v)
			if err != nil || !ok {
				t.Fatalf("first item: ok=%t err=%v", ok, err)
			}

			skip, err := r.SkipBlock()
			if err != nil {
				t.Fatalf("SkipBlock: %v", err)
			}
			want := avro.SkipUnsized
			if shape.sized {
				want = avro.SkipSized
			}
			if skip != want {
				t.Fatalf("SkipBlock reported %v, want %v", skip, want)
			}

			// Whatever it reported, the stream is still readable to its end.
			decoded := 1
			for {
				ok, err := r.Next(&v)
				if err != nil {
					t.Fatalf("after skipping, item %d: %v", decoded, err)
				}
				if !ok {
					break
				}
				decoded++
			}

			switch {
			case shape.sized && decoded >= items:
				t.Errorf("skipping a sized block still decoded all %d item(s)", decoded)
			case !shape.sized && decoded != items:
				t.Errorf("skipping an unsized block consumed items: decoded %d, want %d", decoded, items)
			}
		})
	}
}

// TestThePeakAllocationDoesNotGrowWithTheItemCount is the property the whole
// story is for. The reader holds no item state of its own, so what is live at
// the last item of a hundred thousand is what is live at the last item of a
// thousand — an array read this way is never in memory at all, whether the
// caller reuses one destination or lets the iterator allocate each item and
// drop it again.
func TestThePeakAllocationDoesNotGrowWithTheItemCount(t *testing.T) {
	if testing.Short() {
		t.Skip("streams a hundred thousand items")
	}

	const (
		small = 1_000
		large = 100_000

		// Generous next to the several megabytes a materialised array of large
		// would hold, and generous enough that a garbage collector which simply
		// had not got round to something is not a failure.
		tolerance = 1 << 20
	)

	testCases := []struct {
		name string
		peak func(t *testing.T, n int) uint64
	}{
		{name: "reusing one destination", peak: peakReusingOneDestination},
		{name: "over the iterator", peak: peakOverTheIterator},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			atSmall := tc.peak(t, small)
			atLarge := tc.peak(t, large)

			if atLarge > atSmall+tolerance {
				t.Errorf("live heap at the last of %d items is %d bytes, against %d bytes at the last of %d: it grew by %d, more than the %d tolerated",
					large, atLarge, atSmall, small, atLarge-atSmall, tolerance)
			}
		})
	}
}

// peakReusingOneDestination streams n items into a single Event and returns the
// live heap measured at the last of them.
func peakReusingOneDestination(t *testing.T, n int) uint64 {
	t.Helper()

	r := NewEventReader(pipeEvents(t, n))

	var v Event
	var peak uint64
	read := 0
	for {
		ok, err := r.Next(&v)
		if err != nil {
			t.Fatalf("item %d: %v", read, err)
		}
		if !ok {
			break
		}
		read++
		if read == n {
			peak = liveHeap()
		}
	}
	if read != n {
		t.Fatalf("read %d item(s), want %d", read, n)
	}
	runtime.KeepAlive(r)
	return peak
}

// peakOverTheIterator streams n items through All, which allocates each of them
// and keeps none, and returns the live heap measured at the last of them.
func peakOverTheIterator(t *testing.T, n int) uint64 {
	t.Helper()

	r := NewEventReader(pipeEvents(t, n))

	var peak uint64
	read := 0
	for v, err := range r.All() {
		if err != nil {
			t.Fatalf("item %d: %v", read, err)
		}
		read++
		if read == n {
			peak = liveHeap()
			runtime.KeepAlive(v)
		}
	}
	if read != n {
		t.Fatalf("read %d item(s), want %d", read, n)
	}
	runtime.KeepAlive(r)
	return peak
}

// liveHeap returns the bytes of reachable heap, which is what "peak allocation"
// has to mean for a reader that allocates a destination per item and drops it
// again: the total allocated necessarily grows with the item count, and the
// question is whether anything is being kept. Collected twice because the first
// pass can leave finalisable objects behind.
func liveHeap() uint64 {
	runtime.GC()
	runtime.GC()

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}
