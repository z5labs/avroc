# avroc Example

This example demonstrates using `avroc` to generate code from an Avro IDL schema.

## Schemas

There are three. Two differ in their **root type**, which changes what a generator has to
produce for it:

- [`schema.avdl`](schema.avdl) is a record root, based on the
  [Apache Avro documentation](https://avro.apache.org/docs/1.12.0/idl-language/#schemaavdl).
  It describes one value.
- [`events.avdl`](events.avdl) is an array root, `schema array<Event>;`. It describes a
  *stream* of `Event`, and the Go generator emits a streaming reader for it.

The third, [`unions.avdl`](unions.avdl), is a record root like the first, and is here for
*where* its named types are defined: avroc writes a named type out in full at its first
reference from the root, and both of `Outer`'s named types are first referenced from a union
branch.

## Manifest

The generators and their configuration are declared in [`avroc.json`](avroc.json): a `go`,
`json` and `pcf` generator, each with its output directory, its options and the inputs it
reads. `avroc init` scaffolds a starter manifest; this example ships a ready-made one.

There are **three `go` entries** rather than one. The first two are separate because `encoding=single_object` is a
per-generator option and single-object encoding frames a single value: it requires a record
at the schema root and refuses an array one. So `schema.avdl` goes to a `go` generator with
that option and `events.avdl` to one without, into separate packages. That is also why the
inputs are declared per generator here instead of once at the top level — a top-level input
is read by every generator. `unions.avdl` gets its own package so that its types live beside
the test that round-trips them.

## Generating Code

To generate code from the schemas:

```bash
# Install the avroc tools (so they are discovered on PATH as avroc-gen-<name>)
go install ./cmd/...

# From this example directory, run the generators declared in avroc.json
cd example
avroc generate
```

This reads `avroc.json` and produces Go types (`gen/`, `stream/` and `unions/`), Avro JSON schemas
(`test_record.avsc`, `event.avsc`), and Parsing Canonical Form files (`pcf/`).

It also writes [`avroc.gen.json`](avroc.gen.json), the committed record of every file
the run generated. That record is what the next run prunes against: rename `TestRecord`
and the file the old name produced is removed rather than left behind. Note that the
`json` generator's output directory here is the example root, which it shares with
`schema.avdl`, `avroc.json` and this README — that is safe because a file avroc never
recorded generating is never a candidate for removal. `stream/event_stream_test.go` is
the same arrangement seen from the other side: a hand-written test living beside
generated code, kept because nothing ever recorded generating it.

## Generated Output

### Go (`gen/`)

The Go generator produces a Go file in `gen/` containing:

- `Kind` - an enum type with `KindFOO`, `KindBAR`, `KindBAZ` constants
- `MD5` - a fixed 16-byte type
- `TestRecordNullableHashUnion` - a union interface with `TestRecordNullableHashUnionNull` and `TestRecordNullableHashUnionMD5` implementations
- `TestRecord` - a struct with fields for name, kind, hash, and nullable hash

With `encoding=single_object`, the primary record type also includes a `Fingerprint()` method that returns the precomputed CRC-64-AVRO schema fingerprint for [Avro Single Object Encoding](https://avro.apache.org/docs/1.12.0/specification/#single-object-encoding).

### Go (`stream/`)

`events.avdl` has an array root, so [`stream/event.go`](stream/event.go) contains the
`Event` type and its methods **and** a streaming reader and writer over them:

- `StreamEvents(r io.Reader) iter.Seq2[*Event, error]` — the ordinary way to read one:

  ```go
  for ev, err := range stream.StreamEvents(r) {
      if err != nil {
          return err
      }
      fmt.Println(ev.Id)
  }
  ```

- `EventReader`, which embeds `*avro.ArrayReader`. Its `Next` decodes into a destination
  you own, so one `Event` can be reused for the whole stream, and its `SkipBlock` discards
  a block that declared its encoded size without decoding a single item.

- `WriteEvents(w io.Writer, f func(*EventWriter) error, opts ...avro.ArrayWriterOption) error`
  — the ordinary way to produce one, and it takes no `[]Event`:

  ```go
  err := stream.WriteEvents(w, func(s *stream.EventWriter) error {
      for _, ev := range events {
          if err := s.Write(ev); err != nil {
              return err
          }
      }
      return nil
  })
  ```

  It terminates the array once `f` returns without error. That close is not a nicety: an
  Avro array ends in a zero-count block, so a stream that is never closed is a *truncated*
  array, and reading one back fails with `avro.ErrTruncatedArray` rather than quietly
  yielding the items that happened to arrive.

- `EventWriter`, which embeds `*avro.ArrayWriter`, for when the writing is not shaped like
  one function. `NewEventWriter(w, opts...)` returns one and its `Close` terminates the
  array. Pass `avro.WithSizedBlocks(n)` to either form to batch items into blocks that
  declare their encoded size — which is what makes `SkipBlock` above able to discard one —
  at the cost of a buffer bounded by `n`. The default buffers nothing and each item goes
  straight out; both are conforming Avro, and which you want is a trade the generator does
  not make for you.

Both are wrappers and nothing more: the block framing lives in
[avro-go](https://github.com/z5labs/avro-go), because it is the same for every item type
and a copy of it emitted once per schema would be a copy to keep correct. The array is
never materialised in either direction —
[`stream/event_stream_test.go`](stream/event_stream_test.go) streams a hundred thousand
items through a four-kilobyte pipe and checks that the live heap at the last item is what
it was at the last item of a thousand, and
[`stream/event_writer_test.go`](stream/event_writer_test.go) does the same writing them.

### Go (`unions/`)

[`unions/outer.go`](unions/outer.go) declares `Tone` and `Inner` even though neither is
defined beside `Outer` in the descriptor the generator receives: each is defined inside the
union branch that first refers to it, and the generator walks a union's branches for the
definitions they carry before it writes the union. Alongside them are the two unions,
`OuterToneUnion` and `OuterInnerUnion`, each an interface with a `…Null` member and a member
wrapping the named type, and `Outer` itself.

[`unions/outer_test.go`](unions/outer_test.go) encodes an `Outer` holding every combination
of the two unions' branches, compares the bytes with the Avro binary encoding, and decodes
them back.

### JSON Schema (`test_record.avsc`, `event.avsc`)

The JSON generator produces an Avro JSON schema file (`.avsc`) following the [Avro specification](https://avro.apache.org/docs/1.12.0/specification/#schema-declaration). Named types are inlined on first use and referenced by name afterwards.

### Parsing Canonical Form (`pcf/`)

The PCF generator produces a compact JSON file containing the [Avro Parsing Canonical Form](https://avro.apache.org/docs/1.12.0/specification/#parsing-canonical-form-for-schemas) of the schema. Named types are inlined on first use and referenced by their fully-qualified name on subsequent uses. This form is useful for schema validation, fingerprinting, and ensuring interoperability across systems.
