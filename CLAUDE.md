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
- **`internal/avroc-gen-go/`** — Go generator plugin. Reads the descriptor it is handed and writes Go source beneath `--out`. A schema whose root is `array<T>` gets a **streaming reader and writer** for `T` as well as `T` itself (`stream.go`, #174, #175): `StreamEvents(r io.Reader) iter.Seq2[*Event, error]` and an `EventReader` embedding `*avro.ArrayReader`; `WriteEvents(w io.Writer, f func(*EventWriter) error, opts ...avro.ArrayWriterOption) error` and an `EventWriter` embedding `*avro.ArrayWriter`. One decision, `arrayStreamFor`, emits both, because a stream nothing here can produce and one nothing here can consume are each half a feature. They are wrappers and nothing else — avro-go owns the block framing, which is type-independent, so a copy emitted once per schema would be a copy to keep correct; the same split as single-object encoding, where avro-go owns the algorithm and the generator contributes the precomputed fingerprint. The item must be a type this generator wrote `MarshalAvroBinary` and `UnmarshalAvroBinary` for (a record, enum or fixed, or a reference to one), because those methods are the whole of what `avro.ArrayReader` and `avro.ArrayWriter` consume; `array<string>` and `array<array<…>>` generate what they always did, which is nothing.

  Three things about the writer are decisions rather than mechanics. **Whether blocks carry their size is the caller's**: a size prefix can only be written once the block is encoded, so it costs a bounded buffer and buys a reader that can `SkipBlock` past it, and the generator therefore emits neither choice — it passes `avro.ArrayWriterOption` through, leaving unsized-and-unbuffered the default. **`Write` is shadowed** where the reader's `Next` is promoted unchanged, so that a stream of `Event` holding only `Event` is the compiler's to enforce rather than a convention. **`Close` is not shadowed and not hidden**: an array is terminated by a zero-count block, so a writer that is never closed leaves a truncated array (`avro.ErrTruncatedArray` on read), and hiding that behind the wrapper would be the way to get it wrong — `WriteEvents` owns the close instead, and closes only when its callback returns nil. Block boundaries change the encoded *bytes*; the determinism rule constrains generated *source*, and neither the option nor the mode touches it.
- **`internal/avroc-gen-json/`** — Avro JSON schema generator plugin. Reads the descriptor it is handed and writes one `.avsc` per schema beneath `--out`.
- **`internal/avroc-gen-pcf/`** — Parsing Canonical Form generator plugin. Reads the descriptor it is handed and writes one canonical `.avsc` per schema beneath `--out`. Its files are `ir.CanonicalJSON`'s bytes verbatim — no trailing newline, no re-indentation — because they are fingerprint input rather than a rendering for a person.
- **`internal/plugin/`** — The generator's half of `docs/plugin/SPEC.md`: parsing the argument vector, reading the descriptor it names, and writing files beneath `--out`. Every generator here routes its `Main` through it and produces its output through `plugin.FileWriter` — one call per file, path and whole content — with `plugin.OutputDir` the implementation that owns the path check, the directory creation and the record of what was written. avroc's half — discovery and building the vector — is `internal/avroc`'s, and the two are separate on purpose: a third-party generator implements the contract without importing anything from this repository.
- **`internal/cli/`** — Shared CLI context type (`cli.Context`) providing structured logger, environment, filesystem, and args.
- **`internal/ir/`** — Operations every generator performs on the resolved IR: the repository's single Avro Parsing Canonical Form implementation (shared by `avroc-gen-pcf` and `avroc-gen-go`'s fingerprint), plus name and filename helpers. No symbol table, no namespace qualification, no primitive list. `ir.SchemaBaseName` is the one every generator's filename comes from, and it names the schema after what its **root type** is about: a named type is about itself, an array and a map are about what they contain (so `schema array<Event>;` is `event.go`, not the namespace's last component, #172), and anything else — a primitive, a union with no single subject — falls through to the additional types, then the namespace, then `schema`.
- **`internal/docs/`** — No implementation; it is where the published documentation is checked from. `TestEveryRelativeLinkInTheDocumentationResolves` walks every Markdown file in the repository and requires each relative link to resolve *from the file it is written in*, fragment included, because a link is the one part of a document that breaks silently — renaming a heading breaks every reference to it without touching the files carrying them. Links inside fenced blocks are skipped: `docs/CONVENTIONS.md` quotes the conformance-language template, whose `../CONVENTIONS.md` is a path for the *quoting* document to write and not one to resolve from there. Nothing external is fetched, so the check is the same offline.
- **`avrocpb/`** — Generated Go code from the protobuf definitions, and the only package here a third-party generator imports. Public rather than internal because the IR is a contract; do not edit the generated files directly.
- **`proto/`** — Protobuf definitions (edition 2023) for the resolved IR, and nothing else. protobuf is the IR's schema language, not a service definition language: there is no `service` here, and `avrocpb.FileDescriptorSet` publishes every file in this directory (#124).

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

