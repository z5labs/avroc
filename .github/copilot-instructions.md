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

avroc is a modular code generator for Avro IDL. A generator is an executable, not a server: there is no service, no socket and no port. `docs/plugin/SPEC.md` is normative.

### Plugin Invocation

1. avroc writes a `GenerateRequest` descriptor into a directory created for that one invocation
2. avroc execs `avroc-gen-<name> --descriptor <path> --out <dir> [--opt k=v ...]`, both paths absolute, and waits for it to exit
3. The generator writes whole files beneath `--out` and reports on stderr, which avroc passes through to its own unaltered and does not parse; the exit status is the whole of what avroc analyses
4. `--out` is a private, empty scratch directory, merged into the project's output tree only on a zero exit
5. Generators run concurrently via `sourcegraph/conc` pools

### Plugin Discovery

The CLI scans `PATH` for executables matching `avroc-gen-<name>`, earliest match wins. Which generators run, and with which options, comes from the checked-in `avroc.json` manifest.

### Key Packages

- **`cmd/avroc/`** — CLI entry point
- **`cmd/avroc-gen-go/`**, **`cmd/avroc-gen-json/`**, **`cmd/avroc-gen-pcf/`** — generator plugin entry points
- **`internal/avroc/`** — Core CLI logic: plugin discovery, Avro IDL parsing, code generation orchestration
- **`internal/avroc-gen-go/`** — Go generator; reads the descriptor it is handed and writes Go source beneath `--out`
- **`internal/plugin/`** — The generator's half of the CLI contract: argument vector, descriptor, output directory
- **`internal/cli/`** — Shared `cli.Context` type providing logger, environment, filesystem, and args
- **`avrocpb/`** — Generated protobuf code, public because third-party generators import it (do not edit)
- **`proto/`** — Protobuf definitions (edition 2023) for the resolved IR; protobuf is a schema language here, not a service definition language

### Key Dependencies

- `github.com/z5labs/avro-go` — Avro IDL parser
- `google.golang.org/protobuf` — the IR's schema language, and the descriptor's wire encoding
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
