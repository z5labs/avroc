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
built `FROM` it. Run one over the project in the working directory — `/work` is
where the image expects it:

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/work" \
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
- `gen/test_record.go` — Go types with `MarshalAvroBinary` / `UnmarshalAvroBinary` methods
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

| Option | Required | Description |
|---|---|---|
| `package_name` | Yes | The Go package name for all generated files. |
| `encoding` | No | Set to `single_object` to generate a `Fingerprint()` method on the primary record type for [Avro Single Object Encoding](https://avro.apache.org/docs/current/specification/#single-object-encoding). Every schema in the run must be rooted at a record; one rooted at an array, a map, a union or a primitive fails the run with a diagnostic naming that root, rather than generating without the fingerprint. |

**Generated types:**

| Avro type | Go type |
|---|---|
| `record` | `struct` with `MarshalAvroBinary` / `UnmarshalAvroBinary` |
| `enum` | `int` type with typed constants |
| `fixed` | `[N]byte` type |
| `union { null, T }` | interface with `Null` and `T` implementations |
| `string` | `string` |
| `int` / `long` | `int32` / `int64` |
| `float` / `double` | `float32` / `float64` |
| `boolean` | `bool` |
| `bytes` | `[]byte` |

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
5. Write every generated file beneath `--out`, report problems on stderr, and exit zero. A non-zero exit fails the run and nothing you wrote is adopted as output.

`--out` is a private, empty scratch directory for that one invocation, not the project's output directory: avroc merges it in only after a zero exit, which is what makes a partial failure harmless. Two runs over the same descriptor must produce byte-identical output.

The full contract — discovery, the argument vector, capability negotiation, the descriptor's lifetime, exit codes and the stderr diagnostic format, and the determinism a plugin must exhibit — is [`docs/plugin/SPEC.md`](docs/plugin/SPEC.md).

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