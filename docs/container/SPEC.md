# The container base-image contract

> **Stub.** This document has its shape and not its content.
> [#107](https://github.com/z5labs/avroc/issues/107) writes it, and deletes this
> blockquote and the comments below as it goes.

## Overview

<!-- What a Dockerfile building FROM the published avroc image may rely on, and
     why the image is a public contract rather than a convenient way to ship a
     binary. Disclaim the neighbours — what a plugin does once invoked belongs
     to plugin/SPEC.md, and what it is invoked with belongs to ir/SPEC.md. Close
     with the formula: X questions belong there; Y questions belong here. -->

### Scope

<!-- A short paragraph of what is in, ending with a pointer to Out of Scope
     rather than a list of exclusions. -->

### Governing sources

<!-- Bulleted, prose, bare autolinks: the OCI Image Format Specification, the
     Dockerfile reference, and this repository's plugin/SPEC.md for what lands
     in the plugin directory. Followed by the ambiguity blockquote. -->

### Conformance language

**MUST**, **MUST NOT**, **SHOULD** and **MAY** are normative requirements on the
published avroc image and on images derived from it, interpreted as described in
[CONVENTIONS.md](../CONVENTIONS.md). Everything else is descriptive.

## The plugin directory

<!-- The one stable path on PATH that a derived image copies a generator into,
     with its stability guarantee stated alongside it rather than
     separately. -->

## The entrypoint

<!-- The avroc CLI, and what a derived image must not do to it. -->

## The user

<!-- A stable non-root UID: why it is pinned rather than allocated, what it
     means for ownership of the plugin and output directories, and guidance on
     --user $(id -u):$(id -g) for writing files a caller can read. -->

## No shell

<!-- Stated plainly, with the consequence: extension is COPY-only and no RUN
     works in a derived stage. -->

## Tags

<!-- Which exist, and what pinning one promises across a rebuild. -->

## Worked example

<!-- A multi-stage Dockerfile that builds a custom plugin and copies it in,
     runnable as written. -->

## Compatibility guarantees

<!-- What is covered, what is explicitly implementation detail, and how a
     covered thing would change if it had to. -->

## Out of Scope

<!-- One ### per non-trivial exclusion, each with an explicit Reason:
     paragraph — the build machinery, the registry location, and the plugin CLI
     conventions — each above this catch-all as it is decided. -->

### Also out of scope

<!-- The list for exclusions too cheap to earn a heading of their own. -->

## Appendix: Mapping to Stories

| Section | Implemented by |
| --- | --- |
| _Document shape and stub_ | [#103](https://github.com/z5labs/avroc/issues/103) |
