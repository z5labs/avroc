// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// The behaviour of the streaming writer avroc-gen-go emits for an array-rooted
// schema, exercised against the committed generated code for the reason
// event_stream_test.go gives: everything in internal/avroc-gen-go is assertions
// about source text, which can say that WriteEvents was written and cannot say
// that it produces a readable array one item at a time.
//
// It is a separate file from the reader's because the two halves are checked
// against each other here: the streams below are produced by the generated
// writer and consumed by the generated reader, where the reader's own tests
// deliberately encode their input with avro-go directly so that a bug in the
// writer cannot make a reader test pass.
package stream

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"runtime"
	"strconv"
	"testing"

	avro "github.com/z5labs/avro-go"
)

// writeEvents returns n items encoded as one complete Avro array by the
// generated writer.
func writeEvents(t *testing.T, n int, opts ...avro.ArrayWriterOption) []byte {
	t.Helper()

	var buf bytes.Buffer
	err := WriteEvents(&buf, func(s *EventWriter) error {
		for i := range n {
			if err := s.Write(event(i)); err != nil {
				return err
			}
		}
		return nil
	}, opts...)
	if err != nil {
		t.Fatalf("failed to write %d event(s): %v", n, err)
	}
	return buf.Bytes()
}

// countingWriter reports how many bytes reached it and in how many calls, which
// is what "buffers nothing" and "buffers up to a bound" are statements about.
type countingWriter struct {
	n     int64
	calls int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	c.calls++
	return len(p), nil
}

// blocksIn returns the number of blocks the array in data is framed as, and
// fails if any of them declared no size. Counting them is only possible because
// they did: SkipBlock consumes a sized block whole and consumes nothing at all
// from an unsized one.
func blocksIn(t *testing.T, data []byte) int {
	t.Helper()

	r := NewEventReader(bytes.NewReader(data))
	for n := 0; ; n++ {
		skip, err := r.SkipBlock()
		if err != nil {
			t.Fatalf("block %d: %v", n, err)
		}
		switch skip {
		case avro.SkipNone:
			return n
		case avro.SkipUnsized:
			t.Fatalf("block %d declared no size", n)
		}
	}
}

// TestWriteEventsRoundTripsThroughStreamEvents is the pair working: what the
// generated writer produced is what the generated reader hands back, in both
// block shapes and including the array that holds nothing.
func TestWriteEventsRoundTripsThroughStreamEvents(t *testing.T) {
	for _, shape := range blockShapes {
		t.Run(shape.name, func(t *testing.T) {
			for _, items := range []int{0, 1, 2, 5_000} {
				t.Run(itemCount(items), func(t *testing.T) {
					got := 0
					for v, err := range StreamEvents(bytes.NewReader(writeEvents(t, items, shape.opts...))) {
						if err != nil {
							t.Fatalf("item %d: %v", got, err)
						}
						if want := event(got); *v != *want {
							t.Fatalf("item %d is %+v, want %+v", got, *v, *want)
						}
						got++
					}
					if got != items {
						t.Errorf("read back %d item(s), want %d", got, items)
					}
				})
			}
		})
	}
}

func itemCount(n int) string {
	switch n {
	case 0:
		return "empty"
	case 1:
		return "one item"
	}
	return strconv.Itoa(n) + " items"
}

// TestTheWriterEncodesWhatAvroGoEncodes is what "a thin wrapper" means in
// bytes. The reader's tests encode their input with avro-go directly; if the
// generated writer produced anything else, the two halves of this package would
// be checked against different encodings of the same array.
func TestTheWriterEncodesWhatAvroGoEncodes(t *testing.T) {
	const items = 1_000

	for _, shape := range blockShapes {
		t.Run(shape.name, func(t *testing.T) {
			got := writeEvents(t, items, shape.opts...)
			want := encodeEvents(t, items, shape.opts...)
			if !bytes.Equal(got, want) {
				t.Errorf("the generated writer produced %d byte(s), avro-go produced %d, and they differ", len(got), len(want))
			}
		})
	}
}

