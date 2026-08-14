// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// Package main implements avroc's root Dagger module: the one definition of
// this repository's pipeline, called by CI and by contributors alike.
//
// # What this module still owns, now that the archetype builds the image
//
// This module used to build avroc's published images itself, and about 1,900 of
// its lines existed for that reason alone. The argument was recorded here and in
// image.go at length: the Z5Labs standard pipeline's image half produced one
// scratch image per binary with /app/<binaryName> as entrypoint and nothing
// else — no PATH, no user, no plugin directory — and avroc's image is a
// documented public contract that has to promise all three (#126, #127, #128).
//
// That premise is gone (#217). github.com/z5labs/devex/daggerverse/z5labs is now
// a chainable Go -> App -> Publish API, and every one of the three things avroc
// needed is the archetype's own: /usr/local/bin is its fixed executable
// directory with PATH composed from the same constant, 65532:65532 is its pinned
// non-root user, and App.WithApp composes one application's executable into
// another's plugin directory, which is exactly what a generator image is. So the
// image, the multi-platform publish, the tag family, the recursive signature and
// the attestations are all the standard's, and this module states the version and
// the repositories and gets out of the way.
//
// Three things did not move, and each is a fact about *this* project rather than
// about how an image gets built:
//
//   - The contract checks. ImageContract, GeneratorImageContract, Regeneration,
//     WorkedExample, CompanionModule, TlsEgress, TracePropagation and
//     IrDescriptorSet are assertions about docs/container/SPEC.md and
//     docs/plugin/SPEC.md, and every one of them holds against a container
//     whoever built it. They are also the evidence that adopting the archetype
//     changed nothing a consumer can see, which is the only thing that makes a
//     change of this size reviewable.
//   - Whether this commit is a release. The archetype takes a version from its
//     caller and derives the tag family from it; reading the refs at HEAD to
//     decide whether there is a release here at all, refusing two version tags
//     and refusing `+build` metadata stay in release.go, because they are facts
//     about this repository's release process. TagScheme is what checks them.
//   - The set of generators, and what each one is. builtinGenerators is this
//     repository's list; the archetype has no opinion about it.
//
// # Why the stage functions exist alongside Ci
//
// GoChain.Ci exposes only the whole pipeline, and waiting on four stages to
// learn that one file is unformatted is not the loop to develop in. So Fmt, Vet,
// Lint and Test are here too. They are not a second implementation: each one
// drives the same github.com/z5labs/devex/daggerverse/go builder that the
// standard pipeline drives, with one stage enabled instead of four, against the
// same lint configuration. All four devex dependencies are pinned to a single
// commit for that reason — a bump has to move them together, or a stage run on
// its own stops being the stage Ci runs.
//
// Ci is what CI calls and what a contributor should run before pushing. The
// stage functions are for narrowing down what Ci reported.
//
// # Why .git is bound separately from the source
//
// The archetype stamps every binary with the short HEAD SHA and annotates every
// image with the commit, its committer time and the origin remote, so building
// an App needs real git metadata — which the ignore list on New deliberately
// keeps out of Source, because none of fmt, vet, lint or test reads it and
// leaving it in would make every commit a cache miss for all four.
//
// Both are had by binding it as its own argument, the way Release already took
// one, and folding it back in on the single path that builds an App
// (appSource). The check stages keep the tree they always had; the image path
// gets the metadata it needs; and nothing that was cache-stable per commit
// became a cache miss per commit. The workflow half of that is
// .github/workflows/build.yaml, which fetches full history because an image is
// built on every pull request.
package main

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"dagger/avroc/internal/dagger"
)

// Avroc is the root module type. Source and the lint configuration are bound
// once at construction so every function below checks the same tree the same
// way; there is no per-function source argument that could point somewhere else.
type Avroc struct {
	// +private
	Source *dagger.Directory
	// +private
	LintConfig *dagger.File
	// GitDir is the repository's .git, bound apart from Source so that the
	// check stages never see it. Only appSource folds it back in.
	// +private
	GitDir *dagger.Directory
}

