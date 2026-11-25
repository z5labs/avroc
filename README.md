# avroc

A modular code generator for messages and services defined in [Avro IDL](https://avro.apache.org/docs/current/idl-language/).

## Specs

Three interfaces are built against from outside this repository, so each is
specified rather than merely implemented:

- [The resolved IR](docs/ir/SPEC.md) — what every generator plugin consumes, in
  any language.
- [The generator plugin CLI contract](docs/plugin/SPEC.md) — what
  `avroc-gen-<name>` has to implement.
- [The container base-image contract](docs/container/SPEC.md) — what a
  Dockerfile building `FROM` the published image may rely on.

[docs/CONVENTIONS.md](docs/CONVENTIONS.md) defines the conformance language all
three use, and what else they have in common.

## Features

- **Declarative manifest** — a project's generators and their configuration live in a checked-in `avroc.json` manifest, so generator selection and options are diffable, reviewable, and shared across a team and CI. `avroc init` scaffolds one to get started.
- **Dynamic generator discovery** — avroc discovers generator plugins on your `PATH` using the naming convention `avroc-gen-<name>`; a manifest entry's `name` resolves to the matching `avroc-gen-<name>` executable.
- **No acquisition machinery** — avroc never fetches, pins or verifies a generator: there is no registry, no lockfile and no cache. A generator arrives on `PATH` by whatever means put it there, and reproducibility comes from a [container image](docs/container/SPEC.md) pinned by digest rather than from a file avroc writes.
- **Capability handshake** — every resolved generator is asked what it accepts (`--plugin-info`) before any schema is parsed, so a generator too old for the IR, or one handed an option it does not know, fails the run immediately rather than late and confusingly.
- **Type validation** — avroc resolves all type references in your Avro IDL schemas and reports errors for any undefined types before invoking generators.
- **Value validation** — avroc validates field defaults and enum defaults against their declared types, catching mistakes (e.g. a `null` default on an `int` field) at generation time.
- **Parallel generation** — all generators run concurrently, so code generation scales with the number of plugins you use.

## Architecture

```
┌──────────────────────────────────────────────────────┐
│  avroc generate                                      │
│                                                      │
│  1. Read avroc.json manifest                         │
│  2. Resolve each generator to avroc-gen-<name>       │
│     on PATH, and ask it what it can do:              │
│       exec avroc-gen-<name> --plugin-info  ─────────►│  writes {"name",
│                                            ◄─────────│   "version",
│     A refusal here fails the run, before any         │   "ir_version",
│     schema is parsed and with nothing generated.     │   "options"}, exits
│  3. Parse & validate the declared Avro IDL inputs,   │
│     and resolve every type reference in them         │
│  4. For each generator (concurrently):               │
│     a. Write the descriptor — the resolved schemas   │
│        and that generator's options, protobuf-       │
│        encoded — into a directory created for        │
│        that one invocation                           │
│     b. exec avroc-gen-<name>  ──────────────────────►│  reads the file at
│          --descriptor <path>                         │  --descriptor,
│          --out <dir>                                 │  writes files beneath
│          [--opt k=v ...]                             │  --out, exits
│     c. Wait for it to exit; non-zero fails the run   │
│  5. Merge each zero-exit generator's files into the  │
│     project, prune what avroc.gen.json still names,  │
│     and rewrite the record                           │
└──────────────────────────────────────────────────────┘
```

A generator is an **executable, not a server**: avroc finds it on `PATH`, runs it with a descriptor file and an output directory, and waits for it to exit. There is no socket, no port, no stream and no protobuf runtime required to be reachable — so a generator can be written in any language, including a shell script.

The only thing that crosses between the two is **files**: a descriptor file avroc writes and the generator reads, and the files the generator writes beneath `--out`. `--out` is a private, empty scratch directory for that one invocation, merged into the project only on a zero exit, so a generator that fails partway leaves nothing behind. [`docs/plugin/SPEC.md`](docs/plugin/SPEC.md) is the contract.

## Installation

There are two ways to get avroc and its generators, and they are not
interchangeable:

| | What you get | Reproducible? |
|---|---|---|
| [`go install`](#go-install) | Executables on your `PATH` | **No** — see [Where generators come from](#where-generators-come-from-and-reproducibility) |
| [The published image](#the-published-image) | avroc and a generator in one container | **Yes**, when pinned by digest |

### `go install`

```bash
go install github.com/z5labs/avroc/cmd/avroc@latest
```

Install the built-in generators you need:

```bash
# Go code generator
go install github.com/z5labs/avroc/cmd/avroc-gen-go@latest

# Avro JSON schema generator
go install github.com/z5labs/avroc/cmd/avroc-gen-json@latest

# Avro Parsing Canonical Form generator
go install github.com/z5labs/avroc/cmd/avroc-gen-pcf@latest
```

Each generator lands on your `PATH` under the name avroc looks for, and a
manifest entry's `name` resolves to it. This is the convenient path for a
developer's laptop; it makes no reproducibility guarantee.

### The published image

avroc is published as a container image, and each built-in generator as an image
built `FROM` it. Run one over the project in the current directory. Mount that
directory wherever you like and point `-w` at the same path — the image declares
no working directory of its own, so both flags are needed, and `/work` below is
this project's convention and nothing more:

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/work" -w /work \
  ghcr.io/z5labs/avroc-gen-go:v0 generate
```

Nothing is installed on the host and there is no `avroc` binary to keep in step
with the generator: the image is both. Four tags are published for every image —
`v0.2.0`, `v0.2`, `v0`, `latest` — and a digest is the fifth way to name one.
**Pin the digest when reproducibility is the requirement**, since that fixes
avroc and every generator in the image together.

A project whose manifest names more than one generator needs a single image
holding all of them, which is two `COPY --from` lines and no build from source:

```dockerfile
# syntax=docker/dockerfile:1

FROM ghcr.io/z5labs/avroc-gen-go:v0
COPY --from=ghcr.io/z5labs/avroc-gen-json:v0 \
     /usr/local/bin/avroc-gen-json /usr/local/bin/avroc-gen-json
COPY --from=ghcr.io/z5labs/avroc-gen-pcf:v0 \
     /usr/local/bin/avroc-gen-pcf /usr/local/bin/avroc-gen-pcf
```

The same two lines add a generator this project has never heard of, because the
only thing they depend on is the plugin directory being where the [container
base-image contract](docs/container/SPEC.md) says it is. That document is also
where the [tag table](docs/container/SPEC.md#tags-and-what-pinning-one-buys) and
the `cosign` commands that [verify a
signature](docs/container/SPEC.md#verifying-a-signature) live; every published
image is signed keyless, so there is no avroc key to obtain.

To use the container path without writing a Dockerfile at all, see [Generating in
a pipeline, without a Dockerfile](#generating-in-a-pipeline-without-a-dockerfile).

## Usage

avroc is driven by a project manifest (`avroc.json`) and exposes these commands:

```
avroc init        # scaffold a starter avroc.json (never clobbers an existing one)
avroc generate    # run the generators declared in avroc.json
avroc inspect     # render a descriptor file as JSON (use - for stdin)
```

### Manifest (`avroc.json`)

The manifest declares the input IDL files and the generators to run:

```json
{
  "inputs": ["schema.avdl"],
  "generators": [
    {
      "name": "go",
      "out": "gen",
      "options": { "package_name": "mypackage", "encoding": "single_object" }
    },
    { "name": "json", "out": "." },
    { "name": "pcf", "out": "pcf" }
  ]
}
```

| Field | Scope | Description |
|---|---|---|
| `inputs` | top-level | IDL files shared by every generator. |
| `name` | generator | Logical name; resolves to the `avroc-gen-<name>` executable on `PATH`. |
| `out` | generator | Output directory (relative to the manifest). |
| `options` | generator | `key`/`value` generator options. |
| `inputs` | generator | IDL files specific to this generator, merged with the top-level `inputs`. |

> A generator whose `name` is not found on `PATH` is reported as an error. A `name` is the whole
> of how a generator is identified — there is no `source` and no `version`, and a manifest still
> carrying either is rejected by name with what to do instead.

### Where generators come from, and reproducibility

avroc does not fetch a generator, does not pin one, does not verify one and does not know where
one came from. There is no plugin registry, no `avroc get`, no `avroc.lock` and no image cache:
a generator arrives on `PATH` by whatever means put it there — a `go install`, a package
manager, a `COPY` in a Dockerfile — and avroc runs the first match it finds.

That carries a trade, stated in full under
[Plugin distribution, and reproducibility](docs/plugin/SPEC.md#plugin-distribution-and-reproducibility):

> **avroc makes no reproducibility guarantee about the host-execution path.** The same manifest
> and the same schemas, on two hosts, can produce different generated code if the two hosts have
> different builds of a generator on their `PATH` — and avroc cannot detect it.
>
> **The container is the reproducible path.** An image pinned by digest fixes every generator in
> it, and that is the configuration this project supports when reproducibility is a requirement.
> `go install` and a `PATH` are a convenience for a developer's laptop.

[The published image](#the-published-image) above is how to run one, and the [container
base-image contract](docs/container/SPEC.md) is what a Dockerfile building `FROM` it may rely
on.

### Generating in a pipeline, without a Dockerfile

For a caller who wants the container path but would rather not write a Dockerfile, this
repository publishes a companion [Dagger](https://dagger.io) module. It pulls the published
images, composes the generators a project needs, runs `avroc generate` and hands the generated
tree back:

```bash
dagger call -m github.com/z5labs/avroc/daggerverse/avroc \
  with-generator --name go \
  with-generator --name json \
  generate --source . \
  export --path .
```

Nothing is installed on the host and no image is built. `--version` picks the avroc release,
`with-generator --image` takes a generator out of any image that carries one — including one
this project has never heard of — and `with-generator-executable` takes one straight from a
file, which is what a generator author reaches for before they have published anything.

The module is a convenience over the container contract rather than a contract of its own, so
it has no spec: `dagger call -m github.com/z5labs/avroc/daggerverse/avroc --help` and the module
comment in [`daggerverse/avroc/main.go`](daggerverse/avroc/main.go) are its documentation.

### Example

Given the following Avro IDL file (`schema.avdl`):

```
namespace org.apache.avro.test;

schema TestRecord;

enum Kind {
  FOO,
  BAR,
  BAZ
}

fixed MD5(16);

record TestRecord {
  string name;
  Kind kind;
  MD5 hash;
  union { null, MD5 } nullableHash;
}
```

Scaffold a manifest, edit it to declare the `go`, `json`, and `pcf` generators (as above),
then generate:

```bash
avroc init
# edit avroc.json
avroc generate
```

This produces:
- `gen/test_record.go` — Go types with `MarshalAvroBinary` / `UnmarshalAvroBinary` and `SizeAvro` / `AppendAvro` methods
- `test_record.avsc` — Avro JSON schema
- `pcf/test_record.avsc` — Avro Parsing Canonical Form
- `avroc.gen.json` — the record of what was generated, below

See the [`example/`](example/) directory for a working example.

### Stale generated files (`avroc.gen.json`)

avroc owns the output tree, so it removes what it put there and no longer produces.
Rename a record and the file the old name produced is deleted rather than left behind
to be committed and eventually compiled.

The mechanism is a committed record, **`avroc.gen.json`**, written beside `avroc.json`
after every successful run and naming every file that run generated:

```json
{
  "version": 1,
  "files": ["gen/test_record.go", "pcf/test_record.avsc", "test_record.avsc"]
}
```

- **Commit it** alongside the generated output it describes — it is what the next
  regeneration, including the first one in a clean checkout, prunes against. A run that
  finds no record prunes nothing.
- **A file avroc did not generate is never removed.** Ownership is the record, not the
  directory, so an output directory shared with hand-written source is safe — which is
  what makes `"out": "."` usable.
- **Only regular files.** A recorded path a person has replaced with a directory or a
  symlink is left alone and reported.
- **`avroc.gen.json` is avroc's.** A generator that produces it fails the run.

[`docs/plugin/SPEC.md`](docs/plugin/SPEC.md) is normative; a plugin never maintains a
record of its own.

### Inspecting a descriptor (`avroc inspect`)

Every generator invocation is handed a **descriptor**: the IR version, that generator's
options, and the resolved schemas, in the protobuf binary encoding
([`docs/ir/SPEC.md`](docs/ir/SPEC.md)). When a generator emits output nobody expected, the
first question is what it was actually handed, and `avroc inspect` answers it:

```bash
avroc inspect descriptor.binpb    # render a saved descriptor as JSON
avroc inspect - < descriptor.binpb # or read it from stdin
```

The rendering uses the field names from `proto/` and the spec (`full_name`, not `fullName`),
and is byte-stable across runs and across avroc builds, so two descriptors can simply be
diffed. It is a rendering for people, never an input: a generator is handed the binary
descriptor, and nothing reads the JSON back. A descriptor whose IR version this avroc does
not know still renders — that is the case worth reading.

> avroc removes an invocation's descriptor once the generator exits, so what you inspect is a
> copy a generator saved from the path it was handed.

## Built-in Generators

### `avroc-gen-go`

Generates idiomatic Go types with binary Avro serialization support.

Generated code imports [avro-go](https://github.com/z5labs/avro-go) and **requires
v0.7.0 or later**: it decodes enum ordinals and union branch indexes with
`BinaryReader.ReadIndex`, which that release added, and its `SizeAvro` and `AppendAvro`
methods are built on avro-go's `Size*` and `Append*` functions, and `compare`'s functions on its
`Compare*` and `Consume*` functions. An ordinal or branch index
the schema does not define fails the decode with an error matching `avro.ErrInvalidIndex`,
in either reader mode, rather than producing a value no symbol names.

| Option | Required | Description |
|---|---|---|
| `package_name` | Yes | The Go package name for all generated files. |
| `compare` | No | `true` or `false`, and `false` when absent. `true` generates a `Compare<Record>Avro` function for every record, which compares two encoded records by Avro's sort order without decoding them — see [Comparing encoded records](#comparing-encoded-records). A record it cannot generate one for fails the run, naming the record, the field and the field's type. Any other value fails the run naming it. |
| `views` | No | `true` or `false`, and `false` when absent. `true` generates a `<Record>View` for every record, which indexes an encoded record's fields from a prefix of its bytes without copying them — see [Viewing encoded records](#viewing-encoded-records). A record it cannot generate one for fails the run, naming the record, the field and the field's type. Any other value fails the run naming it. |
| `encoding` | No | Set to `single_object` to generate a `Fingerprint()` method on the primary record type for [Avro Single Object Encoding](https://avro.apache.org/docs/current/specification/#single-object-encoding). Every schema in the run must be rooted at a record. Any other root — an array, a map, a union, an enum, a fixed or a primitive — fails the run with a diagnostic naming that root, rather than generating without the fingerprint. |

**Generated types:**

| Avro type | Go type |
|---|---|
| `record` | `struct` with `MarshalAvroBinary` / `UnmarshalAvroBinary`, and `SizeAvro` / `AppendAvro` |
| `enum` | `int` type with typed constants, and the same four methods |
| `fixed` | `[N]byte` type, and the same four methods |
| `union { null, T }` | interface with `Null` and `T` implementations; the interface and every implementation have `MarshalAvroBinary`, `SizeAvro` and `AppendAvro` |
| `string` | `string` |
| `int` / `long` | `int32` / `int64` |
| `float` / `double` | `float32` / `float64` |
| `boolean` | `bool` |
| `bytes` | `[]byte` |

**Encoding into a `[]byte`.** Every type that has `MarshalAvroBinary` also has two methods,
always generated, with the same receiver:

```go
func (x *Record) SizeAvro() int                // the length of x's Avro binary encoding
func (x *Record) AppendAvro(dst []byte) []byte // dst extended by exactly that encoding
```

`AppendAvro` is the **allocation-free encoder**: it needs no `io.Writer`, and appending into
a `dst` that already has `SizeAvro()` bytes of room makes no allocation — map values and union
branches included. `SizeAvro` never allocates. Together they are what writing records into
fixed-size blocks takes — check a record fits, then write it there:

```go
if len(block)+rec.SizeAvro() > cap(block) {
    block = flush(block)
}
block = rec.AppendAvro(block)
```

`SizeAvro()` is always `len(AppendAvro(nil))`, `AppendAvro` leaves `dst[:len(dst)]` as it
was, and the bytes are the ones `MarshalAvroBinary` writes — except that a map of more than
one entry may be written in a different order, since Go does not order a map's entries, and
decodes to the same value either way. A field named `sizeAvro` or `appendAvro` would collide
with these methods, and is refused at generation like any other collision.

#### Comparing encoded records

With `"compare": "true"`, every record also gets a function that orders two encoded records by
the specification's [sort order](https://avro.apache.org/docs/1.12.0/specification/#sort-order)
straight from the buffer — what a sort or a merge join over encoded records calls. It goes in
the same `<schema>.go` file as the record's type:

```go
// CompareRecordAvro compares the Records encoded at the start of a and b by
// the spec's sort order.
func CompareRecordAvro(a, b []byte) (c, na, nb int)
```

`c` is `-1`, `0` or `+1` as `a`'s record sorts before, equal to or after `b`'s. `na` and `nb`
follow avro-go's `Consume` convention, one per side and each side consumed independently:

- a non-negative value is the number of bytes read from that side, and reading stops at the
  first field that differs, so it is not the record's length;
- a negative value is an error code for `avro.ConsumeError`: `io.ErrUnexpectedEOF` when that
  side ended too soon and more bytes might decide it, or `avro.ErrOverflow`,
  `avro.ErrNegativeLength` or `avro.ErrInvalidIndex` when it is corrupt;
- `c` is `0` whenever either is negative.

Fields are compared in schema order. A field with `@order("descending")` has its result
negated, and one with `@order("ignore")` is skipped. A **trailing run** of ignored fields is
never read at all, so `a` and `b` only have to hold each record's encoding up to the last
compared field — for `record Record { Hash hash; bytes key; Side side; bytes @order("ignore")
value; }` that is through `side`, and the value can still be on its way. Each type is ordered
as the specification orders it:

| Type | Order |
|---|---|
| `null` | always equal |
| `boolean` | `false` before `true`; any non-zero byte is `true` |
| `int`, `long` | by value |
| `float`, `double` | `cmp.Compare`: NaN first, NaN equal to NaN, -0 equal to +0 |
| `bytes`, `string`, `fixed` | by unsigned bytes |
| enum | by ordinal; an ordinal no symbol names is reported as `avro.ErrInvalidIndex` |
| record | field by field, recursively, with a descending record's fields negated and an ignored record's skipped |

It never allocates and never panics or reads out of bounds, on any input. The one-byte case of
every length, index and varint is decoded in the generated code itself, and avro-go's
`CompareLong`, `CompareBytes` and `Consume*` functions are called only for longer encodings.

**Scope.** Every field a `Compare` has to read must be a primitive, an enum, a fixed or a
record, and the same goes for each nested record's fields. The exception is a trailing run of
ignored fields, which is never read and so may be of any type. A record with a map, an array
or a union anywhere else — or a record that contains itself — fails the run under
`compare=true`, before any file is written. A nested record's fields are compared inline rather
than through its own function, so a record reused in several fields is written out once per
field, and the function grows with the number of paths to the record's leaves. A record named `Compare<Other>Avro` beside a
record `Other` collides with that function, and is refused like any other collision.

#### Viewing encoded records

With `"views": "true"`, every record also gets a `<Record>View`, in the same `<schema>.go`, which
indexes one encoded record **from a prefix of its bytes, without decoding or copying it** — what a
reader of in-memory blocks and run files holds instead of a decoded record. For the
`record Record { Hash hash; bytes key; Side side; bytes @order("ignore") value; }` above:

```go
type RecordView struct{ /* unexported */ }

func (v *RecordView) Parse(b []byte) error
func (v *RecordView) Need() int
func (v *RecordView) PrefixLen() int
func (v *RecordView) Len() int
func (v *RecordView) Complete() bool
func (v *RecordView) Bytes() []byte
func (v *RecordView) Decode(x *Record)
func (v *RecordView) Compare(w *RecordView) int // only when compare is also true

func (v *RecordView) Hash() Hash
func (v *RecordView) HashBytes() []byte
func (v *RecordView) Key() []byte
func (v *RecordView) Side() Side
func (v *RecordView) Value() []byte
func (v *RecordView) ValueLen() int

const RecordHashOffset = 0
```

**The prefix.** When a record's last field is `bytes` or `string`, its prefix is the encoding up
to that field's payload — through its length, but not the payload itself. When the last field is
a nested record, the same rule applies to that record's last field, recursively. For any other
last field the prefix is the whole record. `Parse(b)` needs only `PrefixLen()` bytes, so the key,
the side and the value's size can all be read before the value has arrived; `Len()` is the whole
record's size, known from the prefix alone, and `Complete()` reports whether `b` holds all of it.
`Bytes()` is the record's encoding, `b[:Len()]`.

**Need more, or corrupt.** `Parse` never allocates, and its errors are avro-go's sentinels,
unwrapped:

- `io.ErrUnexpectedEOF` when `b` ends before the prefix does and more bytes might parse. Then
  `len(b) < Need() <= PrefixLen()`, no shorter `b` can parse, and retrying with at least `Need()`
  bytes either parses, reports corruption, or reports a strictly larger `Need()`.
- `avro.ErrOverflow`, `avro.ErrNegativeLength` or `avro.ErrInvalidIndex` when the encoding is
  corrupt. `Parse` and `UnmarshalAvroBinary` agree on which input that is, and no more bytes
  mend it.

After a failed `Parse` the view is empty, as the zero view is, and every accessor panics.

**Accessors.** Each field has one, named after it:

| Field | Accessor |
|---|---|
| `bytes` | a slice of the parsed bytes whose capacity is its length |
| `string` | a copy, as a `string`; `<Field>Bytes()` is a slice of the parsed bytes |
| `fixed` | a copy of the value; `<Field>Bytes()` is a slice of the parsed bytes |
| `boolean`, `int`, `long`, `float`, `double`, enum | the value |
| record | that record's view, which shares the parsed bytes |

The last field, when it is `bytes` or `string`, also gets `<Field>Len()`, and its accessors panic
unless the view is `Complete()`. Each field before the first variable-size one — anything but a
`boolean`, `float`, `double`, `fixed`, or a record of only those — gets a
`<Record><Field>Offset` constant, since its offset is the same in every encoding.

`Decode(x)` sets `bytes` fields to slices of the parsed bytes, `string` fields to copies and
`fixed` fields by value, and panics unless the view is `Complete()`. With `compare` also `true`,
`View.Compare` orders two parsed records exactly as `Compare<Record>Avro` orders their
encodings; when the record's last field is compared and variable-size it needs both views
`Complete()`, and otherwise it needs only their prefixes. `Parse`, every accessor but those
returning a `string`, and `View.Compare` make no allocations, and `Decode` allocates only for
`string` fields.

**Aliasing.** A view, and every slice it returns, is valid only as long as the buffer it parsed
is unchanged: reusing, overwriting or moving that buffer invalidates them.

**Scope.** Every field must be a primitive, an enum, a fixed or a record, and the same goes for
each nested record's fields. A view indexes every field, so unlike `compare` there is no exception
for ignored fields: a record with a union, an array or a map anywhere — or one that contains
itself — fails the run under `views=true`, before any file is written. The view's type, methods,
accessors and offset constants are registered with the collision check like every other
identifier, so a field named `complete`, or a pair of fields named `hash` and `hashBytes`, is
refused.

**Streaming an array root.** A schema whose root type is `array<T>` says the file it
describes is a *stream of `T`*, so `avroc-gen-go` generates a streaming reader and writer
alongside `T` itself. For `schema array<Event>;`:

```go
for ev, err := range StreamEvents(r) { // r is any io.Reader
    if err != nil {
        return err
    }
    fmt.Println(ev.Id)
}
```

`NewEventReader(r)` returns the reader that iterator is built on. It embeds
[avro-go](https://github.com/z5labs/avro-go)'s `*avro.ArrayReader`, which owns the block
framing: its `Next` decodes into a destination you own, so one `Event` can be reused for the
whole stream, and its `SkipBlock` discards a block that declared its encoded size without
decoding a single item. The array is never materialised — memory is a function of what you
keep, not of how long the stream is.

Producing one is the same shape in reverse, and takes no `[]Event`:

```go
err := WriteEvents(w, func(s *EventWriter) error { // w is any io.Writer
    for _, ev := range events {
        if err := s.Write(ev); err != nil {
            return err
        }
    }
    return nil
})
```

`WriteEvents` terminates the array once the callback returns without error, which is the
close you would otherwise have to remember: an Avro array ends in a zero-count block, so a
stream that is never closed is a *truncated* array and reading it back fails with
`avro.ErrTruncatedArray`. `NewEventWriter(w, opts...)` is the explicit form, and its `Close`
is the embedded `*avro.ArrayWriter`'s.

Whether blocks carry their size is yours to choose, because it is a trade rather than a
default worth picking for you. By default nothing is buffered and each item goes straight
out. Pass `avro.WithSizedBlocks(n)` and items are batched into blocks that declare their
encoded size — which is exactly what lets a reader's `SkipBlock` discard one without
decoding it — at the cost of a buffer bounded by `n`. Both are conforming Avro.

The item type has to be one this generator gave `MarshalAvroBinary` and `UnmarshalAvroBinary`
— a `record`, an `enum`, a `fixed`, or a reference to one. `array<string>` and nested arrays
generate the types they always did and no reader or writer.

### `avroc-gen-json`

Generates [Avro JSON schema](https://avro.apache.org/docs/current/specification/#schema-declaration) files (`.avsc`). avroc decides where each named type is written out in full and where it is referenced by its fully-qualified name; the generator follows that ordering.

No options required.

### `avroc-gen-pcf`

Generates [Avro Parsing Canonical Form](https://avro.apache.org/docs/1.12.0/specification/#parsing-canonical-form-for-schemas) files (`.avsc`). The output is a compact JSON representation with attribute names and type ordering normalized per the Avro specification. avroc decides where each named type is written out in full and where it is referenced by its fully-qualified name; the generator follows that ordering. The file content is written as exact canonical bytes — no trailing newline — so it can be used directly for fingerprinting.

No options required.

## Writing a Custom Generator

1. Create an executable named `avroc-gen-<name>` and put it on your `PATH`.
2. Answer `--plugin-info` by writing a capability declaration to stdout and exiting zero — nothing else. avroc runs this on every generator before any generation begins, and a generator that will not answer fails the run:

   ```json
   {"name": "mygen", "version": "0.1.0", "ir_version": 1, "options": ["style"]}
   ```

   `options` lists the `--opt` keys you accept; omit the member to decide for yourself instead.
3. Accept `--descriptor <path> --out <dir> [--opt k=v ...]`, each option followed by its value as the next argument. `--descriptor -` means the descriptor arrives on standard input. Nothing else is on the vector, and nothing comes from the environment.
4. Decode the descriptor at that path: it is a `GenerateRequest` in the protobuf binary wire encoding (see [`proto/`](proto/) and [`docs/ir/SPEC.md`](docs/ir/SPEC.md)). Check its `version` first.
5. Write every generated file beneath `--out`, report problems on stderr in whatever form you like — avroc passes it through to its own stderr unaltered and reads none of it — and exit zero. The exit status is the whole of what avroc analyses: a non-zero exit fails the run and nothing you wrote is adopted as output.

`--out` is a private, empty scratch directory for that one invocation, not the project's output directory: avroc merges it in only after a zero exit, which is what makes a partial failure harmless. Two runs over the same descriptor must produce byte-identical output.

The full contract — discovery, the argument vector, capability negotiation, the descriptor's lifetime, exit codes and what becomes of the standard streams, and the determinism a plugin must exhibit — is [`docs/plugin/SPEC.md`](docs/plugin/SPEC.md).

To ship it, build an image `FROM` the published one and `COPY` the executable into the plugin directory — that is the whole of the distribution mechanism, and [_Worked example: adding a generator_](docs/container/SPEC.md#worked-example-adding-a-generator) is a runnable multi-stage Dockerfile doing exactly that.

The schemas you are handed are **resolved**: every named type carries its fully-qualified name, every `Reference` states whether it names an Avro primitive or a named type, and a named type's definition travels at its first use with every later use carrying only its name. A generator therefore builds no symbol table and re-derives no namespace qualification, primitive classification or first-use ordering — see [`docs/ir/SPEC.md`](docs/ir/SPEC.md).

The protobuf definitions are in [`proto/`](proto/) and the generated Go stubs are in [`avrocpb/`](avrocpb/). A generator written in Go imports them directly:

```go
import "github.com/z5labs/avroc/avrocpb"
```

```console
go get github.com/z5labs/avroc
```

### A generator in a language with no protobuf codegen

A generator does not have to compile `proto/` to read a descriptor. avroc publishes
**`ir.binpb`** — a protobuf `FileDescriptorSet` describing the descriptor — as an
asset on each release. Load it, look up `GenerateRequest` by name, and decode the
descriptor as a dynamic message: four library calls in any protobuf runtime, and no
build step.

The same bytes ship inside the container image, at
`/usr/local/share/avroc/ir.binpb` — the path
[`docs/container/SPEC.md`](docs/container/SPEC.md#the-ir-filedescriptorset) fixes,
so a `COPY --from` can name it. The release asset and the file in the image are two
ways of getting one artifact, not two artifacts.

See [_A descriptor is readable by a program with no
bindings_](docs/ir/SPEC.md#a-descriptor-is-readable-by-a-program-with-no-bindings)
for the worked example and what the set does and does not contain.