There is one shape a generator's output takes, and no second one: every generator here writes whole files through `plugin.FileWriter` (#121, #122, #123). The chunk stream that preceded it is gone with the `Generator` service the chunk type came from — `plugin.MainStream`, `plugin.StreamGenerateFunc` and `internal/plugin/stream.go` were all deleted by #124.

### The output directory and the merge

`--out` is never the project's output directory. `internal/avroc/merge.go` creates a private scratch directory per invocation *inside* the project's output tree, hands that to the generator, and merges it in afterwards; `docs/plugin/SPEC.md`'s "The output directory" is normative.

- **Empty, always.** A plugin may assume it exists, is writable and is empty, and must not expect a file it wrote on a previous run to be there. That emptiness is the mechanism behind everything else: the set of files a run produced is exactly the set found in the directory afterwards, with no marker inside a file and no bookkeeping asked of the plugin. The collision check and #119 are derived from it too.
- **Merged only on a zero exit.** A failure or a cancellation discards the directory instead, so nothing a failing generator left behind reaches the project — which is what makes the partially written failure the contract permits harmless. The removal is deferred, so it covers cancellation as well.
- **Phases, and that is the point.** `generator.run` stops at a plan: `planMerge` resolves and checks every path the generator produced and reads and writes nothing. `mergeOutputs` then takes every generator's plan together, checks them against each other, and creates every destination directory the commit will need — all before a single file moves, so a path a generator should not have produced, or one two of them both produced, fails the run with nothing adopted as output and no existing file replaced. Only then does the commit phase run, and it does nothing but rename — atomic per file, so an interrupted merge leaves whole files rather than half-written ones. The scratch directory lives inside the output tree precisely so those are renames and not copies; the cross-filesystem fallback stages into the destination's own directory, renames, and removes its source, preserving both the atomicity and the move.
- **A collision fails the run.** Two generators producing the same destination is `checkCollisions`' (#118), and it is avroc's to detect because a generator cannot see another's scratch directory and is told not to try. It runs on the full set of plans, so the report — every colliding path sorted, each naming every generator that claimed it — is a function of the file sets and not of which generator finished first, and it is reached before either file has been written where a person would find it. `generateAll` is the ordering that makes that possible: every generator runs concurrently into its own scratch directory, and nothing merges until the slowest has finished.
- **avroc enforces the boundary rather than trusting it.** `safeOutputPath` rejects an absolute path or one that climbs out, and the walk refuses any non-regular entry. A symbolic link is the case a relative-path check alone does not catch — every path is beneath the scratch directory and only following the link leaves it — so links are refused, not resolved. `internal/plugin.OutputPath` keeps the same check on the generator side; the duplication is deliberate, because a third-party generator imports nothing from this repository.
- **Stale output is pruned against a record, not a guess.** `internal/avroc/prune.go` keeps `avroc.gen.json` beside `avroc.json` naming every file the last successful run merged, project-root-relative and sorted; after a merge, everything it names that this run did not produce is removed, and nothing else ever is (#119). It is one record at the project root rather than one per output directory, because a generator *deleted from the manifest* is the case a per-directory record cannot cover — avroc would never look there again. A missing record prunes nothing, which is the safe direction: a stale file survives one more run, and a file a person wrote is never removed on a hunch. Hence an output directory shared with hand-written source is supported rather than merely tolerated, and hence `avroc.gen.json` at the project root is reserved — a generator producing it is refused by `checkReservedPaths` in the same phase as a collision. The record is read *before* the pool, for the same reason the handshake is: a record avroc cannot make sense of fails the run with nothing generated. It is written *after* the prune, so a removal that fails leaves a record that still names the file and the next run tries again.

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

### Determinism

Two runs of a generator over the same descriptor produce byte-identical output
(`docs/plugin/SPEC.md`, "Determinism", #120). Generated code is a thing a project
commits, so output that changes when nothing changed turns every regeneration
into a diff. It holds regardless of the clock, the hostname, the user, the
working directory, the locale, the absolute paths in `--descriptor` and `--out`,
filesystem order and any concurrency — everything except `SOURCE_DATE_EPOCH`,
which is an input rather than an accident of the machine.

Three checks, because no one of them sees all of it:

- **`dagger call regeneration`** (`.dagger/main.go`) builds the four binaries and
  generates `example/` twice, in scratch containers that disagree about every one
  of those, then byte-compares the trees. Its second comparison is the first run
  against the committed `example/`, which is the round-trip that catches output
  nobody regenerated; both need the same binaries and worked example, so they are
  one function. It runs on every platform `docs/container/SPEC.md` publishes.
- **`TestGenerateIsDeterministic`** in each generator package runs the generation
  many times in one process. That is what exercises Go's randomised map iteration
  order, which is the usual way the rule gets broken and the one that breaks
  intermittently.
- **`internal/plugin.TestNoGeneratorReadsTheClock`** parses every generator's
  source and fails on any *reference* — called, assigned or passed — to
  something that could not give the same answer twice: `time.Now`,
  `os.Hostname`, `os.Getenv`, `os/user`, either random. Repetition cannot catch
  a clock read, because two runs a moment apart agree on the date.

`internal/plugin.SourceDateEpoch` is the only sanctioned way to get a timestamp:
it reads `SOURCE_DATE_EPOCH`, returns UTC, and reports a malformed value rather
than falling back to the clock. Nothing here needs it yet, and a generator that
grows a timestamp uses it rather than inventing its own.

### The published images

`.dagger/image.go` builds the base image `docs/container/SPEC.md` describes, and
that document is normative: `/usr/local/bin` on `PATH` holding the CLI **and no
generator**, the CLI as `Entrypoint` with an empty `Cmd`, `/work` as
`WorkingDir`, UID and GID 65532 owning both directories and running the process,
the IR `FileDescriptorSet` at `/usr/local/share/avroc/ir.binpb`, and a `scratch`
base — no shell, no libc, no package manager, so extension is `COPY`-only. The
one thing in the filesystem the document does not name is a 1777 `/tmp`, which
avroc needs because it writes each invocation's descriptor under `os.TempDir`.

`.dagger/generator_image.go` builds the three images that carry avroc's own
generators, each of them the base plus one executable in the plugin directory
(#127). The generators are deliberately not in the base: a built-in on `PATH`
because the pipeline put it there is not a consumer of the extension mechanism,
and the first person to discover that the mechanism needs a path the contract
does not promise would otherwise have been a stranger at their own
`docker build`. `dagger call generator-image --name go`, `... publish-generator`
and `... generator-bundle-image` — the last being all three combined by copying
each executable out of the image that publishes it, which is what an adopter
writes as `COPY --from`.

It is built here rather than by the devex `GoApp` archetype, whose image half
produces one scratch image per binary with `/app/<binaryName>` as entrypoint and
nothing else set; `image.go`'s package comment records why that was the choice
over extending `GoApp` upstream or accepting a non-scratch base (#126). The
check stages still route through the Z5Labs standard, so there is still one
definition of what "checked" means.

`dagger call image-contract` is the compatibility guarantees table executed
rather than read — the OCI configuration, an *exact* listing of every path in
the image with its owner and mode, and `help` through the entrypoint, which is
all a base holding no generator can be asked to do.
`dagger call generator-image-contract` is the other half: each generator image's
configuration inherited unchanged, its filesystem exhaustively equal to the
base's plus one executable (which is how "only `COPY` ran" becomes an assertion),
each image generating with its own generator through the inherited entrypoint,
and the combined image reproducing the committed `example/` byte for byte as 65532
and as an overridden UID. CI runs both on every pull request;
`dagger call publish --address <ref>` and `dagger call publish-generator --name
<name> --address <ref>` push one multi-platform index to the reference they are
given, and are what a person calls to put an image on a test registry.

`.dagger/worked_example.go` is the third of them, and its input is a document:
`dagger call worked-example` extracts the multi-stage Dockerfile from
`docs/container/SPEC.md`'s "Worked example: adding a generator", builds it, and
runs the image over the example project's schema (#129). The worked example is
the one thing here nothing else reads — the Go build, the tests and the two image
checks all pass on an example that stopped building releases ago — and it is the
first thing an adopter tries, so it is extracted rather than copied: a Dockerfile
in a `testdata` directory would be the one that is checked while the one in the
document is the one people read. The build stage goes to buildkit as committed,
with an empty build context, so the heredocs and the `CGO_ENABLED=0` are
exercised as written; the final stage is *interpreted* — its `COPY` read for the
flags and paths the document actually wrote and replayed against the base image
the pipeline just built — for `generator_image.go`'s reason, that a `FROM
ghcr.io/z5labs/avroc:v0` line names a published image and a pull request has to
check the base it built. Interpreting cannot be allowed to diverge silently, so
an instruction in that stage other than `COPY` is an error naming it rather than
a line with no effect, and `workedExample.rules` re-runs every one of those
requirements over a copy of the document broken in exactly that way — the same
shape as `tag-scheme`, and for the same reason: a check whose failure path has
never run is a check nobody knows the state of.

### The companion Dagger module

`daggerverse/avroc/` is a **second Dagger module**, separate from the root one at
`.dagger/` and published from this repository for other people's pipelines
(#130). A caller hands `Generate` a project directory and gets the tree avroc
left behind back as a `Directory`, having installed nothing and built no image:
`New` pulls the published base image, `WithGenerator` copies a generator's
executable out of the image that publishes it — `COPY --from`, without a
Dockerfile — and `WithGeneratorExecutable` does the same from a `File`, for a
generator that has no image yet. `Image` exposes what those composed.

It is a **convenience over `docs/container/SPEC.md`, not a contract**, so it gets
no `SPEC.md` (`docs/CONVENTIONS.md`, "What belongs here"): everything it does can
be written as `docker run --rm -v "$PWD:/work"`, and a spec for it would imply
the contract were a property of the module. What it needs to say it says in its
module comment and in `dagger call --help`. It is a separate module rather than
more functions on the root one because a caller who installs it should get the
one function they came for and not this repository's `ci`, `release` and
`publish`.

The two modules meet at exactly one place, `.dagger/companion_module.go`:
`dagger call companion-module` composes the three generators into the base image
the pipeline just built — both ways, from the generator images and from the built
executables — generates `example/` through the module, and requires the committed
tree back byte for byte. The images are injected through the module's `--image`
argument because its defaults name a *released* image, and a check on the last
release would keep passing through a pull request that broke this one; that is
the only reason that argument exists, and CI is what uses it. Both compositions
are checked against the same expected tree, which is what makes them
interchangeable rather than merely both present.

Its generated code is committed for the same reason the root module's is, and
`dagger develop -m ./daggerverse/avroc` regenerates it. Note that `dagger
develop` rewrites `go.mod` from the SDK's template and will lower dependency
versions Renovate has raised for a security advisory; both modules pin the same
set, and `git diff -- '*/go.mod'` after a regeneration is what catches it.

### Releasing the images

`.dagger/release.go` is the whole of a release, and `dagger call release` is the
one call `.github/workflows/release.yaml` makes (#128). **Whether this commit is
a release, and which tags it carries, is decided by the module from the refs at
HEAD** — never by an `if:` on a job and never by a tag assembled in YAML, because
a workflow that decided would be a second place `docs/container/SPEC.md`'s tag
table lives, in a file exercised once per release. A single canonical version tag
at HEAD is a release; a prerelease publishes only its own tag and moves none of
the others; two version tags is an error. `dagger call tag-scheme` runs that
derivation over a table of cases on every pull request, with every expected tag
written as a literal so the check cannot move with the constant it is checking.

Each published digest is then signed with `cosign`, keyless: the identity is the
release workflow, certified per run by the public sigstore CA from the OIDC token
`id-token: write` lets the run mint, so there is no avroc key for anybody to hold
or trust. Signing is recursive, so the published index and every per-platform
manifest under it are signed; the attestations — a SLSA v1 provenance statement
and one SPDX SBOM per executable per platform — go on the **index digest** and
only there. Nothing is ever attached to a tag.
The verifying commands are `docs/container/SPEC.md`'s "Verifying a signature",
and cosign itself is built by `dag.Go().Install` at a module version pinned in
`release.go`, so there is no tool image here to keep in step with an upstream tag.

### Key Dependencies

- `github.com/z5labs/avro-go` — Avro IDL parser
- `google.golang.org/protobuf` — the IR's schema language, and the descriptor's wire encoding
- `github.com/sourcegraph/conc` — Structured concurrency for parallel generator execution

## Conventions

- All source files (Go, proto) must include the MIT license header:
  ```
  // Copyright (c) 2026 Z5Labs and Contributors
  //
  // This software is released under the MIT License.
  // https://opensource.org/licenses/MIT
  ```