// New binds the repository to the pipeline.
//
// source defaults to the repository root, so `dagger call ci` from a checkout
// needs no arguments. The ignore list drops what no check stage reads: .git is
// excluded because none of fmt, vet, lint or test looks at git metadata, and
// leaving it in would make every commit a cache miss for all four.
//
// It stays excluded now that the archetype builds the image (#217), rather than
// coming off the list as the comment here used to predict. The archetype does
// need real git metadata — it stamps the short HEAD SHA into every binary and
// annotates every image with the commit, its committer time and the origin — but
// it needs it on one path out of nine, and folding .git into Source would have
// bought that by making fmt, vet, lint and test miss their cache on every
// commit. gitDir is that metadata bound as its own input, and appSource is the
// only thing that puts the two back together.
//
// bin and the per-issue worktrees under .claude go too: both are local build
// output, neither is committed, and both would otherwise vary between a
// contributor's checkout and CI's for no effect on any stage.
//
// gitDir defaults to the repository's own .git. It is a *directory* rather than
// something derived, because git's own answers are what the archetype stamps and
// anything this module computed would be a build identity a caller could have
// supplied. Note that a git *worktree* has a .git file rather than a directory,
// so the stages that build an image are the ones that do not run from one; that
// was already true of Release and is now true of the image checks too.
//
// lintConfig defaults to the repository's own .golangci.yml. It is passed
// explicitly rather than left to the standard pipeline's bundled default so that
// the configuration committed to this repository is the configuration CI lints
// against — a .golangci.yml that CI ignored would be a file contributors read
// and trusted while the pipeline enforced something else. It is written in the
// golangci-lint v2 dialect, which is what the standard pipeline runs; see that
// file's own comment.
func New(
	// +optional
	// +defaultPath="/"
	// +ignore=[".git", ".claude", "/bin", "/dist"]
	source *dagger.Directory,
	// +optional
	lintConfig *dagger.File,
	// +optional
	// +defaultPath="/.git"
	gitDir *dagger.Directory,
) *Avroc {
	if lintConfig == nil {
		lintConfig = source.File(".golangci.yml")
	}
	return &Avroc{Source: source, LintConfig: lintConfig, GitDir: gitDir}
}

// Ci runs the whole pipeline: fmt, vet, golangci-lint and `go test -race`, as
// the Z5Labs standard defines them. The standard runs the enabled stages in
// parallel and aggregates their errors, so one run reports every stage that
// failed rather than stopping at the first.
//
// This is the single entrypoint — CI is one `dagger call ci` and stays one,
// because a workflow step that reran any of these stages would be a second
// definition of them.
//
// +check
// +cache="session"
func (m *Avroc) Ci(ctx context.Context) error {
	return m.goChain().Ci(ctx)
}

// goChain is the standard Go chain bound to this repository's source and
// configured the one way this repository is checked.
//
// It is one helper rather than a call at each site because Ci and the App path
// have to agree about the source they are given and about the lint
// configuration: a chain configured twice is two definitions of what "checked"
// means here, which is the thing wrapping the standard exists to prevent.
//
// WithTest(true) is written out rather than left to the default. The archetype's
// race detector is on unless a caller says otherwise, so the argument is
// redundant today and is stated anyway — avroc runs generators concurrently, and
// "the race detector is on because nobody turned it off" is not the same claim as
// "the race detector is on because this repository asked for it".
//
// The chain carries no .git: Ci does not build, and the stages it does run are
// the ones the ignore list on New exists to keep cache-stable. appSource is
// where the metadata arrives.
func (m *Avroc) goChain() *dagger.Z5LabsGoChain {
	return dag.Z5Labs().
		Go(m.Source).
		WithLint(dagger.Z5LabsGoChainWithLintOpts{Config: m.LintConfig}).
		WithTest(true)
}

