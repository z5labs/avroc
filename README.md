# avroc

A modular code generator for messages and services defined in [Avro IDL](https://avro.apache.org/docs/current/idl-language/).

## Features

- **Declarative manifest** — a project's generators and their configuration live in a checked-in `avroc.json` manifest, so generator selection and options are diffable, reviewable, and shared across a team and CI. `avroc init` scaffolds one to get started.
- **Dynamic generator discovery** — avroc discovers generator plugins on your `PATH` using the naming convention `avroc-gen-<name>`; a manifest entry's `name` resolves to the matching `avroc-gen-<name>` executable.
- **Reproducible generator acquisition** — `avroc get` resolves each generator's OCI image tag to an immutable digest, pulls it into a local cache without a Docker daemon, and pins it in a committed `avroc.lock`, so every developer and CI run uses the exact same toolchain.
- **Type validation** — avroc resolves all type references in your Avro IDL schemas and reports errors for any undefined types before invoking generators.
- **Value validation** — avroc validates field defaults and enum defaults against their declared types, catching mistakes (e.g. a `null` default on an `int` field) at generation time.
- **Parallel generation** — all generators run concurrently, so code generation scales with the number of plugins you use.

## Architecture

```
┌─────────────────────────────────────────────────────┐
│  avroc generate                                      │
│                                                      │
│  1. Read avroc.json manifest                         │
│  2. Resolve each generator to avroc-gen-<name>       │
│  3. Parse & validate the declared Avro IDL inputs    │
│  4. For each generator (concurrently):               │
│     a. Create a temporary Unix socket                │
│     b. Launch avroc-gen-<name> subprocess            │
│     c. Connect via gRPC (Generator service)          │
│     d. Send GenerateRequest  ──────────────────────► │  avroc-gen-<name>
│     e. Receive streamed GenerateResponse ◄─────────  │  (gRPC server on
│     f. avroc writes the streamed files               │   Unix socket)
└─────────────────────────────────────────────────────┘
```

Generators communicate with `avroc` over a gRPC `Generator` service defined in [`proto/`](proto/). This means you can write a generator in **any language** that supports gRPC — just name the executable `avroc-gen-<name>` and put it on your `PATH`.

## Installation

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

## Usage

avroc is driven by a project manifest (`avroc.json`) and exposes these commands:

```
avroc init        # scaffold a starter avroc.json (never clobbers an existing one)
avroc get         # resolve & pull generator images, pinning them in avroc.lock
avroc generate    # run the generators declared in avroc.json
```

### Manifest (`avroc.json`)

The manifest declares the input IDL files and the generators to run:

```json
{
  "inputs": ["schema.avdl"],
  "generators": [
    {
      "name": "go",
      "source": "ghcr.io/z5labs/avroc-gen-go",
      "version": "v0.1.0",
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
| `source` | generator | OCI image reference. Recorded for the containerized-generator workflow; today generators are run from `PATH`. |
| `version` | generator | OCI image tag. Recorded alongside `source`. |
| `out` | generator | Output directory (relative to the manifest). |
| `options` | generator | `key`/`value` generator options. |
| `inputs` | generator | IDL files specific to this generator, merged with the top-level `inputs`. |

> A generator whose `name` is not found on `PATH` is reported as an error. If such an entry
> also names an OCI `source`, avroc explains that containerized execution is not yet
> supported — that lands in a follow-on release.

### Acquiring generators (`avroc get` and `avroc.lock`)

`avroc get` reads the manifest and, for every generator that declares an OCI `source`, resolves
its floating `version` tag to an immutable `sha256:` digest, pulls the image into a local cache
(no Docker daemon required), and records the resolved digest in a committed **`avroc.lock`**
lockfile — the reproducibility record, analogous to `.terraform.lock.hcl`:

```bash
avroc get             # resolve, pull, and pin every OCI generator
avroc get -upgrade    # re-resolve floating tags to fresh digests and rewrite the lock
```

- **Reproducible by default.** When a lockfile already pins a generator's `name` + `source` +
  `version`, that digest is reused on rerun rather than re-resolving the tag, so an unchanged
  manifest + lockfile always acquires the same images. Use `-upgrade` to move to newer digests.
  The pinned digest is the platform-independent manifest the tag points at (the multi-arch index
  for multi-arch images), so a committed `avroc.lock` is identical across developer machines and
  CI regardless of OS/arch.
- **Verified.** Pulled content is fetched by digest and verified against it; a populated cache
  lets later runs proceed offline.
- **Cache location.** Images are cached under your user cache directory (`os.UserCacheDir()` /
  `avroc`, e.g. `~/.cache/avroc` on Linux). Override the location with the `AVROC_CACHE`
  environment variable.
- **Registry auth.** Authentication is resolved through the standard Docker keychain
  (`~/.docker/config.json` and platform keychains), so a prior `docker login ghcr.io` enables
  authenticated and private `ghcr.io` pulls with no extra configuration.

> Generators without an OCI `source` (PATH-based) are skipped by `avroc get`. Running the pinned
> images is handled by a follow-on release; today `avroc generate` resolves generators from
> `PATH`.

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

See the [`example/`](example/) directory for a working example.

## Built-in Generators

### `avroc-gen-go`

Generates idiomatic Go types with binary Avro serialization support.

| Option | Required | Description |
|---|---|---|
| `package_name` | Yes | The Go package name for all generated files. |
| `encoding` | No | Set to `single_object` to generate a `Fingerprint()` method on the primary record type for [Avro Single Object Encoding](https://avro.apache.org/docs/current/specification/#single-object-encoding). |

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

Generates [Avro JSON schema](https://avro.apache.org/docs/current/specification/#schema-declaration) files (`.avsc`). Named types are inlined on their first use and referenced by name afterwards.

No options required.

### `avroc-gen-pcf`

Generates [Avro Parsing Canonical Form](https://avro.apache.org/docs/1.12.0/specification/#parsing-canonical-form-for-schemas) files (`.avsc`). The output is a compact JSON representation with attribute names and type ordering normalized per the Avro specification. Named types are inlined on first use and referenced by their fully-qualified name on subsequent uses. The file content is written as exact canonical bytes — no trailing newline — so it can be used directly for fingerprinting.

No options required.

## Writing a Custom Generator

1. Create an executable named `avroc-gen-<name>` and put it on your `PATH`.
2. On startup, read the Unix socket path from `os.Args[1]`.
3. Start a gRPC server on that socket and register your implementation of the `Generator` service (see [`proto/generator.proto`](proto/generator.proto)).
4. Handle a `GenerateRequest` (options + schemas) by **streaming** the generated files back: each `GenerateResponse` carries a relative `path`, a chunk of `content`, and a `last` flag. avroc reassembles the chunks and writes the files, so the generator never touches the filesystem.

The protobuf definitions and generated Go stubs are in [`internal/avrocpb/`](internal/avrocpb/).