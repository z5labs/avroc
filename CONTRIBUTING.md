# Contributing to avroc

## The pipeline

fmt, vet, golangci-lint and `go test -race` are defined once, in the root Dagger
module under [`.dagger/`](.dagger/). CI calls that module and so do you, which is
the point: there is no arrangement of local commands that passes while CI fails,
because they are the same functions.

Run the whole thing before pushing:

```sh
dagger call ci
```

That is the same call CI makes, with no arguments on either side. It runs the
four stages in parallel and reports every failure, not just the first.

### Running one stage

`dagger call ci` is the gate; the individual stages are for narrowing down what
it reported.

```sh
dagger call fmt    # gofmt, reported as a diff of what it would rewrite
dagger call vet    # go vet ./...
dagger call lint   # golangci-lint over ./... against .golangci.yml
dagger call test   # go test -race ./...
```

These are not a second definition of the pipeline. Each drives the same builder
`ci` drives with one stage enabled instead of four, against the same lint
configuration, so a stage that passes on its own passes inside `ci`.

`dagger check` runs them as a checklist, if you would rather see them together
than pick one.

### Regeneration

```sh
dagger call regeneration                          # every platform
dagger call regeneration --platform linux/arm64   # one of them
```

`regeneration` is avroc's own stage rather than part of the Z5Labs standard, so
CI runs it as a second `dagger call` beside `ci`. It builds the four binaries,
generates [`example/`](example/) twice, and requires the two trees to be
byte-identical — and identical to what is committed. See
[Determinism](#determinism) below for why both comparisons are one function.

### The images

```sh
dagger call image-contract                          # every platform
dagger call image-contract --platform linux/arm64   # one of them
dagger call generator-image-contract                # the images built FROM it

dagger call image export --path ./avroc.tar         # build one, to look at
dagger call generator-bundle-image export --path ./avroc-all.tar
```

`image` builds the image [`docs/container/SPEC.md`](docs/container/SPEC.md)
describes — the one other people's Dockerfiles say `FROM` — and
`image-contract` is that document's compatibility guarantees table executed
rather than read: the plugin directory and its place on `PATH`, the entrypoint,
the empty `Cmd`, the working directory, the pinned non-root UID, the absence of
a shell, and the `FileDescriptorSet`'s path.

The base ships the CLI and **no generator**. avroc's own three are images built
`FROM` it, one `COPY` each, exactly as a stranger's generator image is
(`generator-image --name go|json|pcf`), and `generator-image-contract` checks
them: the configuration inherited unchanged, a filesystem that is the base's plus
exactly one executable, each image generating with its own generator through the
inherited entrypoint, and the combined image reproducing the committed
[`example/`](example/) as itself and as an overridden UID.

It is a check because every one of those promises is depended on from a
repository this project cannot see and breaks without breaking anything here: an
image whose `PATH` lost `/usr/local/bin` runs avroc perfectly and fails at the
point where somebody else's generator is not found.

To run an image the way a consumer does, load the tarball and mount a project
at `/work`. Use the bundle rather than the base — `example/`'s manifest names
three generators, and the base carries none:

```sh
docker load -i ./avroc-all.tar
docker run --rm --user "$(id -u):$(id -g)" -v "$PWD/example:/work" <image> generate
```

`dagger call publish --address …` and `dagger call publish-generator --name …
--address …` push multi-platform indexes. Nothing but
[`.github/workflows/release.yaml`](.github/workflows/release.yaml) should call
either.

### Getting the tools

The pipeline needs the Dagger CLI and a container runtime (Docker or Podman).
Nothing else — not even a Go toolchain, since every stage runs in a container
built from the version in `go.mod`.

```sh
curl -fsSL https://dl.dagger.io/dagger/install.sh \
  | DAGGER_VERSION="$(jq -r .engineVersion dagger.json | tr -d v)" \
    BIN_DIR="$HOME/.local/bin" sh
```

The version comes out of `dagger.json` rather than being typed in, because the
`engineVersion` the module declares is what CI installs too. A CLI that
provisions a different engine is a difference between your machine and CI, which
is the one thing this module exists to prevent.

### After changing the module

`.dagger/dagger.gen.go` and `.dagger/internal/` are generated **and committed**.
Regenerate them after editing `.dagger/main.go` or `dagger.json`, and commit the
result alongside the change:

```sh
dagger develop
```

They are committed rather than ignored because Dagger is moving to requiring
generated code in the tree by v1.0, and because it means the module builds from
a checkout alone instead of only after somebody has run `dagger develop`.
[`.dagger/.gitattributes`](.dagger/.gitattributes) marks them
`linguist-generated`, so they stay collapsed in diffs and out of the
repository's language statistics.

A pull request whose `.dagger/main.go` or `dagger.json` moved without the
generated files moving with it is a tree that does not build. Bumping a
dependency pin counts: `dagger develop` rewrites `.dagger/internal/dagger/` from
the dependency's schema, so the pin and the generated bindings have to land in
the same commit.

`.dagger/` is its own Go module. `go build ./...` and `go test ./...` at the
repository root do not see it, and neither does `golangci-lint run` — which is
why `dagger call ci` is the check that covers both trees.

## Why the module wraps the Z5Labs standard pipeline

`ci` does not implement the four stages. It hands the source to
[`github.com/z5labs/devex/daggerverse/z5labs`](https://github.com/z5labs/devex/tree/main/daggerverse/z5labs)
and lets that module's `GoLib` archetype run them — the same standard every other
Z5Labs repository runs.

Reimplementing them here would be a second definition of what "checked" means in
a Z5Labs repository, and two definitions drift. A stage added to the standard
would silently not apply to avroc; a difference in how a stage is invoked would
show up as this repository disagreeing with every other one, for reasons nobody
wrote down. Wrapping costs one dependency and makes that impossible. The full
reasoning, including why the stage functions are not a fork of it, is in
[`.dagger/main.go`](.dagger/main.go)'s package comment.

Both dependencies in [`dagger.json`](dagger.json) are pinned to one `devex`
commit, so a bump has to move them together — otherwise a stage run on its own
stops being the stage `ci` runs.

### `GoLib`, even though avroc has four commands

`GoLib` is not a claim that avroc is a library. There are four `package main`
binaries under [`cmd/`](cmd/), so the usual reason to call `GoLib` — no main
package for the image half of the standard to act on — does not apply here. Two
reasons that do:

- **The check stages are identical either way.** `GoApp` and `GoLib` route fmt,
  vet, lint and `go test -race` through the same shared check, so the choice
  costs this pipeline nothing in coverage.
- **`GoApp`'s image half is not the image avroc needs.** It builds a `scratch`
  image per binary with `/app/<binaryName>` as entrypoint and nothing else: no
  `PATH`, no `USER`, no plugin directory. avroc's published image is a documented
  public contract that has to promise all three, because generator images are
  built `FROM` it and avroc discovers generators on `PATH`.

So this module takes the checks, and the base-image work owns how images get
built — whether by extending `GoApp` upstream in `devex`, building the image
here, or using a non-`scratch` base. Moving to `GoApp` is a change to which
factory `New` calls, plus dropping `.git` from its ignore list and restoring
`fetch-depth: 0` in the workflow, because a `GoApp` stamps binaries from the refs
at HEAD and does read git metadata.

## Linting

[`.golangci.yml`](.golangci.yml) is passed explicitly to the pipeline by the
module, so the configuration committed here is the configuration CI enforces. It
is not picked up from your `PATH`; running `golangci-lint` directly is running
something else.

It is written in the golangci-lint **v2** dialect — it opens with
`version: "2"` — because that is the major the standard pipeline runs. The
majors are not interchangeable: a v2 binary refuses a v1 file outright, before
any linter runs, and v1 refuses a v2 one. The `version` key is the config
schema's version, not the tool pin; the pin lives in
[`github.com/z5labs/devex/daggerverse/go`](https://github.com/z5labs/devex/tree/main/daggerverse/go),
which is where a bump belongs.

## Determinism

Two runs of a generator over the same descriptor produce byte-identical output.
[`docs/plugin/SPEC.md`](docs/plugin/SPEC.md)'s *Determinism* is normative and
binds third-party generators too; generated code is a thing a project commits,
so output that changes when nothing changed turns every regeneration into a diff
and makes the output useless as a thing to commit.

It is checked in three places, because no one of them can see all of it:

- **`dagger call regeneration`** generates [`example/`](example/) twice under
  deliberately different surroundings — different absolute paths for
  `--descriptor` and `--out`, working directory, temporary directory, `PATH`,
  user, hostname, locale and time zone — and byte-compares the trees. It holds
  `SOURCE_DATE_EPOCH` fixed across the two, because that one is an input to
  generation rather than an accident of the machine. It runs on every platform
  the image ships on.
- **The round-trip is the same stage.** Its second comparison is the first run
  against the committed tree, which is what fails when `example/` was not
  regenerated after a change to a generator. Both comparisons need the same four
  binaries and the same worked example, so they are one function rather than two
  that drift.
- **`go test ./...`** covers the parts a pipeline cannot. Each generator's
  `TestGenerateIsDeterministic` runs it many times in one process, which is what
  exercises Go's randomised map iteration order — the usual way the rule gets
  broken, and the way that breaks intermittently rather than every time. And
  `internal/plugin.TestNoGeneratorReadsTheClock` reads the source of every
  generator to prove none of them can reach a clock, a hostname, a username or a
  random value; two runs a moment apart agree on the date, so no amount of
  repetition would catch that one.

Where a generator genuinely cannot avoid a timestamp,
`internal/plugin.SourceDateEpoch` is the only sanctioned way to get one. It
reads `SOURCE_DATE_EPOCH` and never the clock, returns UTC because `TZ` is part
of the environment output may not vary with, and reports a malformed value as an
error rather than falling back — a build that quietly carries on being
nondeterministic is the failure the whole rule exists to prevent. No generator
here needs it yet.

The round-trip is also in the local verify list, so it runs without a container
runtime:

```sh
go build -o ./bin/avroc ./cmd/avroc
go build -o ./bin/avroc-gen-go ./cmd/avroc-gen-go
go build -o ./bin/avroc-gen-json ./cmd/avroc-gen-json
go build -o ./bin/avroc-gen-pcf ./cmd/avroc-gen-pcf
export PATH="$PWD/bin:$PATH"
(cd example && avroc generate)
git diff --exit-code -- example
```

## Conventions

All source files — Go and proto alike, including
[`.dagger/main.go`](.dagger/main.go) — carry the MIT licence header:

```go
// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT
```

Generated files are the exception; they carry whatever their generator emits.