// appSource is the source tree an App is built from: this repository's, with
// its git metadata folded back in.
//
// This is the only place the two are put together, and that is the whole of
// #217's answer to a problem the old comment on New predicted: the archetype
// stamps binaries and annotates images from the refs at HEAD, so it needs real
// git metadata, and the check stages need a tree that does not change when a
// commit is made. One argument, folded in on one path, gives both.
func (m *Avroc) appSource() *dagger.Directory {
	return m.Source.WithDirectory(".git", m.GitDir)
}

// Fmt reports any file that gofmt would rewrite, as a diff.
//
// +check
// +cache="session"
func (m *Avroc) Fmt(ctx context.Context) error {
	return m.stage().WithFmt().Check(ctx)
}

// Vet runs `go vet ./...`.
//
// +check
// +cache="session"
func (m *Avroc) Vet(ctx context.Context) error {
	return m.stage().WithVet().Check(ctx)
}

// Lint runs golangci-lint over ./... against this repository's .golangci.yml.
//
// +check
// +cache="session"
func (m *Avroc) Lint(ctx context.Context) error {
	return m.stage().
		WithLint(dagger.GoCiWithLintOpts{Config: m.LintConfig}).
		Check(ctx)
}

// Test runs `go test -race ./...`. The race detector is on here for the same
// reason it is on in Ci: a race that only the pipeline looks for is one found
// after the change is pushed rather than before. avroc runs generators
// concurrently, so this is not a formality.
//
// +check
// +cache="session"
func (m *Avroc) Test(ctx context.Context) error {
	return m.stage().
		WithTest(dagger.GoCiWithTestOpts{Race: true}).
		Check(ctx)
}

// IrDescriptorSet builds the IR's protobuf FileDescriptorSet — the published
// ir.binpb — by compiling this repository and asking avrocpb for it. It is the
// pipeline's one artifact, and it is what the release workflow attaches to a
// release and what the base image copies to
// /usr/local/share/avroc/ir.binpb (docs/container/SPEC.md, #126).
//
// It is a function on this module rather than a step in a workflow for the
// reason .github/workflows/build.yaml gives: repo-specific work lands here and
// is invoked with `dagger call`, so a contributor produces the same bytes CI
// does with the same command, and there is no second recipe living in YAML that
// nobody can run locally.
//
// dag.Go().Container is the escape hatch the standard Go module offers for
// exactly this — a command its typed helpers do not cover. Using it rather than
// a container of this module's own keeps the toolchain version read out of
// go.mod, where this repository's toolchain version already lives, instead of
// pinning a golang: tag here that could drift from it.
//
// The bytes are a function of the source: avrocpb.MarshalFileDescriptorSet
// encodes deterministically and the file order is fixed by the protos'
// imports, so two runs over an unchanged tree produce an identical artifact.
// That is what makes the published file comparable across releases at all.
func (m *Avroc) IrDescriptorSet() *dagger.File {
	const out = "/out/ir.binpb"

	return dag.Go().
		Container(m.Source).
		WithExec([]string{"go", "run", "./internal/tools/ir-descriptor-set", "-o", out}).
		File(out)
}

