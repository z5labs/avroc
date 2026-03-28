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
- **`internal/avrocpb/`** — Generated Go code from the protobuf definitions. Do not edit directly.
- **`proto/`** — Protobuf definitions (edition 2023) for the `Generator` gRPC service.

### Plugin Communication

1. avroc creates a temporary Unix socket and starts the generator subprocess (`avroc-gen-<name>`), passing the socket path as its first argument.
2. The generator listens on the Unix socket and registers its `Generator` gRPC service.
3. avroc connects as a gRPC client, sends a `GenerateRequest` (schemas + output directory), and receives a `GenerateResponse` (output file paths).
4. Generators run concurrently via `sourcegraph/conc` pools.

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
