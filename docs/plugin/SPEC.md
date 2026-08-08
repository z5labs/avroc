# The generator plugin CLI contract

## Overview

An avroc generator is an executable, not a server. avroc finds it on `PATH`,
runs it with a resolved descriptor and an output directory, and waits for it to
exit; the generator writes files and says what went wrong on stderr. That is the
entire contract, and it is deliberately small enough that a generator can be a
shell script.

The alternative — a plugin that speaks gRPC, as `protoc`'s do — costs a socket,
a port, a lifecycle and a protobuf runtime in every implementation language, to
buy a structured response that a process writing files into a directory does not
need. avroc shipped exactly that, and inside a container it is strictly worse:
two processes and a rendezvous where there was one process and a path. Removing
the service is #124.

**protobuf is the IR's schema language and nothing more: there is no gRPC
service, no server and no port.** The argument is the IR spec's, under [protobuf
is a schema language, not a
service](../ir/SPEC.md#protobuf-is-a-schema-language-not-a-service); it is
repeated here because a plugin author arriving from `protoc` comes looking for a
service definition, and should not have to read to the end of this document to
find out there is not one.

This is the plugin author's half of the picture. What a plugin *receives* is the
[resolved IR](../ir/SPEC.md), specified there and never restated here; where the
published image keeps plugins so that they are on `PATH` at all is the
[container contract](../container/SPEC.md)'s. What is in the descriptor belongs
there, which directories exist in an image belongs there, and how an executable
is found, invoked, and judged to have succeeded belongs here.

### Scope

In scope: how avroc discovers a generator and hands it work. The
`avroc-gen-<name>` naming convention and its resolution against `PATH`; the
argument vector — `--descriptor <path>` with `-` for standard input,
`--out <dir>`, and repeated `--opt k=v`; the encoding and lifetime of the
descriptor file; what a plugin may and may not do to the output directory; exit
codes and the stderr diagnostic format; the determinism a plugin **MUST**
exhibit; and the `--plugin-info` handshake by which a plugin declares what it
supports before it is handed work.

Out of scope, with reasons, in [Out of Scope](#out-of-scope).

### Governing sources

- **POSIX.1-2024 Base Definitions, chapter 12**, *Utility Conventions* —
  normative for the shape of the argument vector: option syntax, the `--`
  delimiter, and the meaning of `-` as an operand standing for standard input.
  It is cited as the convention the vector follows rather than as a standard
  avroc claims conformance to, because long options are not POSIX at all.
  <https://pubs.opengroup.org/onlinepubs/9799919799/basedefs/V1_chap12.html>
- **POSIX.1-2024 Base Definitions, chapter 8**, *Environment Variables* — the
  normative definition of `PATH`: a colon-separated list of directory prefixes,
  searched in order. Discovery is a `PATH` search and inherits its rules,
  including which entry wins when a name appears twice.
  <https://pubs.opengroup.org/onlinepubs/9799919799/basedefs/V1_chap08.html>
- **`SOURCE_DATE_EPOCH`**, *Reproducible Builds* — the reference for
  [Determinism](#determinism), and for why an embedded timestamp is the failure
  it is: it turns every regeneration into a diff and makes generated output
  useless as a thing to commit.
  <https://reproducible-builds.org/docs/source-date-epoch/>
- **`plugin.proto`**, *the `protoc` compiler plugin contract* — cited as the
  design this one deliberately does not follow. It is the reference point every
  prospective plugin author already has, so the differences are stated against
  it rather than described from nothing.
  <https://github.com/protocolbuffers/protobuf/blob/main/src/google/protobuf/compiler/plugin.proto>
- **[`ir/SPEC.md`](../ir/SPEC.md)** — normative for the value this contract
  moves. Everything about what a plugin reads out of the descriptor, including
  the version field it checks first, is that document's.

> **Ambiguity:** two names collide and neither is this document's to rename. A
> **descriptor**, in `--descriptor`, is a resolved avroc IR message; a
> **`FileDescriptorSet`** is protobuf's own reflection type, which avroc also
> ships (#113) so that plugin authors without protobuf codegen can read the
> first one. Where this document says "the descriptor" it always means the IR.
>
> Otherwise the sources do not overlap: POSIX governs the argument vector,
> Reproducible Builds governs timestamps, and the IR spec governs the bytes.
> Where this document appears to contradict [`ir/SPEC.md`](../ir/SPEC.md) about
> the descriptor, that document wins and this one has a bug.

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

A generator has a **name**: the `<name>` a project's manifest asks for. Its
executable **MUST** be named `avroc-gen-<name>`, and avroc **MUST** resolve a
name to an executable by searching `PATH` for exactly that filename (#114). The
name is the only thing that connects the two, in either direction: the suffix of
the filename is the generator's name, and nothing inside the executable is
consulted to discover it.

A `<name>` **MUST** be non-empty and **MUST NOT** contain `/`, and **SHOULD** be
lowercase ASCII letters, digits and hyphens. The first two follow from the name
being a filename component; the third is a convention rather than a rule,
because a name appears in a manifest, in a filename and in a log line, and a
name that needs quoting in one of those is a name that will eventually be
mistyped in another.

A candidate **MUST** be a regular file carrying an execute bit. There is no
second test — no extension, no magic number, no metadata file beside it — which
is what keeps a shell script with `chmod +x` a first-class plugin.

`PATH` is searched **in order, and the earliest match wins**, exactly as it does
for a shell resolving a command name. That rule is worth stating because it is
what makes a plugin under development shadow an installed one by prepending a
directory, which is how an author tests a change; the opposite rule would make
that gesture silently do nothing, and the difference is invisible in the output.

An empty `PATH` element **MUST NOT** be treated as the current working
directory, though POSIX permits it to be. avroc runs generators as a side effect
of a build command, and a generator picked up from whatever directory a user
happened to be standing in is an execution surface nobody chose. The working
directory is searched only when it appears in `PATH` as a written-out path.

avroc **MUST NOT** consult anything beyond `PATH` to find a generator — no
registry, no cache directory of its own, no lockfile, no download. That is not a
detail of discovery but the whole distribution position, and it is stated as
such under
[Plugin distribution](#plugin-distribution-and-reproducibility).

## Invocation

avroc runs a discovered executable once per generation, with this argument
vector and no other (#114):

```
avroc-gen-<name> --descriptor <path> --out <dir> [--opt k=v ...]
```

`--descriptor` and `--out` **MUST** each appear exactly once. `--opt` **MAY**
appear any number of times, including none, and avroc **MUST** pass the options
in the order the manifest declares them, so that the vector is a function of the
manifest rather than of a map iteration.

avroc **MUST** pass both paths as absolute paths, and a plugin **MUST NOT**
assume any particular working directory. Two runs of the same generator from
different directories are the same invocation, and a plugin that resolves a
relative path against `.` makes them differ.

avroc **MUST NOT** add arguments beyond those above, and in particular **MUST
NOT** take arguments from the environment. The `AVROC_GENERATOR_ARGS` variable,
which prepended arbitrary words to every generator's argument vector, is
removed (#114). It made the vector a function of the ambient environment, which
is the one input a manifest does not record and a reviewer cannot see — the
same hole [Determinism](#determinism) closes on the other side.

### Options

An option is a single `--opt` argument whose value is `k=v`: everything before
the first `=` is the key, everything after it is the value, and a value **MAY**
be empty or contain further `=` characters. A key **MUST NOT** be empty and
**MUST NOT** contain `=`.

Option keys are the generator's own vocabulary. avroc **MUST NOT** interpret
one, and a plugin **MUST** fail on a key it does not recognise rather than
ignoring it (#115). An ignored option is a line in a checked-in manifest that
reads as configuration and does nothing, and the user finds out by noticing that
the output never changed.

### Option form, and why the separated one

A plugin **MUST** accept the separated form — `--descriptor`, `--out` and
`--opt` each followed by their value as the next argument — and avroc **MUST**
emit that form. A plugin **MAY** additionally accept the joined
`--descriptor=<path>` form, and avroc **MUST NOT** rely on it.

One required form is what keeps the shell-script plugin honest: a `case`
statement over `"$1"` handles the separated form in three lines and needs no
option-parsing library. Requiring both would mean every plugin author writing
the split on `=` themselves, or reaching for a parser, in order to accept a
spelling avroc never emits.

A plugin **MUST** treat `--` as the end of options. This contract defines no
operands, and avroc **MUST NOT** pass any; the delimiter is required anyway so
that a plugin invoked by hand behaves the way its neighbours on the system do.

### The descriptor

The value at `--descriptor` is a resolved IR message in the protobuf binary wire
encoding, as [`ir/SPEC.md`](../ir/SPEC.md) defines it (#111). It is not JSON:
the JSON rendering (#112) is a thing a person reads, and a plugin that accepted
both would have to sniff which one it had been handed.

A plugin **MUST** read the descriptor and **MUST NOT** write to it, rename it,
or delete it. It **MUST NOT** derive anything from the file's name or its
directory — the path is a temporary location avroc chose, not a place a plugin
may hang meaning on. The file is gone once the plugin exits, as [Location and
lifetime](#location-and-lifetime) requires, so a plugin **MUST NOT** retain the
path for later use.

#### Location and lifetime

avroc **MUST** write the descriptor into a directory it creates for that one
invocation and nothing else, and **MUST NOT** share a descriptor file between two
generators, or between two runs. One file per invocation is what makes the bytes
attributable: a descriptor on disk belongs to exactly one generator and one run,
and cannot have been overwritten by whichever invocation happened to finish last.

The file **MUST** be written in full and closed before the generator is started.
A plugin therefore never observes a partial descriptor, and needs no protocol —
no lock, no sentinel, no retry — to find out whether the bytes it can see are all
of them.

avroc **MUST** remove the file, and the directory holding it, once the generator
has exited, whether it exited zero or not. That is the whole of the file's
lifetime. A plugin that wants the descriptor to outlive the invocation **MUST**
copy the bytes, and **MUST NOT** expect the path to resolve to anything
afterwards; what it will find there instead is unspecified, because avroc creates
the next invocation's directory by the same means.

avroc **SHOULD** create the file read-only. The prohibition above is on the
plugin and a mode bit does not enforce it — a plugin running as the user that
created the file can change the mode back — but it turns the accidental case, a
plugin opening the path for writing, into an error where the mistake is rather
than into a descriptor quietly different from the one avroc wrote.

Nothing here is a promise about the *path*. The directory avroc picks, the name
it gives the file and the suffix on that name are all implementation, and a
plugin reading any of them has broken the rule above rather than found a feature.

`--descriptor -` means the descriptor arrives on standard input, and a plugin
**MUST** accept it. avroc itself always passes a path, which is the point: a
file is bytes an author can save, check, diff, attach to an issue and feed back
to the plugin by hand a year later, and a re-run of a failing invocation is then
the same invocation rather than an approximation of it. The `-` form exists so
that a plugin is drivable from a pipeline that has a descriptor and no
convenient place to put it, and it costs a plugin one branch.

### The environment

avroc passes its own environment through to a generator unchanged, except as
[Determinism](#determinism) requires of `SOURCE_DATE_EPOCH`.

A plugin **MUST NOT** require an environment variable to be set in order to do
its normal work; everything that configures its output arrives as `--opt`. The
manifest is the reviewable record of how a project's code was generated, and a
setting that lives in the environment is absent from it — the two developers
comparing generated output cannot see the difference between their machines.

### Standard streams

A plugin **MUST NOT** write to standard output during a generation invocation.
Generated files go under `--out`, diagnostics go to stderr, and stdout carries
exactly one thing in this contract: the `--plugin-info` declaration. Keeping it
empty otherwise is what makes that declaration parseable without a mode flag,
and makes a generation invocation safe to place in a pipeline.

Standard error is the diagnostic channel, specified in [Exit codes and
diagnostics](#exit-codes-and-diagnostics). Standard input is unused unless
`--descriptor -` was passed.

## The output directory

The directory at `--out` is a private scratch directory that avroc creates for
this invocation and this generator alone (#117). A plugin **MAY** assume it
exists, is a directory, is writable, and is **empty**.

A plugin **MUST** write every file it produces beneath that directory. It
**MUST NOT** write outside it — not through `..`, not through an absolute path,
and not through a symlink it created for the purpose. avroc enforces this rather
than trusting it (#117), so an escape is a failed run, but the requirement is
stated here because a plugin that needs to escape has misunderstood the
contract rather than hit a limitation. A plugin **MAY** create subdirectories
beneath `--out` to whatever depth it needs.

Everything the plugin leaves in that directory on a zero exit is the output of
the run, and avroc merges it into the project's output tree (#117). Nothing else
is: a file written elsewhere is not output, and a file the plugin deletes before
exiting was never output.

### What a plugin does not own

Three responsibilities that look like a generator's are avroc's, because avroc
owns the project's output tree and a generator can see only its own scratch
directory:

- **Merging.** Files reach the project tree only after a zero exit, in one
  step (#117). A plugin **MUST NOT** attempt to write into the project tree
  itself, and does not learn where it is.
- **Collision detection.** Two generators producing the same output path is an
  error avroc raises at merge time, naming both (#118). A plugin **MUST NOT**
  try to coordinate with another generator, and cannot: their directories are
  disjoint and their order is unspecified.
- **Stale-file pruning.** Removing a file that a previous run produced and this
  one did not is avroc's (#119). A plugin **MUST NOT** delete or rename anything
  it did not itself create in this invocation.

The empty scratch directory is what makes the first two mechanical: the set of
files a run produced is exactly the set found in the directory afterwards, with
no marker inside a file and no bookkeeping asked of the plugin. What that
leaves — how avroc records a previous run's file set, so that a file from an
earlier run can be told from a file a person wrote by hand in the same
directory — is decided by #119 and specified here when it lands.

Collision detection is what fixes when a merge happens, so it is stated as a
requirement on avroc rather than left to an implementation: avroc **MUST** have
resolved every generator's output before it writes any of it into the project
tree, and a collision **MUST** fail the run with nothing merged (#118). A check
made as each generator finished would let the first one's files land before the
second was known about, and the report would then name whichever generator lost
a race — so the same unchanged inputs would fail differently on different runs,
which is the one thing generated output cannot do.

The consequence for a plugin is only the one [Scheduling](#also-out-of-scope)
already states: its files appear when the whole run's do, not when it exits, and
it **MUST NOT** depend on being run or merged before, after or alongside another.

### A plugin does not read its own past output

A plugin **MUST NOT** read the project's existing output, and **MUST NOT**
expect files it wrote on a previous run to be present: the directory it is
handed is empty every time. Generation is a function of the descriptor and the
options, and a generator that consulted its previous output would make it a
function of the repository's history too — so a regeneration from a clean
checkout would differ from a regeneration in place, and only one of them would
be reviewed. [Determinism](#determinism) is the same requirement stated
positively.

## Exit codes and diagnostics

A **zero** exit status means every file the plugin intended to produce is
written and closed beneath `--out`, and the invocation succeeded. avroc merges
the directory (#117), once every other generator in the run has succeeded too
(#118).

A **non-zero** exit status means the invocation failed. avroc **MUST** fail the
run and **MUST** discard the scratch directory rather than merging it (#115).
Because it is discarded, a plugin **MAY** exit non-zero with partial output on
disk, and **SHOULD NOT** spend effort cleaning up after itself before failing.

One generator's failure fails the whole run, and since nothing is merged until
every generator has produced its output (#118), a failed run leaves the project
tree as it found it. So a plugin **MUST NOT** read its own zero exit as a promise
that its files are in the tree: another generator failing, or colliding with it,
discards them too. That is the point rather than a side effect — a half-generated
tree is worse than an ungenerated one, because a person then has to work out
which half is which.

avroc **MUST NOT** attach meaning to a particular non-zero value beyond
failure, and a plugin **MUST NOT** expect it to. The small integers are already
spoken for by parties this contract does not control: a shell exits 126 for a
file it cannot execute and 127 for one it cannot find, and a process killed by a
signal is reported as 128 plus the signal number by most shells. A scheme
assigning meanings on top of those would be a scheme that misreads the cases it
most needs to get right.

A generator terminated by a **signal** **MUST** be reported by avroc as
terminated by that signal, naming it, and distinguishably from one that exited
non-zero (#115). The two need different responses — the second is a bug in the
generator, the first is usually the run being cancelled or the machine running
out of memory — and a report that flattens them sends the user looking in the
wrong place.

### The diagnostic format

A diagnostic is a single line on standard error, encoded in UTF-8:

```
<severity>: <message>
```

`<severity>` **MUST** be one of `error`, `warning` or `note` — a closed set of
three, matched case-sensitively. `<message>` **MUST NOT** contain a newline; a
diagnostic that needs more than one line **MUST** be written as one `error:` or
`warning:` line followed by `note:` lines. A message **SHOULD** open with the
fully-qualified name of the schema or field it is about, because that is the
only location a plugin has: it never sees the `.avdl` a user wrote.

The severity **MUST** open the line, the separator **MUST** be a colon and a
single space, and the message **MUST NOT** be empty. Each of those is what tells
a diagnostic from the ordinary output of a program that also writes to standard
error: a line indented under a stack trace, an `error:something` with no space,
and a bare `error: ` say nothing a level could be attached to, and avroc treats
all three as text rather than guessing (#115).

```
error: com.example.User.created_at: logical type "duration" is not supported
note: com.example.User.created_at: declared as fixed(12)
```

avroc **MUST** parse lines of that form into its structured log at the
corresponding level, attributed to the generator (#115). Any line that does not
match **MUST** be surfaced verbatim and attributed the same way. It is never
discarded and never held back until the process exits: an unrecognised line is
usually a panic, a stack trace or a library writing to stderr on its own
account, and those are exactly what a user needs to see when a generator fails
in a way its author did not anticipate.

The levels are `error:` to error, `warning:` to warning and `note:` to info. A
line that is not a diagnostic is recorded at **warning**, one level above the
`note:` a plugin writes deliberately — info is where a log is ordinarily
threshold-ed, so a handler configured a notch above it would drop exactly the
panic this rule exists to surface, and a line avroc could not classify is not one
to file under the mildest severity it has.

A plugin that writes an `error:` diagnostic **MUST** exit non-zero, and a plugin
that exits non-zero **SHOULD** write at least one `error:` diagnostic saying
why. avroc **MUST** fail the run on a non-zero exit even when nothing was
written to stderr — a silent failure is still a failure — and **MUST NOT** fail
a run whose generator exited zero after printing `error:`, because the exit
status is the verdict and the diagnostics are the explanation.

## Determinism

Two invocations of the same plugin executable, given a byte-identical descriptor
and an identical option list, **MUST** produce the same set of relative paths
beneath `--out`, with byte-identical contents (#120).

That holds regardless of the wall-clock time, the hostname, the user, the
working directory, the locale, the environment beyond `SOURCE_DATE_EPOCH`, the
absolute paths passed in `--descriptor` and `--out`, the order in which the
filesystem returns entries, and any concurrency inside the plugin. In
particular, a plugin **MUST NOT** embed a timestamp, a hostname, a username, an
absolute path or a random value in its output, and **MUST** emit anything
derived from an unordered collection in a fixed order — the descriptor's own
order where there is one, or a byte-wise sort of a stable key where there is
not. Map iteration order is the usual way this requirement is broken, and it
breaks intermittently, which is worse than breaking every time.

A plugin **MAY** embed its own name and version, which are properties of the
plugin rather than of the invocation and do not vary between two runs of the
same executable. Output that changes when the generator is upgraded is expected;
output that changes when nothing changed is the failure.

Where a timestamp is genuinely unavoidable, a plugin **MUST** use the value of
`SOURCE_DATE_EPOCH` when it is set, interpreted as a count of seconds since the
Unix epoch in UTC, and **MUST NOT** read the clock. When it is not set, a plugin
**SHOULD** omit the timestamp rather than fall back to the clock: a missing
generation date costs a reader nothing, and a present one costs every
regeneration a diff.

The requirement is checked rather than asserted — a pipeline stage generates
twice and byte-compares, failing on any difference (#120) — because determinism
is the kind of property that holds until nobody is looking.

## Capability negotiation

A plugin **MUST** support being invoked as:

```
avroc-gen-<name> --plugin-info
```

It **MUST** then write a capability declaration to standard output, exit zero,
and do nothing else: no descriptor is read, no file is written, and no other
argument is required or accepted. The declaration **MUST** be identical across
invocations of the same executable, for the reasons
[Determinism](#determinism) gives.

avroc **MUST** invoke `--plugin-info` on every generator it resolved, before any
generation begins (#116).

### The declaration

The declaration is a single JSON object on one or more lines, encoded in UTF-8:

```json
{
  "name": "go",
  "version": "0.2.0",
  "ir_version": 1,
  "options": ["package", "module"]
}
```

- `name` (string, required) — the generator's `<name>`. It **MUST** match the
  suffix of the executable avroc resolved, and avroc **MUST** fail the run when
  it does not: a mismatch means the file on `PATH` is not the generator the
  manifest asked for, and generating with it would produce output attributed to
  the wrong plugin.
- `version` (string, required) — the plugin's own version, in whatever scheme
  its author uses. avroc **MUST NOT** interpret it, compare it, or make any
  decision from it; it exists to be reported in a log and quoted in a bug
  report.
- `ir_version` (integer, required) — the highest IR version the plugin
  understands, in the sense of [`ir/SPEC.md`](../ir/SPEC.md)'s version field.
  avroc **MUST** compare it against the version of the descriptor it is about to
  write, and **MUST** fail the run before generating anything when the
  descriptor's version is higher, with a diagnostic naming both numbers and the
  generator. This does not relieve the plugin of the check the IR spec requires
  of it at generation time; a plugin is entitled to be run by something other
  than this version of avroc.
- `options` (array of strings, optional) — the `--opt` keys the plugin accepts.
  When present, avroc **MAY** reject a manifest option the plugin did not
  declare, before invoking it. When absent, avroc passes the options through and
  the plugin decides, which is the behaviour
  [Options](#options) already requires of it.

avroc takes that **MAY**: it rejects a manifest option that a plugin declaring a
vocabulary left out of it (#116). So *present and empty* and *absent* are
different declarations rather than two spellings of one — the first says the
plugin accepts no option at all, the second hands the decision back to the
plugin — and a plugin that wants avroc out of the decision omits the member
rather than writing `[]`. A plugin whose vocabulary is genuinely empty needs to
be able to say so, which is the whole reason the two are not collapsed.

Because they are opposite instructions, a third spelling is a bug rather than a
third meaning: `"options": null` is neither an array nor an absent member, and
avroc **MUST** reject it rather than choose one of them (#116). Reading it as
absent is the reading that does damage — every option in the manifest then goes
through unchecked, past a check the plugin appeared to have asked for — and a
plugin holding an empty list in a language that renders one as `null` emits it by
accident, which is exactly the case a guess would get wrong silently.

avroc **MUST** ignore members it does not recognise, so that a plugin declaring
more than this version of avroc reads is not thereby broken. That is the
opposite of what avroc does with an unknown field in a project's `avroc.json`,
and the two differ because their authors do: a manifest is a file a person wrote,
where an unknown field is a typo, while a declaration is a message a program
wrote, where an unknown member is a newer plugin.

### Why JSON, and why it is not optional

The declaration is JSON while the descriptor is protobuf, and the split is
deliberate. This is the one message a plugin has to produce *before* it has
demonstrated any tooling at all, and a shell script can emit the object above
with a single `printf`; requiring protobuf here would mean a plugin needed a
protobuf runtime in order to say that it does not have one. The descriptor
travels the other way — large, deeply structured, written by avroc and read by
generated types — so the trade points the opposite direction there.

A plugin that does not implement `--plugin-info` is not a conforming plugin.
avroc **MUST** treat a non-zero exit, an unparseable declaration or a missing
required member as a failure of the run, before generation, naming the generator
and what it received (#116).

Making the handshake optional was considered and rejected. Its entire purpose is
to turn a late, confusing failure — a generator that ran, half-worked and
produced a bad error about a type — into an early one that names the version
mismatch. A plugin allowed to skip it gets exactly the failure the handshake
exists to remove, and the user cannot tell which kind of plugin they have. The
cost of the rule is one `printf` in the smallest plugin that can exist, which is
the standard this contract holds itself to everywhere else.

## Out of Scope

### What is in the descriptor

The structure and meaning of the IR a plugin reads are **not specified here**:
what a resolved schema contains, what each type constructor carries, what
*resolved* means, and how the version field is compared.

Reason: they are [`ir/SPEC.md`](../ir/SPEC.md)'s (#108, #109), and the split is
what lets the two evolve apart. The IR gains a field without this document
changing; this document changes a flag without the IR moving. Restating any of
the IR's shape here would produce a second description for a plugin author to
find, and two descriptions of one value disagree eventually.

### Where plugins live in the published image

The directory a derived image copies a generator into, the entrypoint, the UID
the process runs as, and the path the published `FileDescriptorSet` ships at are
**not specified here**.

Reason: they are the [container contract](../container/SPEC.md)'s (#126, #127).
This document says a generator is found by name on `PATH`, which is true
wherever avroc runs, including a laptop with no container involved. Which
directories are *on* that `PATH` inside the published image is a property of the
image, and binding the two together would make a contract about executables
depend on a contract about layers.

### The `avroc.json` project manifest

Which generators a project runs, over which inputs, with which options, is **not
part of this contract**, and neither is the file that says so.

Reason: different audience. The manifest is what a *user* writes to drive the
CLI; this document is what a *plugin author* implements. A plugin never reads
the manifest — it receives the options the manifest selected, already resolved,
on its command line — so specifying the file here would put a build
configuration format in front of the one reader with no use for it.

### Plugin distribution, and reproducibility

There is **no transport, no plugin registry, no lockfile and no resolution
protocol**. avroc does not fetch a plugin, does not pin one, does not verify one
and does not know where one came from. A plugin arrives on `PATH` by whatever
means put it there — a package manager, a `go install`, a `COPY` in a
Dockerfile, a file a colleague sent — and avroc runs the first one it finds.

Reason: the mechanisms that would make this avroc's problem already exist and
are better than anything this project would build. `avroc get` and `avroc.lock`
tried the other way and are removed (#125); a container image is a lockfile that
the whole industry already knows how to sign, diff, cache and pull, and `FROM`
plus `COPY` is a resolution protocol with an ecosystem behind it (#126, #127).

The consequence has to be stated plainly rather than discovered:

> **avroc makes no reproducibility guarantee about the host-execution path.**
> The same manifest and the same schemas, on two hosts, can produce different
> generated code if the two hosts have different builds of a generator on their
> `PATH` — and avroc cannot detect it, because a plugin is a filename and an
> execute bit. [Determinism](#determinism) binds one plugin executable to
> itself; nothing here binds two machines to the same executable.
>
> **The container is the reproducible path.** An image pinned by digest fixes
> every generator in it, and that is the configuration this project supports
> when reproducibility is a requirement (#126, #127, #128). `go install` and a
> `PATH` are a convenience for a developer's laptop.

That is a real trade and not a gap waiting to be filled. What it buys is that a
generator needs no packaging story at all to be usable — a shell script in
`~/bin` is a working plugin ten seconds after it is written — and that avroc
never becomes a package manager for a language it does not know.

### What a generator emits

The files a generator writes, their language, their layout, their naming and
whatever API they expose are the generator's own. `avroc-gen-go` (#121),
`avroc-gen-json` (#122) and `avroc-gen-pcf` (#123) are three plugins among
many.

Reason: a contract that described the output would not be a contract a
third-party generator could meet. This document constrains how a plugin is
reached and what it may assume, and stops there — the whole point of the plugin
model is that avroc has no opinion about the code that comes out.

### Also out of scope

- **Sandboxing.** A generator runs as a subprocess of avroc, with the invoking
  user's privileges and their whole environment. avroc does not confine it, and
  running an untrusted generator is exactly as safe as running any other
  untrusted executable. The container is the confinement story here too.
- **Scheduling.** How many generators avroc runs at once, in what order, and how
  cancellation reaches them is avroc's (#114). A plugin sees a single
  invocation and **MUST NOT** depend on being run before, after or alongside
  another.
- **Windows.** Covered above under [Host platform](#host-platform) rather than
  here, because it is a decision this document makes rather than a boundary it
  defers to another (#104).
- **A plugin SDK.** There is no library a plugin has to link, in Go or anywhere
  else. A helper package may exist for convenience, but conformance is measured
  against this document and nothing else — a plugin that meets it in `sh` is as
  conforming as one that meets it in Go.

## Appendix: Mapping to Stories

| Section | Implemented by |
| --- | --- |
| _Document shape and stub_ | [#103](https://github.com/z5labs/avroc/issues/103) |
| _This document_ | [#106](https://github.com/z5labs/avroc/issues/106) |
| [Host platform](#host-platform) | [#104](https://github.com/z5labs/avroc/issues/104) |
| [Discovery](#discovery) | [#114](https://github.com/z5labs/avroc/issues/114) |
| [Invocation](#invocation) | [#114](https://github.com/z5labs/avroc/issues/114), [#115](https://github.com/z5labs/avroc/issues/115) |
| [The descriptor](#the-descriptor), [Location and lifetime](#location-and-lifetime) | [#111](https://github.com/z5labs/avroc/issues/111), [#114](https://github.com/z5labs/avroc/issues/114) |
| [The output directory](#the-output-directory) | [#117](https://github.com/z5labs/avroc/issues/117), [#118](https://github.com/z5labs/avroc/issues/118), [#119](https://github.com/z5labs/avroc/issues/119) |
| [Exit codes and diagnostics](#exit-codes-and-diagnostics) | [#115](https://github.com/z5labs/avroc/issues/115) |
| [Determinism](#determinism) | [#120](https://github.com/z5labs/avroc/issues/120) |
| [Capability negotiation](#capability-negotiation) | [#116](https://github.com/z5labs/avroc/issues/116) |
| No transport — the gRPC service and its socket removed | [#124](https://github.com/z5labs/avroc/issues/124) |
| [No plugin distribution](#plugin-distribution-and-reproducibility) — `avroc get` and `avroc.lock` removed | [#125](https://github.com/z5labs/avroc/issues/125) |
| A worked implementation of this contract | [#121](https://github.com/z5labs/avroc/issues/121), [#122](https://github.com/z5labs/avroc/issues/122), [#123](https://github.com/z5labs/avroc/issues/123) |
| Conventions this document follows | [#103](https://github.com/z5labs/avroc/issues/103) |