// Regeneration is docs/plugin/SPEC.md's determinism requirement, checked
// rather than asserted (#120): it builds the four binaries, runs
// `avroc generate` over the worked example in example/ twice, and requires the
// two trees to be byte-identical — and identical to what is committed.
//
// Two comparisons, one function, because they need the same binaries and the
// same worked example and two functions would drift:
//
//   - The two runs against each other. This is the determinism check. Generated
//     code is a thing a project commits, so output that changes when nothing
//     changed turns every regeneration into a diff and makes the output useless
//     as a thing to commit. The property holds until nobody is looking, which is
//     why it is a stage and not a paragraph.
//   - The first run against the committed tree. This is the round-trip
//     CONTRIBUTING.md used to run only locally. It fails when example/ was not
//     regenerated after a change to a generator, which is the other way the
//     committed output stops meaning anything.
//
// The runs deliberately disagree about everything docs/plugin/SPEC.md says a
// generator's output must not vary with: the absolute paths in --descriptor and
// --out, the working directory, the temporary directory, PATH beyond the entry
// that resolves the generators, the user, the hostname, the locale and the time
// zone — and, since #197, whether the run is traced at all. They agree about
// SOURCE_DATE_EPOCH, because that one is an input rather than an accident of the
// machine — a run that varied it would be asking two different questions and
// calling the difference a bug.
//
// Tracing is on the list for the same reason as the rest of it and is worth
// naming separately, because it is the one entry that puts code *inside* the
// generation loop: #196 makes an invocation a span and #197 makes its phases
// spans, so the second run opens one between every two writes. Tracing is an
// observation of a generation and never an input to it, and this is where that
// sentence is checked over whole processes rather than in one.
//
// What this cannot catch is a generator reading the clock: two runs a moment
// apart agree on the date. internal/plugin.TestNoGeneratorReadsTheClock is the
// static half that closes it, and internal/plugin.SourceDateEpoch is the one
// sanctioned way to get a timestamp at all.
//
// platform restricts the check to one of the platforms below; empty runs every
// one of them.
//
// +check
// +cache="session"
func (m *Avroc) Regeneration(
	ctx context.Context,
	// Run the check on this platform alone, as `GOOS/GOARCH` — one of the
	// platforms the check otherwise covers. Empty covers all of them.
	// +optional
	platform string,
) error {
	platforms := regenerationPlatforms()
	if platform != "" {
		if !slices.Contains(platforms, dagger.Platform(platform)) {
			return fmt.Errorf("platform %q is not one this repository targets: %v", platform, platforms)
		}
		platforms = []dagger.Platform{dagger.Platform(platform)}
	}

	// Every platform is checked and every failure reported, rather than
	// stopping at the first: "it is deterministic on amd64 and not on arm64" is
	// the finding, and a run that stopped early would hide half of it.
	var errs []error
	for _, p := range platforms {
		if err := m.regenerationOn(ctx, p); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
		}
	}
	return errors.Join(errs...)
}

// regenerationPlatforms is every platform Regeneration covers, and it is
// docs/container/SPEC.md's published set rather than a list invented here — it
// is imagePlatforms, read from the one place that set is written down, so a
// platform added to the image is checked for determinism in the same change.
//
// docs/plugin/SPEC.md's "Host platform" targets POSIX hosts and distinguishes
// nothing between Linux, macOS and the other Unixes. Neither distribution path
// puts any of them in the position of running a generator: the image runs Linux
// containers, and so does the companion Dagger module, whatever the host they
// are driven from. So the platforms a generator executable actually runs on are
// the image's, and those are the two below.
//
// The two share a data model — both are 64-bit and little-endian — so this is
// not a check for an endianness bug. It is a check that the executable actually
// published for each platform is the one that behaves, which is a different
// claim and the only one worth making about a binary somebody else will run. A
// contributor on macOS covers the remaining ground with `go test ./...`: the
// per-generator determinism tests run natively on the host they are actually
// on, which is the only way that host is ever covered at all.
func regenerationPlatforms() []dagger.Platform {
	return imagePlatforms()
}

// regenerationOn runs both generations for one platform and compares the trees.
//
// The comparison is `diff --recursive` in a container of the pipeline's own
// platform rather than a digest of the two directories, for two reasons: a
// digest reports only that something differs, where diff names the file and
// prints the lines; and a directory digest covers metadata that two runs are
// entitled to disagree about, which would fail the check for reasons that are
// not the property under test.
func (m *Avroc) regenerationOn(ctx context.Context, platform dagger.Platform) error {
	// One build per platform, cross-compiled by the toolchain container rather
	// than compiled under emulation, so only the generation itself runs on the
	// target platform. CGO is off because the run container below is scratch:
	// there is no libc in it, and nothing in avroc needs one.
	binaries := dag.Go().Build(m.Source, dagger.GoBuildOpts{
		Pkg:        "./cmd/...",
		Platform:   string(platform),
		DisableCgo: true,
	})

	committed := m.Source.Directory("example")
	first := generateWorkedExample(binaries, committed, platform, firstRun())
	second := generateWorkedExample(binaries, committed, platform, secondRun())

	const (
		committedAt = "/regeneration/committed"
		firstAt     = "/regeneration/first"
		secondAt    = "/regeneration/second"
	)

	// --text because every file either generator writes is text, and a diff that
	// said only "binary files differ" would report the failure without reporting
	// what it was.
	diff := func(a, b string) []string {
		return []string{"diff", "--recursive", "--unified", "--text", a, b}
	}

	_, err := dag.Go().
		Container(m.Source).
		WithMountedDirectory(committedAt, committed).
		WithMountedDirectory(firstAt, first).
		WithMountedDirectory(secondAt, second).
		WithExec(diff(firstAt, secondAt)).
		WithExec(diff(committedAt, firstAt)).
		Sync(ctx)
	return err
}

