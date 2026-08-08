# Spec conventions

Three of avroc's interfaces are things other people build against rather than
things it is free to change: the resolved IR, the generator plugin CLI contract,
and the container base-image contract. Each is harder to change than the code
behind it, so each gets a `SPEC.md` under this directory. They are linked from
the [README](../README.md), which is the only list of them — a second list here
would be a second answer to what the specs are, and two answers drift.

This document is what those specs agree on. It exists because the alternative is
three documents that each invent a shape and each define **MUST** in their own
wording, so that having read one teaches you nothing about how to read the next.

The model throughout is [cpybkc's `docs/CONVENTIONS.md`][cpybkc] — the sibling
project this one adopted its plugin and distribution model from. Where a
convention below has no stated reason, the reason is that cpybkc does it that
way, and agreement between the two repositories is worth more than a local
improvement.

[cpybkc]: https://github.com/Zaba505/cpybkc/blob/main/docs/CONVENTIONS.md

## What belongs here

A document under `docs/` specifies an interface a third party builds against.
That is the whole test, and it excludes more than it admits:

- **Implementation gets no spec.** The stage that parses Avro IDL and resolves
  its type references into IR has as its correctness criterion that its output
  satisfies `ir/SPEC.md`. A second document describing how it computes that
  would drift from the code the first time an optimisation landed, and nobody
  outside this repository builds against it.
- **Test infrastructure documents itself where it lives.** The `example/`
  round-trip is a corpus plus the command that regenerates it, checked by
  `git diff --exit-code`. It is documented with the corpus.
- **Conveniences over a contract are not contracts.** The Dagger module that
  runs avroc for a caller wraps the container contract; what it needs to say is
  said by its module comment and `dagger call --help`.

Something that fails the test and still needs writing down goes in a package
comment, in `CONTRIBUTING.md`, or in a comment in the file it is about. All
three are places this repository already keeps its reasoning.

## Conformance language

**MUST**, **MUST NOT**, **SHOULD** and **MAY** are normative requirements on the
interface a document specifies and on the code implementing it. Everything else
is descriptive. A requirement written in a spec is not a suggestion a downstream
story may weigh against ergonomics.

Those four, and no others. There is no **SHOULD NOT**, no **SHALL**, no
**REQUIRED**/**RECOMMENDED**/**OPTIONAL**, and no citation of RFC 2119 or RFC
8174. Citing 2119 would import eight keywords and 8174's rule about lowercase
uses in order to define the four that have been needed and four that have not,
and a keyword nobody has used is a keyword nobody has agreed the meaning of.

Keywords are **always bolded**, never bare. A document that separates a
normative `MUST` from an ordinary "must" by capitalisation alone depends on the
reader noticing capitalisation, and stops working the moment a sentence is
quoted into an issue or a review comment.

A spec restates this by reference and never by rewording, in a
`### Conformance language` subsection of its overview. What that sentence
repeats is *which words are normative*, so a reader never has to leave the
document to learn what to watch for. What is defined once, here, and never
repeated is what they mean, that there is no fifth, and that they are bolded:

```markdown
### Conformance language

**MUST**, **MUST NOT**, **SHOULD** and **MAY** are normative requirements on
⟨what the document specifies⟩, interpreted as described in
[CONVENTIONS.md](../CONVENTIONS.md). Everything else is descriptive.
```

## The shape of a spec

In this order:

1. `# Title`.
2. `## Overview`, opening by disclaiming the neighbouring specs' territory and
   closing with the formula — *X questions belong there; Y questions belong
   here.* A spec that never says what it is not about leaves the gap between two
   documents to be discovered by whoever falls into it.
3. `### Scope`, a short paragraph of what is in, ending with a pointer to `Out
   of Scope` rather than a list of exclusions. The exclusions are long and carry
   reasons; they do not belong in front matter.
4. `### Governing sources`, below.
5. `### Conformance language`, the reference sentence above.
6. The normative body, one `##` per topic.
7. `## Out of Scope`, near the back and before any appendix. One `###` per
   non-trivial exclusion, each with an explicit `Reason:` paragraph, then a
   catch-all `### Also out of scope` list for the cheap ones. An exclusion
   without a reason gets re-proposed every six months.
8. `## Appendix: Mapping to Stories`, below. Other appendices — test vectors, a
   cross-reference table — go before it as the document needs them.

Subsection titles that carry an argument are welcome throughout. *protobuf is a
schema language, not a service* and *The nested tree is kept* are load-bearing
headings rather than decoration: a heading that states its conclusion is a
heading somebody can find again.

### Governing sources

A bulleted list. Each entry names the source in bold and its title in italics,
then says in prose what it is normative *for* and why it is needed, with a bare
autolink where the source has a URL:

> - **Apache Avro 1.12.0**, *Specification* — the normative definition of
>   schema declaration, naming and the binary encoding. It fixes the schema
>   model and the wire format, and says nothing about how a code generator
>   represents either, which is why the rest of this list is needed.
>   <https://avro.apache.org/docs/1.12.0/specification/>

Prose rather than a table, because the useful part is the "normative for what",
and that does not fit in a cell.

The list is followed by a blockquote saying what happens when the sources
disagree:

> **Ambiguity:** where the sources differ this document does **not** choose a
> winner; it records the fork as a setting and states who takes which side.

A spec whose sources cannot conflict says that instead, in one line. Silence is
not an option: an adopter who hits a contradiction needs to know whether they
have found a bug in the spec or a fork the spec declined to resolve on purpose.

### Traceability

Two mechanisms:

- **Inline `(#NN)` citations** closing a normative statement, naming the stories
  the requirement lands on.
- **`## Appendix: Mapping to Stories`**, a `| Section | Implemented by |` table
  of anchor links against issue numbers.

There are no bracketed requirement identifiers — no `[IR-1]`, no `[PLUGIN-7]`.
They read as rigour and are not: a number is stable only until the requirement
is split, and nothing checks that one is used exactly once.

The table is the authoritative half. It also carries rows for stories that
produced **no** section — a removal, a build change, a published artifact —
mapped to what they did instead, so that the appendix answers "where did #NN
land" as well as "who implements this section". A section and its row move
together, in one change; a filled section whose row still says nothing is half a
change.

### What a spec does not carry

**No version number, no date, no status badge, no changelog.** Git is the
history and the Mapping to Stories table is the status; a hand-maintained `Last
updated:` line is a claim that goes stale in silence. The IR's own version field
is a different thing — a field in the protobuf, specified by `ir/SPEC.md`, which
versions the interface rather than the document.

**No reference by line number, to any file, ever.** Cite a section by name and
link its anchor. A line number into a thousand-line document rots the moment
either file is edited.

**Lines wrapped at 80 columns**, tables exempted because there is nowhere to
break them. Specs are reviewed as diffs, and a paragraph on a single line turns
a one-word change into an unreadable hunk.

## Stubs

A spec that has been given its shape but not its content carries a `> **Stub.**`
blockquote directly under the title, naming the story that writes it, and one
HTML comment per empty heading naming what belongs there. The story that fills
the document deletes the blockquote and the comments as it goes.

A stub contains **no conformance keyword** outside its own conformance-language
sentence. A stub is the outline nobody argued about; a normative claim in one is
a requirement nobody reviewed.

## `CLAUDE.md`

A spec is paired with a `CLAUDE.md` in the same directory carrying what an
implementer needs to know that is not a requirement on the interface. A spec
directory here gains one once there is an implementation to guide, and not
before — written against a stub it would be guidance about nothing.