// TestUnsizedBlocksBufferNothingAndSizedBlocksAreBounded is the one design
// decision the story turned on. A block that declares its encoded size can only
// do so once that size is known, which means holding the block; a block that
// declares no size can go straight out. So the trade is the caller's, the
// default is the one that costs nothing, and the bound on the other is a number
// the caller chose.
func TestUnsizedBlocksBufferNothingAndSizedBlocksAreBounded(t *testing.T) {
	const items = 10_000

	t.Run("unsized blocks buffer nothing", func(t *testing.T) {
		w := &countingWriter{}
		s := NewEventWriter(w)

		if err := s.Write(event(0)); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if w.n == 0 {
			t.Errorf("the first item was buffered: nothing reached the underlying writer")
		}
	})

	t.Run("sized blocks buffer up to the bound", func(t *testing.T) {
		// Generous next to a block header and one encoded Event, and far
		// smaller than the bound, so neither assertion below is really about
		// the size of an item.
		const (
			limit = 1 << 12
			slack = 64
		)

		w := &countingWriter{}
		s := NewEventWriter(w, avro.WithSizedBlocks(limit))

		if err := s.Write(event(0)); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if w.n != 0 {
			t.Errorf("%d byte(s) reached the underlying writer before the block's size could be known", w.n)
		}

		written := 1
		for ; w.n == 0 && written < items; written++ {
			if err := s.Write(event(written)); err != nil {
				t.Fatalf("Write: %v", err)
			}
		}
		if w.n == 0 {
			t.Fatalf("%d item(s) went in and the %d byte bound never flushed a block", written, limit)
		}
		if w.n > limit+slack {
			t.Errorf("the first flushed block is %d bytes, more than the %d byte bound (+%d): the buffer is not bounded by the option", w.n, limit, slack)
		}
	})

	t.Run("the bound is the caller's", func(t *testing.T) {
		const (
			tight = 1 << 8
			loose = 1 << 12
		)

		blocksTight := blocksIn(t, writeEvents(t, items, avro.WithSizedBlocks(tight)))
		blocksLoose := blocksIn(t, writeEvents(t, items, avro.WithSizedBlocks(loose)))
		if blocksTight <= blocksLoose {
			t.Errorf("a %d byte bound framed %d block(s) and a %d byte one framed %d: the bound is not configurable",
				tight, blocksTight, loose, blocksLoose)
		}
	})
}

// TestAStreamThatWasNeverClosedIsDetectable is why Close is left promoted from
// the embedded writer rather than hidden behind the wrapper. An Avro array is
// terminated by a zero-count block, so a writer that is never closed has not
// missed a nicety — it has produced a truncated array, and the point of the
// terminator is that the reader can say so.
func TestAStreamThatWasNeverClosedIsDetectable(t *testing.T) {
	const items = 64

	var buf bytes.Buffer
	s := NewEventWriter(&buf)
	for i := range items {
		if err := s.Write(event(i)); err != nil {
			t.Fatalf("item %d: %v", i, err)
		}
	}

	read, err := readBack(bytes.NewReader(buf.Bytes()))
	if !errors.Is(err, avro.ErrTruncatedArray) {
		t.Fatalf("an unclosed stream read back with %d item(s) and error %v, want one wrapping %v", read, err, avro.ErrTruncatedArray)
	}

	// And closing the same writer completes the array, which is what makes the
	// failure above a missing terminator rather than a corrupt stream.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	read, err = readBack(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("after closing: %v", err)
	}
	if read != items {
		t.Errorf("after closing, read back %d item(s), want %d", read, items)
	}
}

