# The generator plugin CLI contract

> **Stub.** This document has its shape and not its content.
> [#106](https://github.com/z5labs/avroc/issues/106) writes it, and deletes this
> blockquote and the comments below as it goes.

## Overview

<!-- What `avroc-gen-<name>` has to implement: an executable avroc finds on
     PATH, runs, and waits for. Disclaim the neighbours — what the descriptor
     contains belongs to ir/SPEC.md, and where the executable comes from in a
     container belongs to container/SPEC.md. Close with the formula: X questions
     belong there; Y questions belong here. -->

### Scope

<!-- A short paragraph of what is in, ending with a pointer to Out of Scope
     rather than a list of exclusions. -->

### Governing sources

<!-- Bulleted, prose, bare autolinks: POSIX utility argument syntax, the
     reproducible-builds SOURCE_DATE_EPOCH definition, and this repository's
     ir/SPEC.md for the value being passed. Followed by the ambiguity
     blockquote. -->

### Conformance language

**MUST**, **MUST NOT**, **SHOULD** and **MAY** are normative requirements on a
generator plugin and on the code invoking one, interpreted as described in
[CONVENTIONS.md](../CONVENTIONS.md). Everything else is descriptive.

## Host platform

avroc targets POSIX hosts. A plugin author writes against a POSIX filesystem
and a POSIX process model: an executable is a file carrying an execute bit, its
name is the whole of its name, `PATH` is colon-separated, and paths are rooted
at `/` (#104).

Windows is not a target, and dropping it is a decision rather than an omission.
avroc is distributed as a scratch base image that a user extends with their own
generators, or through the companion Dagger module, whose stages run in Linux
containers whatever the host. Neither path puts a Windows host in the position
of running a generator executable, so the cross-platform machinery that used to
sit behind discovery — a second executable test, an `.exe` suffix to strip
before a generator's name could be read — was surface with nobody behind it.

macOS and the other Unixes are targets in the sense that matters here: nothing
in this document distinguishes them from Linux. What is excluded is the
non-POSIX case, not everything that is not Linux.

## Discovery

<!-- The avroc-gen-<name> naming convention and its resolution against PATH.
     The host-platform decision is recorded above; discovery is written against
     it rather than restating it. -->

## Invocation

<!-- --descriptor <path> with - for stdin, --out <dir>, repeated --opt k=v.
     Record why a path is the default rather than stdin: it makes the bytes a
     plugin receives reproducible, and lets an author re-run a failing
     invocation by hand. -->

## The output directory

<!-- What a plugin may assume about the directory it is handed, and that
     merging, collision detection and stale-file pruning are avroc's
     responsibility rather than the plugin's. -->

## Exit codes and diagnostics

<!-- What zero and non-zero mean, and the stderr format avroc parses. -->

## Determinism

<!-- Identical descriptor and options produce byte-identical output: no embedded
     timestamps, hostnames or map-iteration order. Cite SOURCE_DATE_EPOCH. -->

## Capability negotiation

<!-- The --plugin-info handshake by which a plugin declares what it supports
     before being handed work. -->

## Out of Scope

<!-- One ### per non-trivial exclusion, each with an explicit Reason:
     paragraph — no transport, no plugin registry, no lockfile, no resolution
     protocol — and the reproducibility non-guarantee for PATH-supplied plugins:
     a plugin arrives on PATH by whatever means put it there, and the container
     is the reproducible path. Each goes above this catch-all as it is
     decided. -->

### Also out of scope

<!-- The list for exclusions too cheap to earn a heading of their own. -->

## Appendix: Mapping to Stories

| Section | Implemented by |
| --- | --- |
| _Document shape and stub_ | [#103](https://github.com/z5labs/avroc/issues/103) |
| [Host platform](#host-platform) | [#104](https://github.com/z5labs/avroc/issues/104) |
