# The container base-image contract

## Overview

Adding a generator to avroc means building an image: a multi-stage Dockerfile
that compiles the generator, copies it onto a directory already on `PATH`, and
inherits the avroc CLI as its entrypoint. That is the whole extension
mechanism, and it is why this project needs no plugin registry, no lockfile, no
OCI fetching and no resolution protocol — `COPY` and `FROM` already are one.

The consequence is that the published image is a **public contract**, not just a
convenient way to ship a binary. A Dockerfile in somebody else's repository
names a path in it, and that path cannot move without breaking their build. This
document is what those Dockerfiles are entitled to rely on, and — just as
importantly — what they are not.

It is the deployment half of the [plugin contract](../plugin/SPEC.md). That
document says a generator is found by searching `PATH` for `avroc-gen-<name>`,
which is true on a laptop with no container in sight; this one says which
directory is on that `PATH` inside the published image, who owns it, and as
which user the process searching it runs. What a plugin *receives* once invoked
is the [resolved IR](../ir/SPEC.md)'s, and is not restated here either. What is
in the descriptor belongs there, how an executable is invoked and judged to have
succeeded belongs in the plugin contract, and where that executable comes from
belongs here.

### Scope

In scope: what the published image guarantees to an image built `FROM` it — the
directory a generator is copied into and the `PATH` that makes it reachable, the
entrypoint, the working directory a project is mounted at, the user and UID the
process runs as together with the guidance for overriding it with
`--user $(id -u):$(id -g)`, whether a shell is present and what follows from the
answer, the path the IR `FileDescriptorSet` ships at, the tags a derived image
may pin, and which of these are covered by a compatibility guarantee and which
are implementation detail that may change without notice.