// generateWorkedExample runs `avroc generate` once over a pristine copy of the
// worked example and returns the tree it left behind.
//
// The container is scratch, holding the four statically linked binaries and the
// example: nothing else is in it, so nothing else can be what the output depended
// on. **There is no temporary directory in it**, which is #218 executed — avroc
// writes each invocation's descriptor into the generator's own output tree, so a
// generation that needed one would fail here rather than in an adopter's scratch
// container. It is also the shape docs/container/SPEC.md publishes — the CLI and
// the generators on PATH, no shell — so a generation that needs anything more
// than that is a finding about the image as much as about determinism.
func generateWorkedExample(
	binaries *dagger.Directory,
	example *dagger.Directory,
	platform dagger.Platform,
	run regenerationRun,
) *dagger.Directory {
	c := dag.Container(dagger.ContainerOpts{Platform: platform}).
		WithDirectory(pluginDir, binaries).
		WithDirectory(run.root, example).
		WithWorkdir(run.root)

	// Applied in order from a slice rather than a map: the environment is part
	// of the container's identity, and a map would vary it between two calls of
	// this function for no reason but Go's iteration order — which is the very
	// thing being checked for downstream.
	for _, v := range run.env {
		c = c.WithEnvVariable(v.name, v.value)
	}

	// The executable by absolute path, because PATH is a variable of this run
	// rather than a thing to rely on; PATH is what avroc searches to find the
	// generators, and that is the one job it has here.
	return c.
		WithExec([]string{pluginDir + "/avroc", "generate"}).
		Directory(run.root)
}

// pluginDir is docs/container/SPEC.md's plugin directory — the one directory on
// the published image's PATH that an extension's executables land in, and the one
// the archetype composes a generator into (#217).
//
// The regeneration container below puts all four binaries here, which is not quite
// the published layout any more: since #217 the archetype puts the *application's
// own* binary in appDir and names it absolutely in the entrypoint, so the image has
// avroc in one place and its generators in another. What that container is
// reproducing is the half that matters to a generation — the generators are found
// on PATH, by the name a manifest asked for, in a scratch container with no shell —
// and it names avroc by absolute path for the reason the comment above gives, which
// is that PATH is one of the things this check varies. image.go reads the same
// constant for the listing, so the day the plugin directory moves, both move.
const pluginDir = "/usr/local/bin"

// regenerationRun is one generation's arrangement of everything the output must
// not depend on.
type regenerationRun struct {
	// root is the directory the worked example is copied to, and so the prefix of
	// both absolute paths avroc passes a generator: --out, and — since #218 —
	// --descriptor, whose per-invocation directory avroc creates inside the
	// generator's own output tree.
	//
	// It is the axis that carries the descriptor's path, and it carries it alone.
	// TMPDIR used to, and the two runs still disagree about that variable for the
	// opposite reason: neither container has the directory it names, so an avroc
	// that went back to reading it would fail the stage outright rather than pass
	// it with the axis watching nothing.
	root string
	env  []envVar
}

type envVar struct {
	name  string
	value string
}

