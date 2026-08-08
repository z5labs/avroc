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
// So this module takes the checks and the base-image work owns how images get
// built — whether that means extending GoApp upstream in devex, building the
// image in this module, or using a non-scratch base. Moving to GoApp is a change
// to which factory New calls, plus dropping .git from the ignore list below; it
// is not a change to what the pipeline is.
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

// stage returns the check builder the standard pipeline builds on, bound to
// this module's source and with no stage enabled yet. Callers enable the one
// they want. The Go toolchain version is left unset so the builder reads it from
// go.mod, which is where this repository's toolchain version lives and the only
// place it lives.
func (m *Avroc) stage() *dagger.GoCi {
	return dag.Go().Ci(m.Source)
}
