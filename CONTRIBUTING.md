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

`dagger check` runs all five as a checklist, if you would rather see them
together than pick one.

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

## What the pipeline does not check yet

The example round-trip — build the four binaries, run `avroc generate` in
[`example/`](example/), and confirm the committed output is unchanged — is not
part of `dagger call ci`. It runs in the repository's local verify list instead.

It comes back to CI the same way everything else does: as another function on the
root module invoked by another `dagger call`, never as raw Go steps beside it in
[`.github/workflows/build.yaml`](.github/workflows/build.yaml). It belongs with
the stage that generates twice and byte-compares, because that stage needs the
same four binaries and the same worked example — one function, not two that drift.

Until then, run it by hand before pushing a change to generation:

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
