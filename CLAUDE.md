# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

avroc is a modular code generator for messages and services defined in Avro IDL. It follows a plugin architecture inspired by protoc: generators are external executables discovered on `PATH` with the naming convention `avroc-gen-<name>`, and they communicate with avroc via a gRPC `Generator` service (defined in `proto/`).

## Build & Test Commands

```bash
# Build
go build ./...

# Run tests
go test ./...

# Run a single test
go test ./internal/avroc -run TestName

# Run with verbose output
go test -v ./...
```

## Architecture

- **`cmd/avroc/`** — CLI entry point. Sets up signal handling, logger, and delegates to `avroc.Main`.
- **`cmd/avroc-gen-go/`** — Entry point for the Go code generator plugin.
- **`internal/avroc/`** — Core CLI logic. Discovers generator plugins on `PATH`, registers `-<name>_out` flags for each, parses Avro IDL files, and orchestrates code generation.
- **`internal/avroc-gen-go/`** — Go generator plugin. Implements the `Generator` gRPC service, listens on a Unix socket, and handles `GenerateRequest`s.
- **`internal/cli/`** — Shared CLI context type (`cli.Context`) providing structured logger, environment, filesystem, and args.
- **`internal/ir/`** — Operations every generator performs on the resolved IR: the repository's single Avro Parsing Canonical Form implementation (shared by `avroc-gen-pcf` and `avroc-gen-go`'s fingerprint), plus name and filename helpers. No symbol table, no namespace qualification, no primitive list.
- **`avrocpb/`** — Generated Go code from the protobuf definitions, and the only package here a third-party generator imports. Public rather than internal because the IR is a contract; do not edit the generated files directly.
- **`proto/`** — Protobuf definitions (edition 2023) for the `Generator` gRPC service.

### Plugin Communication

1. avroc creates a temporary Unix socket and starts the generator subprocess (`avroc-gen-<name>`), passing the socket path as its first argument.
2. The generator listens on the Unix socket and registers its `Generator` gRPC service.
3. avroc connects as a gRPC client, sends a `GenerateRequest` (schemas + output directory), and receives a `GenerateResponse` (output file paths).
4. Generators run concurrently via `sourcegraph/conc` pools.

### The resolved IR

Generators are handed **resolved** schemas (`docs/ir/SPEC.md`). `internal/avroc/resolve.go` is the only place that qualifies a namespace, knows Avro's primitive list, or decides where a named type is written out in full versus referred to by name. Every named type carries `full_name`; every type reference is a `Reference` stating its `kind`. A generator that re-derives any of this is a bug.

### The IR version

Every descriptor carries `GenerateRequest.version`, the single monotonic integer `docs/ir/SPEC.md` specifies. `internal/ir.Version` is the constant avroc stamps on and every generator here understands; `ir.CheckVersion` is the **first** thing a generator's `Generate` does, before options and before any schema, so a descriptor from a contract the generator does not know fails the invocation instead of being read for the parts that look familiar.

Bumping `ir.Version` is the last step of a breaking change to the IR schema, never a routine one — it strands every generator built against the previous version. `ir.Validate` enforces the other half of the spec's asymmetry: unknown *fields* are ignored (protobuf drops them), unknown *members of a closed set* — type constructor, `TypeRefKind`, `SortOrder` — are rejected.

### The descriptor file

Every generator invocation gets one descriptor file: the `GenerateRequest` — IR
version, that generator's options, its resolved schemas — encoded by
`ir.MarshalDescriptor` and written by `internal/avroc.writeDescriptor` into a
directory created for that invocation alone, read-only, complete before the
generator starts and removed once it has exited. `docs/plugin/SPEC.md`'s
"Location and lifetime" is normative; nothing may derive meaning from the path.

The descriptor value is built once and then both written and sent, so the file is
the value the generator received rather than a second encoding that could drift
from it. Passing it as `--descriptor` is #114's; until then the same value still
travels over the gRPC service #124 removes.

**The bytes are deterministic**: two runs over unchanged inputs produce
byte-identical descriptors, because generated output is a thing a project commits
and a descriptor that varied would make every regeneration a diff. Every repeated
field is ordered by the producer before it reaches the encoding — manifest
options sort by key, schemas follow the manifest's input order — and
`ir.MarshalDescriptor` marshals deterministically. An unordered collection
reaching the encoder in Go's map iteration order is the way this gets broken, and
it breaks intermittently.

**Reading one is `avroc inspect <descriptor>`** (`-` for stdin), which renders it
as JSON via `ir.MarshalDescriptorJSON`: `docs/ir/SPEC.md`'s "A descriptor is
readable by a person". The rendering uses protobuf field names and is byte-stable
across builds — protojson varies its whitespace per binary on purpose, so the
output is re-indented through `encoding/json` before it is printed. It renders
regardless of the descriptor's IR version and without validating the closed sets:
looking is not consuming, and the descriptor worth reading is usually the one a
generator refused.

### The published `FileDescriptorSet`

`avrocpb.FileDescriptorSet` is the IR's self-description: `GenerateRequest`'s
file plus the transitive closure of its imports, in dependency order, computed
from the descriptors compiled into `avrocpb` rather than from a committed
`.binpb`. There is no second copy of the IR to keep in step, which is the whole
point — a hand-produced set goes stale invisibly.

The service files (`generator.proto`, `generate_response.proto`) are
deliberately absent: `docs/ir/SPEC.md` says the IR defines no service, and #124
removes both.

`internal/tools/ir-descriptor-set` writes it out, `dagger call ir-descriptor-set`
is how the pipeline builds `ir.binpb`, `.github/workflows/release.yaml` attaches
it to each release, and `docs/container/SPEC.md` fixes the path it ships at
inside the image. `avrocpb/descriptor_set_test.go` is the staleness gate: a new
`.proto` that nothing in the descriptor's import graph reaches fails there.

### Plugin Discovery

The CLI scans all directories in `PATH` for executables matching `avroc-gen-<name>`. Each discovered generator gets a corresponding `-<name>_out` CLI flag for specifying its output directory.

### Key Dependencies

- `github.com/z5labs/avro-go` — Avro IDL parser
- `google.golang.org/grpc` + `google.golang.org/protobuf` — gRPC plugin communication
- `github.com/sourcegraph/conc` — Structured concurrency for parallel generator execution

## Conventions

- All source files (Go, proto) must include the MIT license header:
  ```
  // Copyright (c) 2026 Z5Labs and Contributors
  //
  // This software is released under the MIT License.
  // https://opensource.org/licenses/MIT
  ```
