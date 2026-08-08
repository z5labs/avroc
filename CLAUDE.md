# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

avroc is a modular code generator for messages and services defined in Avro IDL. Generators are external executables discovered on `PATH` with the naming convention `avroc-gen-<name>`. avroc runs one with a descriptor and an output directory on its command line and waits for it to exit; there is no server, no socket and no port. `docs/plugin/SPEC.md` is normative.

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
- **`internal/avroc-gen-go/`** — Go generator plugin. Reads the descriptor it is handed and writes Go source beneath `--out`.
- **`internal/plugin/`** — The generator's half of `docs/plugin/SPEC.md`: parsing the argument vector, reading the descriptor it names, and writing files beneath `--out`. Every generator here routes its `Main` through it. avroc's half — discovery and building the vector — is `internal/avroc`'s, and the two are separate on purpose: a third-party generator implements the contract without importing anything from this repository.
- **`internal/cli/`** — Shared CLI context type (`cli.Context`) providing structured logger, environment, filesystem, and args.
- **`internal/ir/`** — Operations every generator performs on the resolved IR: the repository's single Avro Parsing Canonical Form implementation (shared by `avroc-gen-pcf` and `avroc-gen-go`'s fingerprint), plus name and filename helpers. No symbol table, no namespace qualification, no primitive list.
- **`avrocpb/`** — Generated Go code from the protobuf definitions, and the only package here a third-party generator imports. Public rather than internal because the IR is a contract; do not edit the generated files directly.
- **`proto/`** — Protobuf definitions (edition 2023) for the `Generator` gRPC service.

### Plugin Invocation

1. avroc writes the descriptor into a directory created for that one invocation.
2. avroc forks and execs `avroc-gen-<name> --descriptor <path> --out <dir> [--opt k=v ...]`, both paths absolute, and waits for it to exit. Nothing else is on the vector, and in particular nothing comes from the environment — `AVROC_GENERATOR_ARGS` is gone.
3. The generator writes its own files beneath `--out` and reports on stderr. A zero exit is the whole of the success signal; anything else fails the run, and nothing the generator left behind is adopted as output.
4. `--out` is a **private, empty scratch directory** for that one invocation, and only a zero exit gets its contents merged into the project's output tree. See "The output directory and the merge" below.
5. Generators run concurrently via `sourcegraph/conc` pools. Cancellation flows from the signal-based parent context through `exec.CommandContext`, and every child is waited on — on success, on failure and on cancellation.

### Diagnostics and the exit status

Standard error is the diagnostic channel, and avroc reads it rather than inheriting it. Every line lands in avroc's structured log attributed to the generator: a `<severity>: <message>` line at the level its severity names — `error`, `warning`, `note` — carrying a `severity` attribute, and **any other line verbatim at warning level**, as it arrives rather than held until the process exits. `internal/avroc/diagnostic.go` is the parser; `internal/plugin.DiagnosticHandler` is the producer every generator here logs through, so a generator in this repository is held to the format a third-party one is held to.

A failed invocation is reported as one of three things, because they need different responses: a generator that never ran, one that **exited non-zero** (a bug in the generator — the code is reported and nothing is concluded from its value), and one **terminated by a signal**, named as such (usually the run being cancelled or the machine running out of memory).

Discovery is a `PATH` search in order, and **the earliest match wins**, exactly as it does for a shell: prepending a directory is how an author shadows an installed generator with one under development. An empty `PATH` element is not the working directory.

What is not yet there, so that it is not mistaken for a gap: two generators producing the same output path is not detected (#118), and a file an earlier run produced that this one did not is not pruned (#119). The generators here still emit chunks internally through the `Generator` service's stream type, reassembled in `internal/plugin`; #121–#123 replace that with a plain write and #124 deletes the service.

### The output directory and the merge

`--out` is never the project's output directory. `internal/avroc/merge.go` creates a private scratch directory per invocation *inside* the project's output tree, hands that to the generator, and merges it in afterwards; `docs/plugin/SPEC.md`'s "The output directory" is normative.

- **Empty, always.** A plugin may assume it exists, is writable and is empty, and must not expect a file it wrote on a previous run to be there. That emptiness is the mechanism behind everything else: the set of files a run produced is exactly the set found in the directory afterwards, with no marker inside a file and no bookkeeping asked of the plugin. #118 and #119 are derived from it too.
- **Merged only on a zero exit.** A failure or a cancellation discards the directory instead, so nothing a failing generator left behind reaches the project — which is what makes the partially written failure the contract permits harmless. The removal is deferred, so it covers cancellation as well.
- **Two phases, and that is the point.** `planMerge` resolves every file and creates every destination directory first, so a path a generator should not have produced fails the run with the project tree untouched. Only then does the commit phase run, and it does nothing but rename — atomic per file, so an interrupted merge leaves whole files rather than half-written ones. The scratch directory lives inside the output tree precisely so those are renames and not copies; the cross-filesystem fallback stages into the destination's own directory and renames, preserving the same property.
- **avroc enforces the boundary rather than trusting it.** `safeOutputPath` rejects an absolute path or one that climbs out, and the walk refuses any non-regular entry. A symbolic link is the case a relative-path check alone does not catch — every path is beneath the scratch directory and only following the link leaves it — so links are refused, not resolved. `internal/plugin.OutputPath` keeps the same check on the generator side; the duplication is deliberate, because a third-party generator imports nothing from this repository.

### The capability handshake

Before any generation begins, avroc runs every generator the manifest resolved as
`avroc-gen-<name> --plugin-info` and reads the JSON capability declaration it
writes to standard output: `name`, `version`, `ir_version` and `options`
(`docs/plugin/SPEC.md`, "Capability negotiation"). A run fails there — with
nothing generated — when a generator will not answer, answers with something
unparseable or incomplete, declares a name that is not the one avroc resolved,
understands a lower `ir_version` than `ir.Version`, or declares a vocabulary the
manifest's options are not in. That early failure is the whole point: without it
a plugin too old for the IR fails late, as a confusing complaint about a type.

The two halves are `internal/plugin.Info`/`WriteInfo` (the generator writes it)
and `internal/avroc/plugininfo.go` (avroc reads it), and they deliberately share
no code and no constant — a third-party generator implements the handshake with
one `printf` and imports nothing from here.
`internal/avroc.TestTheDeclarationThisRepositorysGeneratorsWriteIsOneAvrocAccepts`
is what keeps them from drifting.

`options` present-and-empty ("I accept none") is a different declaration from
`options` absent ("you pass them, I decide"); `avroc-gen-json` and
`avroc-gen-pcf` declare the first.

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

The file **is** the value the generator received: its path is what `--descriptor`
names, so there is no second encoding of the same inputs that could drift from
it. The generator's options travel twice — in the descriptor, which
`docs/ir/SPEC.md` puts them in, and as `--opt` pairs, which `docs/plugin/SPEC.md`
configures a generator through. avroc emits the same pairs in the same order in
both places, and `internal/plugin` believes the command line.

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

### Key Dependencies

- `github.com/z5labs/avro-go` — Avro IDL parser
- `google.golang.org/protobuf` — the IR's schema language, and the descriptor's wire encoding
- `google.golang.org/grpc` — no longer a transport. Nothing dials or serves; what is left is the generated `Generator` service types the generators here still emit through, which #124 deletes
- `github.com/sourcegraph/conc` — Structured concurrency for parallel generator execution

## Conventions

- All source files (Go, proto) must include the MIT license header:
  ```
  // Copyright (c) 2026 Z5Labs and Contributors
  //
  // This software is released under the MIT License.
  // https://opensource.org/licenses/MIT
  ```
