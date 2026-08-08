# The resolved IR

> **Stub.** This document has its shape and not its content.
> [#105](https://github.com/z5labs/avroc/issues/105) writes it, and deletes this
> blockquote and the comments below as it goes.

## Overview

<!-- What the IR is: the value every generator plugin consumes, in any
     language. Disclaim the neighbours — how a plugin is invoked with it
     belongs to plugin/SPEC.md, and where the executable holding it comes from
     belongs to container/SPEC.md. Close with the formula: X questions belong
     there; Y questions belong here. -->

### Scope

<!-- A short paragraph of what is in, ending with a pointer to Out of Scope
     rather than a list of exclusions. -->

### Governing sources

<!-- Bulleted, prose, bare autolinks: the Avro specification, the protobuf
     proto3 language guide, and descriptor.proto. Each entry says what it is
     normative for and why it is needed. Followed by the ambiguity
     blockquote. -->

### Conformance language

**MUST**, **MUST NOT**, **SHOULD** and **MAY** are normative requirements on the
resolved IR and on the code producing and consuming it, interpreted as described
in [CONVENTIONS.md](../CONVENTIONS.md). Everything else is descriptive.

## protobuf is a schema language, not a service

<!-- No service definition, no server, no port, no lifecycle. Include the
     argument, because a reader arriving from protoc expects a service. -->

## The version field

<!-- The field itself, and the rule that a consumer reads it before processing
     anything and refuses a version it does not know. -->

## Compatibility

<!-- The asymmetry: unknown fields are ignored, unknown members of closed sets
     are rejected. Say which sets are closed. -->

## What *resolved* means

<!-- Every named-type reference carries a fully-qualified name; a generator does
     not build its own symbol table and does not re-derive namespace
     qualification. -->

## The nested tree is kept

<!-- Why avroc does not flatten into a node set the way cpybkc does: an Avro
     schema is a tree, cpybkc's compiled automaton is a graph, and flattening
     here would add indirection to buy nothing. -->

## Out of Scope

<!-- One ### per non-trivial exclusion, each with an explicit Reason:
     paragraph, then a catch-all ### Also out of scope list. -->

## Appendix: Mapping to Stories

| Section | Implemented by |
| --- | --- |
| _Document shape and stub_ | [#103](https://github.com/z5labs/avroc/issues/103) |
