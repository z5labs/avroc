# The resolved IR

## Overview

The IR is a set of Avro schemas after every question the IDL left open has been
answered: every namespace applied, every named-type reference qualified, every
reference classified as a primitive or as a named type. It is what every
generator plugin consumes, in any language, and it is the *only* thing they
consume. A generator never reads an `.avdl` file, never parses Avro IDL, and
never re-derives anything the IR already carries.

That makes it the keystone of the three specs, and the reason it is written
first. Fixing what a resolved schema *is* before the [plugin
contract](../plugin/SPEC.md) fixes how one reaches an executable keeps the
semantics from being back-derived from an invocation convention, and keeps
every generator author from having to arrive at a second reading of the same
Avro IDL.

It is distinct from the [plugin contract](../plugin/SPEC.md), which governs how
these bytes reach an executable and says nothing about what is in them, and
from the [container contract](../container/SPEC.md), which governs where that
executable comes from. Delivery questions belong in the plugin spec, packaging
questions belong in the container spec, and what the value *is* belongs here.

### Scope

In scope: what a descriptor contains and what each part of it means — the
version identifying the contract it was written against, the options for the
invocation it belongs to, and the resolved schemas themselves, with their
fully-qualified names, their type structure, and the ordering that decides
where a named type is defined and where it is merely referenced. Together with
the protobuf schema language that carries all of it, and the compatibility
policy that governs changing it.