Out of scope, with reasons, in [Out of Scope](#out-of-scope).

### Governing sources

- **OCI Image Format Specification** — normative for what an image *is*, and so
  for what a guarantee about one can be made about at all. It fixes the terms
  this document uses for layers, manifests, digests and platforms.
  <https://github.com/opencontainers/image-spec/blob/main/spec.md>
- **OCI Image Configuration** — normative for `Entrypoint`, `Cmd`, `User`, `Env`
  and `WorkingDir`, which are precisely the fields a derived image inherits and
  the ones this contract makes promises about. Every promise below is a promise
  about a value in that structure or about a file in the filesystem it
  describes.
  <https://github.com/opencontainers/image-spec/blob/main/config.md>
- **Dockerfile reference** — the reference for the `FROM` and `COPY --from`
  forms the [worked example](#worked-example-adding-a-generator) uses, and for
  how a derived image's `USER` and `ENTRYPOINT` interact with the base image's.
  It is cited as the builder syntax the examples are written in, not as a
  standard this image claims conformance to.
  <https://docs.docker.com/reference/dockerfile/>
- **[`plugin/SPEC.md`](../plugin/SPEC.md)** — normative for what belongs in the
  plugin directory. That an executable there is named `avroc-gen-<name>`, is
  found by an ordered `PATH` search, and behaves a particular way when run is
  that document's; this one only says where it goes.

> **Ambiguity:** the OCI specifications define the artifact; the Dockerfile
> reference describes one widely used builder for it, and is not a standard.
> Where they differ, this document states the guarantee in OCI terms — those are
> what a Podman, Buildah or Kaniko user gets too — and treats Dockerfile syntax
> as the illustration rather than the promise. Where this document appears to
> contradict [`plugin/SPEC.md`](../plugin/SPEC.md) about what an executable in
> the plugin directory must do, that document wins and this one has a bug.

### Conformance language

**MUST**, **MUST NOT**, **SHOULD** and **MAY** are normative requirements on the
published avroc image and on images derived from it, interpreted as described in
[CONVENTIONS.md](../CONVENTIONS.md). Everything else is descriptive.

## The plugin directory

The plugin directory is **`/usr/local/bin`**, and it is on `PATH` (#126).

That is the whole of the promise a derived Dockerfile depends on, so it is worth
stating what each half of it buys. The path is what a `COPY` line writes; the
`PATH` membership is what makes the copied file reachable by the name the
manifest asks for. Either one alone is useless, which is why they are one
requirement here rather than two facts in different sections.

The image **MUST** set `Env` such that `PATH` contains `/usr/local/bin`, and the
directory **MUST** exist in the base image even when it is empty, so that a
`COPY` into it never depends on the builder creating it. A derived image
**MUST NOT** remove the directory from `PATH` or shadow it with an earlier entry
containing an executable of the same name; the plugin contract's rule that
[the earliest `PATH` match
wins](../plugin/SPEC.md#discovery) is exactly what makes that a way to silently
substitute a generator.

An executable copied there **MUST** be executable by the [image's
user](#the-user) and **MUST** be a statically linked native executable for the
image's platform. It **MUST NOT** be a script: there is [no shell](#no-shell)
and no interpreter, so a `#!` line names a file that does not exist and the
generator fails to start with an error that names the interpreter rather than
the plugin. The plugin contract deliberately allows a shell script to be a
first-class plugin; that freedom is real on a host and is spent inside this
image, and the trade is bought back by the image having no attack surface to
speak of.

**Stability.** `/usr/local/bin` is covered by the [compatibility
guarantees](#compatibility-guarantees) below: it does not move within a major
version, and the release that moved it would ship both paths for a full minor
release before the old one disappeared. It is stated here rather than only there
because a path in a stranger's Dockerfile is not a detail they should have to go
looking for a guarantee about.

`/usr/local/bin` is chosen over an avroc-specific directory precisely because it
is not avroc-specific. It is the conventional destination for a locally
installed executable on a Unix-alike, it is what a `COPY --from=build`
line reads naturally against, and a reader who has never seen this document
guesses it correctly. An `/opt/avroc/plugins` would have been equally stable and
would have taught every adopter one more thing.

### The CLI's own path is not part of the contract

Where the `avroc` executable itself lives inside the image is **implementation
detail**. A derived image reaches it through the [entrypoint](#the-entrypoint),
never by path.

Keeping it out of the contract is deliberate: the image is built by a shared
pipeline archetype that has its own opinion about where a binary goes (#126),
and pinning the CLI's path here would make a promise to strangers out of a
detail of this repository's build. Nothing a derived image legitimately does
requires knowing it.

## The entrypoint

The image's `Entrypoint` is the avroc CLI, and its `Cmd` is empty (#126). The
arguments a caller passes to `docker run` are therefore avroc's arguments:

```console
$ docker run --rm -v "$PWD:/work" ghcr.io/z5labs/avroc:v0 generate
```

A derived image **MUST NOT** replace or clear `Entrypoint`. That is the one
edit which turns a derived image into a different program wearing avroc's
filesystem — the plugin it added would no longer be run by avroc, and the
`FROM` would be doing nothing but supplying a base. A derived image **MAY** set
`Cmd`, which is how a generator image supplies default arguments while leaving
a caller free to override them (#127).

A derived image **MUST NOT** rely on `Entrypoint` having any particular
*value*, only on its behaviour: it accepts avroc's arguments. The array itself
is [implementation detail](#the-clis-own-path-is-not-part-of-the-contract),
which is what allows the CLI to move without a major version.

## The working directory

The image's `WorkingDir` is **`/work`** (#126), and a caller mounts the project
to be generated there. avroc resolves the relative paths in a project's manifest
against the working directory, so a mount at `/work` and no further arguments is
the shortest complete invocation:

```console
$ docker run --rm -v "$PWD:/work" ghcr.io/z5labs/avroc:v0 generate
```

`/work` **MUST** exist in the base image and **MUST** be owned by the [image's
user](#the-user), so that generation into an unmounted `/work` works and a
caller who mounts over it inherits nothing surprising. It is covered by the
[compatibility guarantees](#compatibility-guarantees).

A derived image **MAY** change `WorkingDir`, and a caller **MAY** override it
with `--workdir`. Neither is expected: `/work` is a mount point, and a project
that wants a different one passes different paths to avroc instead.

## The user

The image runs as UID **65532**, GID **65532** — `User` is the literal string
`65532:65532` (#126). It is not root, and there is no `/etc/passwd` entry for
it.

### Why the UID is pinned rather than allocated

The number is part of the contract, not an artifact of whichever `useradd` ran
last. Three things need it and none of them can ask the image what it is:

- A derived Dockerfile writes `COPY --chown=65532:65532` to hand ownership of a
  copied plugin to the runtime user. `--chown=avroc` would need a name the
  image has no passwd file to resolve.
- A Kubernetes `securityContext` writing `runAsUser: 65532`, or a policy
  admitting only known non-root UIDs, is written against a number in a manifest
  a long way from this repository.
- A caller reasoning about the ownership of files that appear in a bind mount is
  reasoning about a number, because that is all their host kernel ever sees.

An allocated UID would be a value that could differ between two rebuilds of the
same tag, which is precisely the kind of change [Tags and what pinning one
buys](#tags-and-what-pinning-one-buys) promises does not happen. 65532 is the
conventional non-root UID for a distroless-style image; it is chosen for being
the number other people's tooling already expects.

### What it means for ownership

The [plugin directory](#the-plugin-directory) and the [working
directory](#the-working-directory) are owned by 65532:65532 in the base image. A
derived image copying a plugin in **SHOULD** use `--chown=65532:65532` and
**MUST** ensure the result is readable and executable by that user; a
world-readable, world-executable file satisfies this without the `--chown`,
which is what makes the omission a latent bug rather than an immediate one.

Files avroc writes are created by the process, so they are owned by whatever UID
the container is actually running as — 65532 by default.

### Writing files a caller can read

Generated output that lands in a bind mount is owned by the UID that wrote it,
and a host user who is not 65532 then owns none of it. Depending on the mount,
they may not be able to edit or delete it either.

A caller **SHOULD** therefore run as themselves:

```console
$ docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/work" \
    ghcr.io/z5labs/avroc:v0 generate
```

This is supported and is the recommended invocation whenever output is written
into a mount. avroc **MUST NOT** require the image's own UID: nothing in the
image is readable only by 65532, no directory a run needs to write is owned only
by it, and no code path looks the running user up in a passwd file it would not
find. An overridden UID is an ordinary configuration, not a workaround.

The default exists for the case where no host user is involved — a CI runner, a
Kubernetes job, a Dagger pipeline — where running as a pinned non-root UID is
what a policy wants to see.

## No shell

**There is no shell in the image**, and no package manager, no `cp`, no
`chmod` and no libc. The base is `scratch` plus the files this document names
(#126).

The consequence is a rule and not a nuance, because it is the difference between
a Dockerfile that builds and one that fails on its second line: **extension is
`COPY`-only.** A stage built `FROM` the avroc image **MUST NOT** contain a `RUN`
instruction — there is no shell for the shell form and no executable to name in
the exec form — and **MUST** do everything it needs with `COPY`, `COPY --from`,
`ENV`, `CMD`, `LABEL` and the other instructions that only edit metadata.

Everything that needs a shell belongs in an earlier stage, which is a full
distribution image and can do as it likes. That is not a restriction on what a
generator may be built with; it is a restriction on where the building happens,
and the multi-stage form the [worked
example](#worked-example-adding-a-generator) uses is how every image in this
model is written anyway.

Two consequences worth stating outright, because each has been discovered the
hard way by somebody:

- A plugin **MUST** be statically linked, as [The plugin
  directory](#the-plugin-directory) requires. For a Go generator that means
  `CGO_ENABLED=0`; a dynamically linked executable finds no loader and no libc,
  and fails with `no such file or directory` naming a file that is plainly
  there.
- `docker run --entrypoint sh` does not work, and neither does `docker exec`
  into a running container. Debugging is done by running the CLI with different
  arguments, or against an image built `FROM` this one with a busybox copied in
  for the purpose.

Keeping the shell out is the same decision as having no plugin registry: the
image's contents are exactly the executables somebody deliberately put there,
and there is nothing else in it to run, exploit or depend on by accident.

## The IR `FileDescriptorSet`

The image ships the IR's protobuf `FileDescriptorSet` at
**`/usr/local/share/avroc/ir.binpb`** (#113, #126). It is covered by the
[compatibility guarantees](#compatibility-guarantees).

It is there for a plugin author whose language has no protobuf code generation
available in the build: a `FileDescriptorSet` is enough to decode a descriptor
dynamically, so the file turns "my language has no avroc bindings" into a
runtime lookup rather than a blocker. What it contains, and which IR version it
describes, is [`ir/SPEC.md`](../ir/SPEC.md)'s (#113); that it is at a fixed path
inside the image is this document's, because a path is the only part of it a
`COPY --from` can name.

A derived image **MAY** copy it out into its own stage and **MUST NOT** modify
it in place.

## Tags and what pinning one buys

A derived Dockerfile names a tag in its `FROM` line, so the tag is as much a
part of this contract as the paths are. Four tags are published (#128), and a
digest is the fifth way to name the image:

| Reference | Example | Moves? |
| --- | --- | --- |
| Full version tag | `v0.2.0` | Never |
| Minor tag | `v0.2` | On each patch release of that minor |
| Major tag | `v0` | On each release within that major |
| Rolling tag | `latest` | On each release |
| Digest | `@sha256:…` | Never, by construction |

A published full-version tag **MUST NOT** be repointed at a different manifest
after it is published — not for a rebuild, not for a base-image refresh, not to
correct a broken release. A release that has to be corrected gets a new version
number. That is the promise that makes `v0.2.0` mean something across a rebuild:
two builds naming it resolve to the same digest, forever, or the tag was a lie.

The other three **MUST** move, and moving is what they are for. `v0` picks up
every fix in the major version and, by the guarantee below, keeps every path
this document names; that is the right pin for a derived image that wants
security updates without a Dockerfile change, and it is what the
[worked example](#worked-example-adding-a-generator) uses.

A digest is the only reference that pins the bytes rather than a promise about
them, and it is what to use when reproducibility is the requirement — the
position the plugin contract takes under [Plugin distribution, and
reproducibility](../plugin/SPEC.md#plugin-distribution-and-reproducibility).
Pinning a digest fixes every generator in the image along with avroc itself,
which is the whole reason this project has no lockfile.

Images are signed and carry provenance and an SBOM (#128), and a consumer can
verify a signature before trusting a tag. That verification is a thing a
consumer can perform, so it is in scope here; how the signature comes to exist
is not, and is under [Out of Scope](#how-the-image-is-built-and-published).

## Worked example: adding a generator

A complete multi-stage Dockerfile that builds a generator which is not one of
avroc's built-ins and copies it into the plugin directory. It is runnable as
written, with an empty build context (#129):

```dockerfile
# syntax=docker/dockerfile:1

FROM golang:1.25 AS build
WORKDIR /src

COPY <<'EOF' go.mod
module example.com/avroc-gen-hello

go 1.25
EOF

COPY <<'EOF' main.go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const info = `{"name":"hello","version":"0.1.0","ir_version":1,` +
	`"options":["greeting"]}`

func main() {
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "--plugin-info" {
		fmt.Println(info)
		return
	}

	descriptor, out, greeting := "", "", "hello"
	for len(args) > 0 && args[0] != "--" {
		if len(args) < 2 {
			fail("error: %s: missing value", args[0])
		}
		flag, value := args[0], args[1]
		args = args[2:]
		switch flag {
		case "--descriptor":
			descriptor = value
		case "--out":
			out = value
		case "--opt":
			key, v, ok := strings.Cut(value, "=")
			if !ok || key != "greeting" {
				fail("error: --opt %s: unrecognised option", value)
			}
			greeting = v
		default:
			fail("error: %s: unrecognised flag", flag)
		}
	}
	if descriptor == "" || out == "" {
		fail("error: --descriptor and --out are both required")
	}
	if _, err := os.Stat(descriptor); err != nil {
		fail("error: %v", err)
	}

	name := filepath.Join(out, "hello.txt")
	if err := os.WriteFile(name, []byte(greeting+"\n"), 0o644); err != nil {
		fail("error: %v", err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
EOF

RUN CGO_ENABLED=0 go build -trimpath -o /out/avroc-gen-hello .

FROM ghcr.io/z5labs/avroc:v0
COPY --from=build --chown=65532:65532 --chmod=0755 \
     /out/avroc-gen-hello /usr/local/bin/avroc-gen-hello
```

Build it, and run it against a project whose manifest asks for the `hello`
generator:

```console
$ docker build -t avroc-hello .
$ docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/work" \
    avroc-hello generate
```

Five things in it are the contract rather than the example, and each one is a
line that would be wrong in a Dockerfile written from habit:

- The final stage contains **no `RUN`**. Every command ran in the `golang`
  stage, which has a shell; the stage built `FROM` the avroc image only copies.
  See [No shell](#no-shell).
- `CGO_ENABLED=0` produces a static executable. Without it the build succeeds
  and the generator fails to start inside the image.
- The destination is `/usr/local/bin`, on `PATH`, and the filename is
  `avroc-gen-hello` — the name avroc searches for when a manifest asks for
  `hello`, per the [plugin contract](../plugin/SPEC.md#discovery).
- `--chown=65532:65532 --chmod=0755` hands the file to the [image's
  user](#the-user) as an executable. `COPY` otherwise preserves the source's
  ownership, which is root's in the build stage.
- `ENTRYPOINT` is not touched, so the image is still avroc — now with one more
  generator on its `PATH`.

The generator itself is deliberately trivial: it declares itself through
`--plugin-info`, rejects an option it does not know, checks the descriptor is
there and writes one deterministic file under `--out`. Everything it does is
required of it by the [plugin contract](../plugin/SPEC.md), and nothing it does
is required of it by this one — which is the point. This document's whole
involvement in the generator is the two lines that put it on `PATH`.

## Compatibility guarantees

Covered. Within a major version of the image, each of these holds, and a change
to any of them is a breaking change:

| Guarantee | Value |
| --- | --- |
| [The plugin directory](#the-plugin-directory) | `/usr/local/bin`, on `PATH` |
| [The entrypoint](#the-entrypoint) | Is the avroc CLI, and takes its arguments |
| [The working directory](#the-working-directory) | `/work`, existing and owned by the image's user |
| [The user](#the-user) | UID 65532, GID 65532, non-root, overridable |
| [No shell](#no-shell) | Absent; extension is `COPY`-only |
| [The `FileDescriptorSet`](#the-ir-filedescriptorset) | `/usr/local/share/avroc/ir.binpb` |
| [Tags](#tags-and-what-pinning-one-buys) | A published full-version tag never moves |

Not covered, and explicitly implementation detail. Depending on any of it is
depending on something that may change in a patch release, with no notice:

- The base image and everything in the filesystem other than the files named
  above — their existence, their contents and their paths.
- The path of the `avroc` executable itself, and the literal value of
  `Entrypoint`.
- The value of `PATH` beyond its containing `/usr/local/bin`, and the value of
  any other environment variable.
- Layer count, layer ordering, image size, build timestamps and every other
  label or annotation on the manifest.
- The set of platforms beyond `linux/amd64` and `linux/arm64` (#126).
- Which UID owns a file that is not in the plugin directory or the working
  directory.

### How a covered thing would change

Not by moving it. A covered path or value changes in a new major version, and
the transition **MUST** hold both forms simultaneously for at least one full
minor release of that new major: a moved plugin directory means both directories
exist and both are on `PATH`, with the old one deprecated in the release notes
and removed no earlier than the following minor release. A moved
`FileDescriptorSet` ships at both paths over the same window.

The overlap is the entire point. A derived Dockerfile is in a repository this
project cannot see, is built by a pipeline this project cannot warn, and fails
at `COPY` time with an error naming a path rather than a version. Giving it a
release in which both the old and the new form work is what turns that into a
deprecation notice somebody reads instead of a broken build somebody bisects.

## Out of Scope

### How the image is built and published

The build system, the platform matrix, emulated testing, and the machinery that
produces signatures, provenance and an SBOM are **not specified here** (#126,
#128).

Reason: none of it is visible to a Dockerfile that says `FROM`. This document
describes what a consumer may depend on, and the machinery producing the image
can be replaced wholesale without a single promise above changing — a contract
that described it would freeze an implementation by accident. The dividing line
is verifiability: that a signature *exists for a tag* is something a consumer
can check, so [Tags](#tags-and-what-pinning-one-buys) states it; how the signing
key reaches the pipeline is not.

### Where the image is published

That the image is published to `ghcr.io/z5labs/avroc` is **not a guarantee made
here**, and neither is the availability of any registry.

Reason: a registry location is a fact about where to find the image, not about
what is inside it. A mirror, an internal registry or an air-gapped copy serving
the same digest satisfies every requirement in this document identically, and a
consumer who has pinned a digest has pinned something no registry can change.
Writing the location into the contract would make relocating the project's
publishing a breaking change to its image, which is exactly backwards.

### The plugin CLI contract

The `avroc-gen-<name>` naming convention, the argument vector, the descriptor,
exit codes, the stderr diagnostic format, `--plugin-info` and the determinism
requirement are **not specified here**.

Reason: they are [`plugin/SPEC.md`](../plugin/SPEC.md)'s (#114–#120), and they
hold with no container involved — a generator on a developer's `PATH` obeys
every one of them. Restating any of them here would imply the contract is a
property of the image, leaving the plugin author who runs avroc from a
`go install` with no document that applies to them, and would create a second
description of a vector that already has one. The two documents meet at exactly
one point: this one says which directory is on `PATH`, that one says what is
found there and what happens to it.

### Also out of scope

- **The Dagger module** (#130) that runs avroc for a caller. It is a
  convenience over this contract rather than a contract of its own; what it
  needs to say, it says in its module comment and `dagger call --help`.
- **What a generator emits.** The files a plugin writes, in what language and
  with what API, are the plugin author's. An image is a delivery mechanism and
  has no opinion about its payload.
- **Orchestration.** Kubernetes manifests, Compose files, CI job definitions and
  cache mounts are a consumer's own. This document names the UID and the working
  directory they need and stops there.
- **Runtime resource limits.** Memory, CPU, PID and filesystem limits are set by
  whoever runs the container. avroc has no requirement to state beyond what any
  short-lived batch process needs.

## Appendix: Mapping to Stories

| Section | Implemented by |
| --- | --- |
| _Document shape and stub_ | [#103](https://github.com/z5labs/avroc/issues/103) |
| _This document_ | [#107](https://github.com/z5labs/avroc/issues/107) |
| [The plugin directory](#the-plugin-directory) | [#126](https://github.com/z5labs/avroc/issues/126) |
| [The entrypoint](#the-entrypoint) | [#126](https://github.com/z5labs/avroc/issues/126), [#127](https://github.com/z5labs/avroc/issues/127) |
| [The working directory](#the-working-directory) | [#126](https://github.com/z5labs/avroc/issues/126) |
| [The user](#the-user) | [#126](https://github.com/z5labs/avroc/issues/126) |
| [No shell](#no-shell) | [#126](https://github.com/z5labs/avroc/issues/126) |
| [The IR `FileDescriptorSet`](#the-ir-filedescriptorset) | [#113](https://github.com/z5labs/avroc/issues/113), [#126](https://github.com/z5labs/avroc/issues/126) |
| [Tags and what pinning one buys](#tags-and-what-pinning-one-buys) | [#128](https://github.com/z5labs/avroc/issues/128) |
| [Worked example: adding a generator](#worked-example-adding-a-generator) | [#129](https://github.com/z5labs/avroc/issues/129) |
| [Compatibility guarantees](#compatibility-guarantees) | [#126](https://github.com/z5labs/avroc/issues/126), [#128](https://github.com/z5labs/avroc/issues/128) |
| avroc's own generators as the first consumers of this contract | [#127](https://github.com/z5labs/avroc/issues/127) |
| Multi-platform build and publishing — out of scope, see above | [#126](https://github.com/z5labs/avroc/issues/126), [#128](https://github.com/z5labs/avroc/issues/128) |
| A convenience over this contract, with no spec of its own | [#130](https://github.com/z5labs/avroc/issues/130) |
| The plugin contract this one is the deployment half of | [#106](https://github.com/z5labs/avroc/issues/106) |
| Conventions this document follows | [#103](https://github.com/z5labs/avroc/issues/103) |
