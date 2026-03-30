# avroc

A modular code generator for messages and services defined in [Avro IDL](https://avro.apache.org/docs/current/idl-language/).

## Features

- **Dynamic generator discovery** — avroc automatically discovers generator plugins on your `PATH` using the naming convention `avroc-gen-<name>`. No configuration file needed; just install a plugin and it is available immediately.
- **Type validation** — avroc resolves all type references in your Avro IDL schemas and reports errors for any undefined types before invoking generators.
- **Value validation** — avroc validates field defaults and enum defaults against their declared types, catching mistakes (e.g. a `null` default on an `int` field) at generation time.
- **Parallel generation** — all active generators run concurrently, so code generation scales with the number of plugins you use.

## Architecture

```
┌─────────────────────────────────────────────────────┐
│  avroc (CLI)                                         │
│                                                      │
│  1. Scan PATH for avroc-gen-* executables            │
│  2. Register -<name>_out / -<name>_opt flags         │
│  3. Parse & validate Avro IDL files                  │
│  4. For each active generator (concurrently):        │
│     a. Create a temporary Unix socket                │
│     b. Launch avroc-gen-<name> subprocess            │
│     c. Connect via gRPC (Generator service)          │
│     d. Send GenerateRequest  ──────────────────────► │  avroc-gen-<name>
│     e. Receive GenerateResponse ◄──────────────────  │  (gRPC server on
└─────────────────────────────────────────────────────┘   Unix socket)
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

```
avroc [options] <idl files...>
```

For each generator plugin discovered on `PATH`, avroc registers two flags:

| Flag | Description |
|---|---|
| `-<name>_out <dir>` | Output directory for the `<name>` generator. Passing this flag activates the generator. |
| `-<name>_opt <key>=<value>` | Generator option. Can be specified multiple times. |

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

Generate Go types, an Avro JSON schema file, and a Parsing Canonical Form file:

```bash
avroc \
  -go_out=./gen \
  -go_opt=package_name=mypackage \
  -json_out=. \
  -pcf_out=./pcf \
  schema.avdl
```

This produces:
- `./gen/test_record.go` — Go types with `MarshalAvroBinary` / `UnmarshalAvroBinary` methods
- `./test_record.avsc` — Avro JSON schema
- `./pcf/test_record.avsc` — Avro Parsing Canonical Form

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
4. Handle `GenerateRequest` messages (output directory, options, schemas) and return a `GenerateResponse` with the list of generated file paths.

The protobuf definitions and generated Go stubs are in [`internal/avrocpb/`](internal/avrocpb/).