# avroc Example

This example demonstrates using `avroc` to generate code from an Avro IDL schema.

## Schemas

There are two, because a schema's **root type** changes what a generator has to produce for
it:

- [`schema.avdl`](schema.avdl) is a record root, based on the
  [Apache Avro documentation](https://avro.apache.org/docs/1.12.0/idl-language/#schemaavdl).
  It describes one value.
- [`events.avdl`](events.avdl) is an array root, `schema array<Event>;`. It describes a
  *stream* of `Event`, and the Go generator emits a streaming reader for it.

## Manifest

The generators and their configuration are declared in [`avroc.json`](avroc.json): a `go`,
`json` and `pcf` generator, each with its output directory, its options and the inputs it
reads. `avroc init` scaffolds a starter manifest; this example ships a ready-made one.

There are **two `go` entries** rather than one, because `encoding=single_object` is a
per-generator option and single-object encoding frames a single value: it requires a record
at the schema root and refuses an array one. So `schema.avdl` goes to a `go` generator with
that option and `events.avdl` to one without, into separate packages. That is also why the
inputs are declared per generator here instead of once at the top level — a top-level input
is read by every generator.

## Generating Code

To generate code from the schemas:

```bash
# Install the avroc tools (so they are discovered on PATH as avroc-gen-<name>)
go install ./cmd/...

# From this example directory, run the generators declared in avroc.json
cd example
avroc generate
```

This reads `avroc.json` and produces Go types (`gen/` and `stream/`), Avro JSON schemas
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
`Event` type and its methods **and** a streaming reader over them:

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

The reader is a wrapper and nothing more: the block framing lives in
[avro-go](https://github.com/z5labs/avro-go), because it is the same for every item type
and a copy of it emitted once per schema would be a copy to keep correct. The array is
never materialised — [`stream/event_stream_test.go`](stream/event_stream_test.go) streams a
hundred thousand items through a four-kilobyte pipe and checks that the live heap at the
last item is what it was at the last item of a thousand.

### JSON Schema (`test_record.avsc`, `event.avsc`)

The JSON generator produces an Avro JSON schema file (`.avsc`) following the [Avro specification](https://avro.apache.org/docs/1.12.0/specification/#schema-declaration). Named types are inlined on first use and referenced by name afterwards.

### Parsing Canonical Form (`pcf/`)

The PCF generator produces a compact JSON file containing the [Avro Parsing Canonical Form](https://avro.apache.org/docs/1.12.0/specification/#parsing-canonical-form-for-schemas) of the schema. Named types are inlined on first use and referenced by their fully-qualified name on subsequent uses. This form is useful for schema validation, fingerprinting, and ensuring interoperability across systems.