// sourceDateEpoch is the one part of the environment both runs agree on: an
// input to generation rather than a property of the machine, so varying it
// would make a generator that correctly honoured it fail this check. The
// instant itself is arbitrary and fixed — 2024-06-03T00:00:00Z.
const sourceDateEpoch = "1717372800"

// tracedRunEnv is what makes the second run a traced one: an endpoint, so that
// avroc builds a real SDK and propagates a span context to every generator it
// forks, and a TRACEPARENT, so that the run is a child of somebody else's trace
// exactly as it is under `dagger call`.
//
// **Where the spans end up is no part of this check.** What is being checked is
// that a generation which opens spans writes the same bytes as one that does
// not, and the spans have to be opened for that to mean anything; a collector
// container would add a service to the check and a reason for it to fail that
// has nothing to do with determinism. So this endpoint is a placeholder: an
// address on the loopback with nothing behind it, written as a literal rather
// than a name so that a scratch container with no resolver is not resolving
// anything, and harmless either way because internal/telemetry logs a failed
// export and never fails a build over one.
//
// It is a placeholder in a stronger sense than it looks, and the reason belongs
// here rather than being rediscovered: **Dagger sets OTEL_EXPORTER_OTLP_ENDPOINT
// itself on every exec**, overriding whatever the container carried, so that a
// tool inside reports into Dagger's own trace. What avroc reads in this run is
// therefore Dagger's endpoint and not this one — the spans are exported, and
// they are exported successfully. Nothing above depends on which, but a check
// that needed avroc pointed at an endpoint of its own could not be written this
// way at all; tls_egress.go is the one that needed it, and says what it does
// instead.
func tracedRunEnv() []envVar {
	return []envVar{
		{"OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318"},
		{"TRACEPARENT", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
	}
}

// firstRun and secondRun disagree about everything else. Where a value has a
// plausible-looking default, the second run deliberately does not use it: a
// generator that read HOME and got the same answer both times would pass a
// check that was not looking.
//
// TMPDIR is set on both and neither container holds the directory it names. Since
// #218 nothing in avroc reads it, so what it varies is no longer where the
// descriptor goes — see regenerationRun.root — and what it is still here for is
// the claim that avroc no longer reads it: a MkdirTemp("", …) put back anywhere
// would fail both runs with `no such file or directory`.
func firstRun() regenerationRun {
	return regenerationRun{
		root: "/work",
		env: []envVar{
			{"PATH", pluginDir},
			{"TMPDIR", "/tmp"},
			{"HOME", "/root"},
			{"USER", "root"},
			{"LOGNAME", "root"},
			{"HOSTNAME", "builder-one"},
			{"TZ", "UTC"},
			{"LANG", "C"},
			{"LC_ALL", "C"},
			{"SOURCE_DATE_EPOCH", sourceDateEpoch},
		},
	}
}

func secondRun() regenerationRun {
	return regenerationRun{
		// Longer than the first, and at a different depth, because a path that
		// leaked into the output would most plausibly leak as its own text.
		root: "/srv/a-considerably-longer-project-directory",
		env: append([]envVar{
			{"PATH", "/nonexistent:" + pluginDir + ":/also-nonexistent"},
			{"TMPDIR", "/var/tmp/second"},
			{"HOME", "/home/somebody-else"},
			{"USER", "somebody-else"},
			{"LOGNAME", "somebody-else"},
			{"HOSTNAME", "builder-two"},
			{"TZ", "Pacific/Kiritimati"},
			{"LANG", "en_US.UTF-8"},
			{"LC_ALL", "en_US.UTF-8"},
			{"SOURCE_DATE_EPOCH", sourceDateEpoch},
		}, tracedRunEnv()...),
	}
}

// stage returns the check builder the standard pipeline builds on, bound to
// this module's source and with no stage enabled yet. Callers enable the one
// they want. The Go toolchain version is left unset so the builder reads it from
// go.mod, which is where this repository's toolchain version lives and the only
// place it lives.
func (m *Avroc) stage() *dagger.GoCi {
	return dag.Go().Ci(m.Source)
}
