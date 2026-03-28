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

- **`main.go`** — Entry point. Sets up signal handling, logger, and delegates to `avroc.Main`.
- **`internal/avroc/`** — Core CLI logic. Parses `PATH` to discover generator plugins (`avroc-gen-*` executables), registers `<name>_out` flags for each, and parses Avro IDL files using `github.com/z5labs/avro-go/idl`.
- **`proto/`** — Protobuf definitions (edition 2024) for the `Generator` gRPC service that generator plugins must implement. The `Generate` RPC accepts schemas + options and returns output file paths.

### Plugin Discovery

The CLI scans all directories in `PATH` for executables matching `avroc-gen-<name>`. Each discovered generator gets a corresponding `-<name>_out` CLI flag for specifying its output directory.

### Key Dependencies

- `github.com/z5labs/avro-go` — Avro IDL parser