Out of scope, with reasons, in [Out of Scope](#out-of-scope).

### Governing sources

- **Apache Avro 1.12.0**, *Specification* — the normative definition of schema
  declaration, of names, namespaces, full names and aliases, and of the
  attributes each type carries. It fixes what a schema *is* and says nothing
  about how a code generator is handed one, which is why the rest of this list
  is needed. <https://avro.apache.org/docs/1.12.0/specification/>
- **Protocol Buffers Language Guide (proto3)**, *including "Updating A Message
  Type"* — normative for the wire format and for which schema edits are
  wire-compatible. That section is the floor
  [Compatibility](#compatibility) is built on: protobuf says what a decoder
  tolerates, and this document says what avroc additionally promises on top of
  it. The IR's own `.proto` files are written in edition 2023, whose defaults
  differ from proto3 on field presence; the compatibility rules cited here are
  the same under both.
  <https://protobuf.dev/programming-guides/proto3/>
- **`descriptor.proto`** — the definition of `FileDescriptorSet`, which is what
  plugin authors working in a language with weak protobuf tooling read a
  descriptor through (#113).
  <https://github.com/protocolbuffers/protobuf/blob/main/src/google/protobuf/descriptor.proto>

> **Ambiguity:** these sources do not overlap — Avro governs what a schema is,
> protobuf governs the container it travels in — so there is no conflict here
> to resolve. Where the IR appears to disagree with the Avro specification
> about naming, about which types exist, or about what an attribute means, the
> Avro specification wins and the IR has a bug.
>
> They do not cover everything between them. Neither source says what a
> *resolved* schema is: Avro specifies a document a person writes, in which a
> name may be relative and a reference may point at a definition further down,
> and protobuf specifies how to carry a message rather than what to put in one.
> [What *resolved* means](#what-resolved-means) is therefore this document's
> own, and so is [the nesting decision](#the-nested-tree-is-kept).

### Conformance language

**MUST**, **MUST NOT**, **SHOULD** and **MAY** are normative requirements on the
resolved IR and on the code producing and consuming it, interpreted as described
in [CONVENTIONS.md](../CONVENTIONS.md). Everything else is descriptive.

## protobuf is a schema language, not a service

protobuf is the schema language because the IR's consumers are third parties
writing in languages nobody here chooses. Go, Java, Python, C#, C++ and Rust
all have first-class protobuf support, and reach across unknown languages is
the only axis that matters for a value whose entire purpose is to be read by a
program this project did not write. Avro's one real advantage over it here
would have been self-description; publishing a `FileDescriptorSet` closes that,
and a plugin author whose language has weak protobuf tooling then reads a
descriptor dynamically with no build step at all (#113).

And it is the schema language and nothing else. **There is no service
definition, no server, no port and no lifecycle.** A descriptor is a value:
avroc writes it, a plugin reads it, and how it travels between them is the
[plugin contract](../plugin/SPEC.md)'s (#114). The IR **MUST NOT** define an
RPC service, and a plugin **MUST NOT** be required to listen on anything, or to
be handed a socket, in order to receive one.

That sentence is here because "protobuf" reads as "RPC" to most people who have
met it, and because avroc genuinely did ship a `Generator` gRPC service over a
Unix socket, with a subprocess handshake around it. A plugin author arriving
from `protoc`, or from that history, has every reason to expect a service, and
should learn otherwise from the document that defines the message rather than
by reading to the end and finding no service in it. Removing the service is
#124.

What the reversal buys is an IR that is a thing at rest. A descriptor can be
written to a file (#111), rendered as JSON and read (#112), committed, diffed,
pasted into an issue, and replayed against a plugin by hand a year later. None
of those is available to a stream: a message that exists only in flight between
two processes cannot be the thing anyone points at when a generator emits the
wrong output.

## What a descriptor carries

A descriptor is a single protobuf message carrying three things: the
[version](#the-version-field) identifying the contract it was written against,
the options for the invocation it belongs to, and the resolved schemas.

avroc writes one descriptor per generator invocation, and the options in it are
that generator's own (#111). They are opaque to the IR — a name and a value,
both strings, in an order a producer **MUST** make deterministic so that
identical inputs produce identical bytes. What any particular name means is the
generator's, and is [out of scope](#generator-option-meanings).

The whole descriptor carries that requirement, not only its options: two runs
over unchanged inputs **MUST** produce byte-identical descriptors (#111). It is
the same promise [`plugin/SPEC.md`](../plugin/SPEC.md)'s determinism section
makes of a generator's output, one step earlier in the pipeline, and it is needed
one step earlier because a plugin cannot be deterministic about an input that is
not — a descriptor that varied would make every regeneration a diff whatever the
plugins did with it. Every repeated field therefore carries a producer-chosen
order, and a producer **MUST NOT** let an unordered collection reach the encoding
in the order it happened to iterate.

A schema is a tree of types, and every type is exactly one member of a closed
set: the type constructors Avro declares — record, enum, fixed, array, map and
union — plus a reference, which names either an Avro primitive or a named type
the descriptor defines. Sort order on a record field is a closed set too, and
Avro's own: ascending, descending and ignore. A consumer **MUST** switch over
each closed set and **MUST** fail on a member it does not recognise;
[Compatibility](#compatibility) states that rule in general and says why it
points the opposite way from the rule about fields.

What each member carries follows the Avro specification's schema declaration
and is not restated here — a record's fields in declaration order, an enum's
symbols and its default, a fixed's size, an array's item type, a map's value
type, a union's branches in declaration order. What resolution *adds* to each
of them is [What *resolved* means](#what-resolved-means). How any of it is
spelled in protobuf — message names, field names, field numbers, and the
importable package the generated Go lands in — is #108's and #110's.

## The version field

The IR carries a single monotonic integer version. A producer **MUST** set it
on every descriptor it emits. A consumer **MUST** read it before processing
anything else, and **MUST** refuse a version it does not know — failing the
invocation with a diagnostic naming the version it found and the version it
understands — rather than proceeding on the parts it recognises (#109).

One integer, and not a major and a minor. The only question a consumer can act
on is whether it understands the descriptor in front of it, and a minor number
exists to let it answer "not entirely, but I will continue anyway" — which is
the failure this section exists to prevent. The cost of one integer is that
there is no way to say *newer, but you are fine*. That case is covered by not
arising: a change a consumer is genuinely fine with is an additive one, and an
additive one leaves the version alone.

Reading it *first* is the whole of the rule. A consumer that walks the schemas
and checks the version afterwards has already spent its diagnostics on a
document it was never entitled to read, so what a user sees is a complaint
about a type they cannot fix instead of a plugin that is too old for the avroc
in front of it.

### The version this document specifies, and when it advances

The version is **1**. A producer **MUST** write 1, and a consumer built against
this document **MUST** accept 1 and refuse every other value.

Zero is not a version and a producer **MUST NOT** write it. An absent integer
field decodes as zero, so reserving it is what makes a descriptor that carries
no version at all — one written by a producer predating this section, or one
whose version was lost in transit — refuse in exactly the same way as one from
a contract too new to read. A consumer **SHOULD** say which of the two it
found, because "carries no version" and "is version 9" send a user to
different places.

The version advances by **exactly one**, to the next integer, when a change is
breaking in the sense [Compatibility](#compatibility) defines, and at no other
time. Concretely, it advances when the IR schema removes a field or reuses its
number, changes what an existing field means, narrows or widens the values a
field may hold, or adds a member to a closed set — a type constructor, a
reference kind, a sort order. It does not advance for a field a consumer can
ignore and still handle the descriptor correctly, which is the whole of what
makes an additive change free.

One bump covers every breaking change since the last released version, because
the integer identifies a contract rather than an edit. A version already
advanced and not yet released absorbs the next breaking change without
advancing again; advancing per edit would number contracts nobody ever built
against, and a consumer's refusal would then name a version that never
existed.

The bump is the last step of a breaking change and not a routine one. A
producer and a consumer disagreeing about this integer is the *designed*
outcome — it is how a plugin too old for the avroc in front of it says so —
so raising it strands every generator built against the version before it
until each is rebuilt.

### What this version is not

It is not avroc's release version, and it is not the Go module tag of the
package the generated IR types live in (#110) — that follows Go's module rules
and moves for reasons, a dependency bump or a documentation fix, that say
nothing about the descriptor. One IR version outlives many of both.

## Compatibility

Within a version, every edit to the IR schema **MUST** be wire-compatible in
the sense of the protobuf language guide's *Updating A Message Type*, and a
consumer **MUST** ignore fields it does not recognise.

A change is breaking when a conforming consumer cannot ignore it and remain
correct. That is a statement about consequence rather than mechanism, and it is
deliberately stricter than protobuf's own rule: protobuf says what a decoder
can still parse, and this says what a generator can still be right about.

Breaking, and requiring the version to advance:

- Removing a field, or reusing its number for anything else.
- Changing what an existing field means, including narrowing or widening the
  values it may hold.
- Adding a member to a closed set — a type constructor, a sort order, or any
  other set the schema enumerates. An old consumer sees an unset choice where a
  new one sees a member, and emits code for a schema it has silently misread.
- Any addition a consumer must understand in order to stay correct, whether or
  not protobuf would call it compatible.

Not breaking: adding a field that a consumer ignoring it still handles the
descriptor correctly without.

### Unknown fields are ignored, unknown members are rejected

The asymmetry is worth stating on its own, because the two halves point in
opposite directions and both are load-bearing. A consumer **MUST** ignore an
unknown *field* and **MUST** reject an unknown *member of a closed set*.

A field a consumer has never seen is information it did not need; that is what
makes an additive change free, and it is the only reason the version can stay
put across most edits. A choice it has never seen is a fact about the schema it
cannot represent at all. A generator that treated an unrecognised type
constructor the way it treats an unrecognised field would emit a record with a
field quietly missing from it, and nothing downstream would say so — the user
finds out when the generated code fails to round-trip data somebody else wrote.

Rejecting means failing the invocation with a diagnostic, not skipping the type
and carrying on (#109, #115). Partial output from a descriptor a generator did
not fully understand is the outcome both halves of this rule exist to prevent.

## What *resolved* means

*Resolved* is a claim about what a generator does not have to do. The Avro IDL
a user writes leaves names to be qualified against an enclosing namespace, type
references to be matched against definitions that may appear later in the
document, and primitives to be told apart from named types by knowing Avro's
list of primitives. Every one of those is answered before the descriptor is
written.

Normatively:

- Every named type — record, enum, fixed — **MUST** carry its fully-qualified
  name: the namespace it resolved to together with its simple name, per the
  Avro specification's rules for full names. Aliases **MUST** be fully
  qualified the same way.
- Every reference to a named type **MUST** carry that same fully-qualified
  name. A reference **MUST NOT** be a bare identifier whose meaning depends on
  where in the document it appears.
- Whether a reference names an Avro primitive or a named type **MUST** be
  stated in the descriptor, and **MUST NOT** be left to be recovered by
  matching the name against Avro's list of primitives.
- Every reference **MUST** resolve to a named type the same descriptor defines,
  or to a primitive. A schema carrying a reference to neither is not resolved,
  and avroc **MUST** reject it rather than emitting it.
- Avro's rule that a named type is written out in full on first use and
  referred to by name thereafter fixes an order over the schemas. That order
  **MUST** be decided by the producer and carried in the descriptor (#108).

And the consequence, which is the point of the whole section:

> A generator **MUST NOT** build its own symbol table, and **MUST NOT**
> re-derive namespace qualification, primitive classification, or first-use
> ordering.

That prohibition is not tidiness. avroc has shipped three independent
resolvers, one in each of its Go, JSON and Parsing Canonical Form generators,
two of which independently implement Avro's canonical form in order to
fingerprint a schema. Three implementations of one algebra, in one repository
and one language, is duplication. The same three written by three authors in
three languages, each having read the Avro specification for themselves, is a
generator emitting a fingerprint that does not match the schema published
beside it — a mismatch that surfaces as a decode failure in somebody else's
system, months later. Resolution happens once, where the symbol table already
exists, or it happens differently everywhere (#108).

Walking the tree is not re-deriving, and the line matters because a consumer
plainly does have to walk. Indexing named types by full name, descending into a
record's fields, following a reference, and spelling a name the way the target
language spells names are operations on the message in hand: identical in every
language, and needing no Avro knowledge. What is abolished is the second list,
not the first.

## The nested tree is kept

The IR **MUST** carry structure by nesting — a field holds its type, an array
holds its item type, a union holds its branches — and **MUST NOT** flatten it
into a set of identified nodes with references between them.

This is the one place avroc deliberately parts company with
[cpybkc](https://github.com/Zaba505/cpybkc/blob/main/docs/ir/SPEC.md), whose IR
this document otherwise follows closely, so the divergence is recorded here as
a decision rather than left to be noticed in a diff.

cpybkc flattens because what its IR carries is not a tree. It holds a compiled
sequencing automaton — states, transitions, predicates, registers, bindings and
guards — which is a graph with cycles in it, and whose edges cross the record
structure freely: a predicate points at a field of the record it selects, and a
repetition may be counted by a value bound from an earlier record. Nesting
cannot express those, so a flat node set is the shape of the thing being
described.

An Avro schema is a tree. A record contains fields, a field has a type, and a
type may be another record; there is no edge in a schema that the nesting
cannot carry. Flattening it would replace that with an identifier space, an
index every consumer builds before it can walk anything, and a JSON rendering
(#112) markedly harder to read — and would buy nothing, because the one
relationship that leaves the tree, a reference to a named type, is already a
fully-qualified name after [resolution](#what-resolved-means). That is a
reference in the only sense a consumer needs.

Recursion is the case worth naming, because it is where "a tree" sounds wrong.
A record with a field referring to itself, directly or through another named
type, is expressible exactly because the reference is a name and not a nested
copy, so the descriptor stays finite. A consumer **MUST NOT** assume the type
graph is acyclic once references are followed, even though the message it
decoded is nested.

## Out of Scope

### Avro IDL and JSON schema syntax

The syntax a user writes is **not specified here**: the keywords of the IDL,
the shape of a `.avsc` document, the defaults either applies, and the errors a
malformed one produces.

Reason: it is the Avro specification's, cited above, and this document restates
none of it. The IR is the resolved artifact, and its whole value is that no
unresolved reference survives into it; a document specifying both the source
and the resolved form would describe every construct twice, once as written and
once as resolved, and the two descriptions would drift. What the syntax
*means* is still this document's, and that asymmetry is deliberate.

### How the IR is produced

The algorithm that turns Avro IDL into a descriptor — the parse, the symbol
table, the traversal order — is **not specified**.

Reason: it is an implementation, and its correctness criterion is that its
output satisfies this document (#108). Writing the algorithm down as well would
be a second description of the same requirement, drifting from the code at the
first optimisation, and no third party builds against it.
[CONVENTIONS.md](../CONVENTIONS.md) draws this line for every spec here.

### Generator option meanings

The names and values of the options a plugin accepts, and what any of them do,
are **not part of this document**.

Reason: the option pairs travel in the descriptor because avroc writes one per
invocation (#111), but the names are the generator's own vocabulary.
Specifying them here would grow this document a section every time some
generator gained a flag, and would put avroc in the position of validating
options for plugins it has never seen. Which options a plugin accepts, and how
it declares them, is [`plugin/SPEC.md`](../plugin/SPEC.md)'s (#116).

### Canonical form and fingerprints

A descriptor carries neither a schema's Parsing Canonical Form nor a
fingerprint derived from one.

Reason: both are functions of the resolved schema, defined by the Avro
specification, and a generator that needs one computes it. Carrying them would
be one fact written twice. The failure that motivated resolution in the first
place is two implementations of that function disagreeing, and the fix for that
is making their input identical rather than shipping one of the outputs:
`avroc-gen-pcf` and `avroc-gen-go` derive theirs from the same descriptor
(#108).

### Also out of scope

- **Delivery.** How a descriptor reaches a plugin — where it is written, the
  flag naming it, how long it lives — is
  [`plugin/SPEC.md`](../plugin/SPEC.md)'s (#111, #114).
- **Where the executable reading it comes from**, including the path the
  published `FileDescriptorSet` ships at inside the image, which is
  [`container/SPEC.md`](../container/SPEC.md)'s (#113).
- **A generated API.** What types a generator emits, what it calls them, and
  what methods they carry are the generator's own. This document constrains
  what a generator is handed, not what it produces.
- **The Avro IDL parser**, which is `github.com/z5labs/avro-go`'s.

## Appendix: Mapping to Stories

| Section | Implemented by |
| --- | --- |
| _Document shape_ | [#103](https://github.com/z5labs/avroc/issues/103) |
| [protobuf is a schema language, not a service](#protobuf-is-a-schema-language-not-a-service) | [#111](https://github.com/z5labs/avroc/issues/111), [#113](https://github.com/z5labs/avroc/issues/113), [#124](https://github.com/z5labs/avroc/issues/124) |
| [What a descriptor carries](#what-a-descriptor-carries) | [#108](https://github.com/z5labs/avroc/issues/108), [#110](https://github.com/z5labs/avroc/issues/110), [#111](https://github.com/z5labs/avroc/issues/111) |
| [The version field](#the-version-field) | [#109](https://github.com/z5labs/avroc/issues/109) |
| [Compatibility](#compatibility) | [#109](https://github.com/z5labs/avroc/issues/109) |
| [What *resolved* means](#what-resolved-means) | [#108](https://github.com/z5labs/avroc/issues/108) |
| [The nested tree is kept](#the-nested-tree-is-kept) | [#108](https://github.com/z5labs/avroc/issues/108), [#112](https://github.com/z5labs/avroc/issues/112) |
