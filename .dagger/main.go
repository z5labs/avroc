// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// Package main implements avroc's root Dagger module: the one definition of
// this repository's pipeline, called by CI and by contributors alike.
//
// # Why this wraps the Z5Labs standard pipeline
//
// Ci does not implement fmt, vet, lint and `go test -race`. It hands the source
// to github.com/z5labs/devex/daggerverse/z5labs and lets that module's GoLib
// archetype run them, which is the same standard every other Z5Labs repository
// runs. A reimplementation here would be a second definition of what "checked"
// means in a Z5Labs repository, and two definitions drift: a stage added to the
// standard would silently not apply to avroc, and a difference in how a stage is
// invoked would show up as this repository disagreeing with every other one for
// reasons nobody wrote down. Wrapping costs one dependency and keeps that
// impossible.
//
// # Why GoLib, when avroc has four commands
//
// The library archetype is not a claim that avroc is a library. avroc has four
// `package main` binaries under cmd/, so the usual reason to call GoLib — there
// being no main package for the image half of the standard to act on — does not
// apply here. Two reasons that do:
//
// The check stages are identical either way. GoApp and GoLib route fmt, vet,
// lint and `go test -race` through the same shared check in the standard, so
// choosing GoLib costs this pipeline nothing in coverage.
//
// GoApp's image half is not the image avroc needs. It builds a scratch image
// per binary with /app/<binaryName> as entrypoint and nothing else: no PATH, no
// USER, no plugin directory. avroc's published image is a documented public
// contract that has to promise all three, because generator images are built
// FROM it and avroc discovers generators on PATH. GoApp's output cannot satisfy
// that as-is, and deciding how it should is a live design question that belongs
// to the base-image story rather than being pre-decided here by an archetype.
//
// So this module takes the checks from the standard and builds the image itself,
// in image.go, which records why that was chosen over extending GoApp upstream
// or accepting a non-scratch base (#126). Moving the *checks* to GoApp remains a
// change to which factory New calls, plus dropping .git from the ignore list
// below; it is not a change to what the pipeline is.
//
// # Why the stage functions exist alongside it
//
// GoLib exposes only the whole pipeline, and waiting on four stages to learn
// that one file is unformatted is not the loop to develop in. So Fmt, Vet, Lint
// and Test are here too. They are not a second implementation: each one drives
// the same github.com/z5labs/devex/daggerverse/go builder that the standard
// pipeline drives, with one stage enabled instead of four, against the same lint
// configuration. Both dependencies are pinned to a single devex commit for that
// reason — a bump has to move them together, or a stage run on its own stops
// being the stage Ci runs.
//
// Ci is what CI calls and what a contributor should run before pushing. The
// stage functions are for narrowing down what Ci reported.
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
}

// New binds the repository to the pipeline.
//
// source defaults to the repository root, so `dagger call ci` from a checkout
// needs no arguments. The ignore list drops what no check stage reads: .git is
// excluded because none of fmt, vet, lint or test looks at git metadata, and
// leaving it in would make every commit a cache miss for all four. That changes
// if the archetype ever becomes GoApp — it stamps binaries from the refs at HEAD
// and does need real git metadata, so .git has to come off this list in the same
// change that switches factories, and .github/workflows/build.yaml has to stop
// checking out shallow in that same change.
//
// bin and the per-issue worktrees under .claude go too: both are local build
// output, neither is committed, and both would otherwise vary between a
// contributor's checkout and CI's for no effect on any stage.
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
) *Avroc {
	if lintConfig == nil {
		lintConfig = source.File(".golangci.yml")
	}
	return &Avroc{Source: source, LintConfig: lintConfig}
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
	return dag.Z5Labs().
		GoLib(m.Source, dagger.Z5LabsGoLibOpts{LintConfig: m.LintConfig}).
		Ci(ctx)
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
// zone. They agree about SOURCE_DATE_EPOCH, because that one is an input rather
// than an accident of the machine — a run that varied it would be asking two
// different questions and calling the difference a bug.
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
// The container is scratch, holding the four statically linked binaries, an
// empty temporary directory and the example: nothing else is in it, so nothing
// else can be what the output depended on. It is also the shape
// docs/container/SPEC.md publishes — the CLI and the generators on PATH, no
// shell — so a generation that needs anything more than that is a finding about
// the image as much as about determinism.
func generateWorkedExample(
	binaries *dagger.Directory,
	example *dagger.Directory,
	platform dagger.Platform,
	run regenerationRun,
) *dagger.Directory {
	c := dag.Container(dagger.ContainerOpts{Platform: platform}).
		WithDirectory(pluginDir, binaries).
		WithDirectory(run.tmp, dag.Directory()).
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

// pluginDir is where the binaries go, and it is docs/container/SPEC.md's plugin
// directory rather than an arbitrary one: avroc discovers generators on PATH, so
// the run container's layout is the published image's layout. image.go reads the
// same constant, which is what makes that sentence true rather than aspirational.
const pluginDir = "/usr/local/bin"

// regenerationRun is one generation's arrangement of everything the output must
// not depend on.
type regenerationRun struct {
	// root is the directory the worked example is copied to, and so the prefix
	// of every absolute path avroc passes a generator in --out.
	root string
	// tmp is TMPDIR, and so where the descriptor avroc passes in --descriptor is
	// written.
	tmp string
	env []envVar
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

// firstRun and secondRun disagree about everything else. Where a value has a
// plausible-looking default, the second run deliberately does not use it: a
// generator that read HOME and got the same answer both times would pass a
// check that was not looking.
func firstRun() regenerationRun {
	return regenerationRun{
		root: "/work",
		tmp:  "/tmp",
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
		tmp:  "/var/tmp/second",
		env: []envVar{
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
		},
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