// TestWriteEventsLeavesAFailedArrayUnterminated is the same property from the
// other side: WriteEvents is the entry point that owns the close, and it closes
// only on success, because a partial array presented as a complete one is worse
// than one that reports itself truncated.
func TestWriteEventsLeavesAFailedArrayUnterminated(t *testing.T) {
	errBoom := errors.New("boom")

	var buf bytes.Buffer
	err := WriteEvents(&buf, func(s *EventWriter) error {
		if err := s.Write(event(0)); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("WriteEvents returned %v, want %v", err, errBoom)
	}

	read, err := readBack(bytes.NewReader(buf.Bytes()))
	if !errors.Is(err, avro.ErrTruncatedArray) {
		t.Errorf("a failed array read back with %d item(s) and error %v, want one wrapping %v", read, err, avro.ErrTruncatedArray)
	}
}

// readBack drains a stream and returns how many items it yielded and the error
// it ended with, if any.
func readBack(r io.Reader) (int, error) {
	read := 0
	for _, err := range StreamEvents(r) {
		if err != nil {
			return read, err
		}
		read++
	}
	return read, nil
}

// TestSizedBlocksTheWriterEmittedAreSkippableWithoutDecoding is the payoff for
// paying the buffer: a block that declared its encoded size is discarded
// straight from the underlying reader.
//
// It is asserted by counting allocations rather than by inspecting the bytes,
// because "without decoding" is a claim about what the reader did and not about
// what the writer wrote. Decoding an item over the iterator allocates it, so
// draining the array costs at least one allocation per item; skipping the same
// array costs a handful per block and none per item, and that gap is only
// possible if no item was decoded.
func TestSizedBlocksTheWriterEmittedAreSkippableWithoutDecoding(t *testing.T) {
	const (
		items = 50_000
		// Small enough that the array below is many blocks rather than one, so
		// skipping is repeated work rather than a single discard.
		blockBuffer = 1 << 12
		// Skipping allocates per block, not per item, so anything approaching
		// one allocation per fifty items is decoding.
		skipBudget = items / 50
	)

	data := writeEvents(t, items, avro.WithSizedBlocks(blockBuffer))

	var decoded int
	decoding := mallocs(func() {
		var err error
		decoded, err = readBack(bytes.NewReader(data))
		if err != nil {
			t.Errorf("decoding: %v", err)
		}
	})
	if decoded != items {
		t.Fatalf("decoded %d item(s), want %d", decoded, items)
	}
	if decoding < items {
		t.Fatalf("decoding %d items cost %d allocation(s), fewer than one per item: the comparison below would say nothing", items, decoding)
	}

	var blocks int
	skipping := mallocs(func() { blocks = blocksIn(t, data) })
	if blocks < 2 {
		t.Fatalf("the array is %d block(s), so skipping one is the whole of it", blocks)
	}
	if skipping > skipBudget {
		t.Errorf("skipping %d block(s) cost %d allocation(s), more than the %d budgeted: items are being decoded (decoding all %d costs %d)",
			blocks, skipping, skipBudget, items, decoding)
	}

	// The control: the same items framed without sizes cannot be skipped at
	// all, which is what the buffer above bought.
	r := NewEventReader(bytes.NewReader(writeEvents(t, items)))
	skip, err := r.SkipBlock()
	if err != nil {
		t.Fatalf("SkipBlock on an unsized array: %v", err)
	}
	if skip != avro.SkipUnsized {
		t.Errorf("SkipBlock on an unsized array reported %v, want %v", skip, avro.SkipUnsized)
	}
}

// mallocs returns the number of heap objects allocated while f ran.
func mallocs(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.Mallocs - before.Mallocs
}

// TestTheWritersPeakAllocationDoesNotGrowWithTheItemCount is the writer's half
// of the property the whole story is for. The items are never a slice: what is
// live as the hundred-thousandth item goes out is what was live as the
// thousandth did, in both block shapes, because the only thing either of them
// holds is a bounded block buffer or nothing at all.
func TestTheWritersPeakAllocationDoesNotGrowWithTheItemCount(t *testing.T) {
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

	for _, shape := range blockShapes {
		t.Run(shape.name, func(t *testing.T) {
			atSmall := peakWriting(t, small, shape.opts...)
			atLarge := peakWriting(t, large, shape.opts...)

			if atLarge > atSmall+tolerance {
				t.Errorf("live heap at the last of %d items is %d bytes, against %d bytes at the last of %d: it grew by %d, more than the %d tolerated",
					large, atLarge, atSmall, small, atLarge-atSmall, tolerance)
			}
		})
	}
}

// peakWriting writes n items into a four-kilobyte buffer over a sink that keeps
// nothing, and returns the live heap measured as the last of them is written.
// Every stream it is called with is far larger than that buffer, so the bytes
// cannot all have been in memory at once.
func peakWriting(t *testing.T, n int, opts ...avro.ArrayWriterOption) uint64 {
	t.Helper()

	sink := &countingWriter{}
	buffered := bufio.NewWriterSize(sink, pipeBufferSize)

	var peak uint64
	err := WriteEvents(buffered, func(s *EventWriter) error {
		for i := range n {
			if err := s.Write(event(i)); err != nil {
				return err
			}
			if i == n-1 {
				peak = liveHeap()
			}
		}
		return nil
	}, opts...)
	if err != nil {
		t.Fatalf("failed to write %d event(s): %v", n, err)
	}
	if err := buffered.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if sink.n <= pipeBufferSize {
		t.Fatalf("only %d byte(s) reached the sink, which is not more than the %d byte buffer: the stream is too small to say anything about materialising it", sink.n, pipeBufferSize)
	}
	return peak
}
