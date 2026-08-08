# Copilot Instructions

## Build & Test Commands

```bash
# Build
go build ./...

# Run all tests
go test ./...

# Run a single test
go test ./internal/avroc -run TestName

# Run with verbose output
go test -v ./...
```

## Architecture

avroc is a modular code generator for Avro IDL, following a plugin architecture inspired by protoc.

### Plugin Communication Flow

1. avroc creates a temporary Unix socket and starts a generator subprocess (`avroc-gen-<name>`), passing the socket path as its first argument
2. The generator listens on the Unix socket and registers its `Generator` gRPC service
3. avroc connects as a gRPC client, sends a `GenerateRequest` (schemas + output directory), and receives a `GenerateResponse` (output file paths)
4. Generators run concurrently via `sourcegraph/conc` pools

### Plugin Discovery

The CLI scans `PATH` for executables matching `avroc-gen-<name>`. Each discovered generator gets a `-<name>_out` CLI flag for specifying its output directory.

### Key Packages

- **`cmd/avroc/`** — CLI entry point
- **`cmd/avroc-gen-go/`** — Go generator plugin entry point
- **`internal/avroc/`** — Core CLI logic: plugin discovery, Avro IDL parsing, code generation orchestration
- **`internal/avroc-gen-go/`** — Go generator implementing the `Generator` gRPC service
- **`internal/cli/`** — Shared `cli.Context` type providing logger, environment, filesystem, and args
- **`avrocpb/`** — Generated protobuf code, public because third-party generators import it (do not edit)
- **`proto/`** — Protobuf definitions (edition 2023) for the `Generator` gRPC service

### Key Dependencies

- `github.com/z5labs/avro-go` — Avro IDL parser
- `google.golang.org/grpc` + `google.golang.org/protobuf` — gRPC plugin communication
- `github.com/sourcegraph/conc` — Structured concurrency for parallel generator execution

## Conventions

### License Header

All source files (Go, proto) must include the MIT license header:

```
// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT
```
