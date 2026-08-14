// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// This file is the one place the pipeline touches the companion Dagger module —
// daggerverse/avroc, published from this repository for other people's pipelines
// (#130).
//
// # Why the module is a separate module
//
// The two have nothing in common but this file. The root module builds avroc
// from a checkout, publishes its images and checks them against
// docs/container/SPEC.md; the companion module knows nothing about a checkout
// and does one thing — pull the published images, compose them, run avroc — for
// somebody whose repository is not this one. Folding it into the root module
// would publish this repository's whole pipeline as an interface, and a caller
// installing it would get `ci`, `release` and `publish` in their `dagger call
// --help` alongside the one function they wanted.
//
// # Why the check drives it with images built here
//
// The module's defaults pull `ghcr.io/z5labs/avroc:v0`, which is a *released*
// image: a check that used the defaults would be checking the last release and
// would keep passing through a pull request that broke both the module and the
// image it drives. So this passes the images this pipeline just built, through
// the same --image argument a caller uses to try an unreleased avroc, and the
// module is exercised against the change rather than around it.
//
// That is also the only reason the module takes that argument at all, which is
// worth knowing before anybody is tempted to remove it as unused: it is used
// here, on every pull request.
package main

import (
	"context"
	"errors"
	"fmt"

	"dagger/avroc/internal/dagger"
)

// CompanionModule is the companion Dagger module's worked example, run rather
// than written down (#130): it composes avroc's three generators into the image
// this pipeline just built, generates example/ through the module, and requires
// the result to be the committed tree byte for byte.
//
// It is a check because the module is a convenience over docs/container/SPEC.md
// rather than a contract of its own, and a convenience that has quietly stopped
// working is worse than none: a caller reaches for it precisely because they did
// not want to learn the contract underneath, so the failure lands on somebody
// with no reason to know where to look. Nothing else here reads it — the image
// checks drive the images directly, and a module whose calls no longer compose
// would not fail one of them.
//
// Both ways of adding a generator are checked, and against the same expected
// tree:
//
//   - WithGenerator, taking each executable out of the generator image that
//     publishes it. This is `COPY --from` and it is the documented path, so it is
//     the one an adopter's pipeline will be on.
//   - WithGeneratorExecutable, taking the same three as files straight from the
//     build. This is the generator author's path, before anything of theirs is
//     published, and it is the one nobody would notice breaking.
//
// Requiring both to produce the committed example is what makes them
// interchangeable rather than merely both present.
//
// One platform — the engine's own — rather than every published one. What varies
// per platform is the executable, and both Regeneration and
// GeneratorImageContract already run the same generation on each of them; what
// this check adds is that the module's calls compose into a working image, which
// is not a property a second architecture can disagree about.
//
// +check
// +cache="session"
func (m *Avroc) CompanionModule(ctx context.Context) error {
	platform, err := dag.DefaultPlatform(ctx)
	if err != nil {
		return fmt.Errorf("resolving the engine's platform: %w", err)
	}

	committed := m.Source.Directory("example")
	binaries := m.binaries(platform)

	// Both start from the base image this pipeline built, which carries the CLI
	// and no generator — so a generation that succeeded below did so with the
	// generators these calls put there and with nothing that was lying around.
	fromImages := dag.Companion(dagger.CompanionOpts{Image: m.baseImage(platform)})
	fromExecutables := dag.Companion(dagger.CompanionOpts{Image: m.baseImage(platform)})

	for _, name := range builtinGenerators() {
		fromImages = fromImages.WithGenerator(name, dagger.CompanionWithGeneratorOpts{
			Image: m.generatorImage(platform, name),
		})
		fromExecutables = fromExecutables.WithGeneratorExecutable(
			name,
			binaries.File(generatorExecutable(name)),
		)
	}

	// Every composition is checked and every failure reported rather than
	// stopping at the first: "it works from the images and not from the files" is
	// the finding, and it names which call broke.
	var errs []error
	if err := m.diffTrees(ctx, committed, fromImages.Generate(committed), "/companion-module/images"); err != nil {
		errs = append(errs, fmt.Errorf("composed from the generator images, the module did not reproduce the committed example: %w", err))
	}
	if err := m.diffTrees(ctx, committed, fromExecutables.Generate(committed), "/companion-module/executables"); err != nil {
		errs = append(errs, fmt.Errorf("composed from generator executables, the module did not reproduce the committed example: %w", err))
	}
	return errors.Join(errs...)
}
