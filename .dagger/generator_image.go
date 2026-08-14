// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// This file builds and checks the published generator images: avroc's own three
// generators, each shipped as an image built FROM the base image, which image.go
// builds (#127).
//
// # Why the built-ins go through the front door
//
// The base image could hold all four binaries — it did, between #126 and #127 —
// and every published image would have looked the same from outside. What that
// arrangement cannot do is test anything. A built-in reachable because the
// pipeline put it there is not a consumer of the extension mechanism; it is a
// private arrangement that happens to resemble one, and the first person to find
// out that the mechanism needs a path the contract does not promise would have
// been somebody in another repository, at their `docker build`.
//
// So the base ships the CLI and nothing else, and avroc-gen-go, avroc-gen-json
// and avroc-gen-pcf reach their images the way a stranger's generator reaches
// theirs: FROM the base, COPY into the plugin directory, chowned to the image's
// user and marked executable, nothing else touched. Every path named below is
// one docs/container/SPEC.md promises, which is the property this whole
// arrangement exists to keep honest — and GeneratorImageContract is what fails
// when it stops being true.
//
// # Why this is Dagger rather than a Dockerfile the pipeline builds
//
// docs/container/SPEC.md states these images as Dockerfiles, because a Dockerfile
// is what an adopter writes, and the two instructions below are the two
// instructions in one. The pipeline nonetheless builds them with the calls here
// rather than by handing that Dockerfile to a builder, for a reason that is about
// what is being checked rather than about taste: a `FROM ghcr.io/z5labs/avroc:v0`
// line names a *published* image, and a pull request has to check the image it
// just built. There is no way to point a Dockerfile's FROM at a container that
// exists only inside the pipeline — a registry service is not reachable from a
// Dockerfile build's image resolution — so building the committed Dockerfile
// would check the previous release's base and say nothing about this change.
//
// The drift that arrangement risks is closed by the check rather than by
// discipline: checkImageFilesystem compares each generator image against an
// exhaustive listing, so an image that differs from "the base plus exactly one
// executable at the promised path" fails, which is the same assertion the
// Dockerfile makes. Dagger's WithFile *is* COPY — both are buildkit — and the
// stage built FROM the base runs no exec at all, which is the strongest form of
// the COPY-only rule available: there is no shell in the image to run one with.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"dagger/avroc/internal/dagger"
)

// builtinGenerators is the set of generators avroc itself ships, by the name a
// manifest asks for them by.
//
// It is the single definition of that set in this module: the images published,
// the images checked, and the bundle they compose into all come from here, so a
// fourth generator becomes a published image by being added once.
func builtinGenerators() []string {
	return []string{"go", "json", "pcf"}
}

// BuiltinGenerators names every generator avroc ships, one per line.
//
// It is how the set is enumerated from outside the module — by a person reaching
// for `dagger call generator-image --name`, or by a pipeline that has to do
// something per generator. Release reads builtinGenerators directly rather than
// through here, so a fourth generator is published by being added to that one
// function; this exists so that answering "which are there" does not require
// reading Go.
func (m *Avroc) BuiltinGenerators() []string {
	return builtinGenerators()
}

// generatorExecutable is the filename avroc searches PATH for when a manifest
// asks for name — docs/plugin/SPEC.md's discovery rule, and the only reason the
// file has to land under this name rather than any other.
func generatorExecutable(name string) string {
	return "avroc-gen-" + name
}

// GeneratorImage builds the published image for one built-in generator: the base
// image with that generator copied into the plugin directory, and nothing else
// changed (#127).
//
// It is the same two instructions docs/container/SPEC.md's worked example gives
// a third-party author, and deliberately no more: the entrypoint is inherited
// rather than set, Cmd stays empty so a caller's arguments are still avroc's, and
// the only path named is the plugin directory, which is covered by the
// compatibility guarantees.
//
// platform defaults to the engine's own, which is what makes
// `dagger call generator-image --name go` useful from a checkout.
func (m *Avroc) GeneratorImage(
	ctx context.Context,
	// The generator to build an image for, by the name a manifest asks for it
	// by — one of `go`, `json` or `pcf`.
	name string,
	// Build for this platform, as `GOOS/GOARCH` — one of the published
	// platforms. Empty builds for the engine's own platform.
	// +optional
	platform string,
) (*dagger.Container, error) {
	if !slices.Contains(builtinGenerators(), name) {
		return nil, fmt.Errorf("generator %q is not one avroc ships: %v", name, builtinGenerators())
	}
	p, err := imagePlatform(ctx, platform)
	if err != nil {
		return nil, err
	}
	return m.generatorImage(p, name), nil
}

// GeneratorBundleImage builds the image that combines every built-in generator:
// one generator image with the others' executables copied into it (#127).
//
// It is here because combining generators is the question an adopter asks
// immediately after adding one — a project whose manifest names three generators
// needs one image holding all three — and because the answer had better not be
// "rebuild them all from source". It is not: each executable is copied out of the
// published generator image that already carries it, at the path
// docs/container/SPEC.md promises, which is `COPY --from=<image>` and is
// available to anybody combining images this project has never heard of.
//
// example/ is what it is checked against, since that project's manifest names all
// three; see checkImageGenerates.
func (m *Avroc) GeneratorBundleImage(
	ctx context.Context,
	// Build for this platform, as `GOOS/GOARCH` — one of the published
	// platforms. Empty builds for the engine's own platform.
	// +optional
	platform string,
) (*dagger.Container, error) {
	p, err := imagePlatform(ctx, platform)
	if err != nil {
		return nil, err
	}
	return m.generatorBundleImage(p), nil
}

// PublishGenerator pushes one built-in generator's image to address as a
// multi-platform index, and returns the digest-qualified reference it pushed.
//
// One function and one address per generator rather than a loop over a registry
// prefix: this pushes one reference, and which references a release consists of
// is Release's (#128), for the same reason Publish takes a full reference.
func (m *Avroc) PublishGenerator(
	ctx context.Context,
	// The generator to publish, by the name a manifest asks for it by.
	name string,
	// The full image reference to push, including the tag.
	address string,
	// The registry username to authenticate as. Both this and password are
	// needed for a registry that requires authentication.
	// +optional
	username string,
	// The registry password or token to authenticate with.
	// +optional
	password *dagger.Secret,
) (string, error) {
	if !slices.Contains(builtinGenerators(), name) {
		return "", fmt.Errorf("generator %q is not one avroc ships: %v", name, builtinGenerators())
	}
	return publishIndex(ctx, address, username, password, func(p dagger.Platform) *dagger.Container {
		return m.generatorImage(p, name)
	})
}

// GeneratorImageContract checks every published generator image against the
// contract its base makes (#127).
//
// It is the other half of ImageContract, and it is where the claim "avroc's own
// generators are the first consumers of the extension mechanism" is either true
// or a sentence. Four groups:
//
//   - The OCI configuration, unchanged from the base. A derived image inherits
//     Entrypoint, Cmd, User, WorkingDir and PATH, and a generator image that had
//     edited any of them would be a different program wearing avroc's filesystem.
//   - The filesystem, as an exact listing: the base's contents plus exactly one
//     executable, at the path docs/container/SPEC.md promises. That is the
//     machine-checkable form of "only COPY ran in the stage built FROM the base"
//     — anything a RUN could have done would show up as a path here — and of
//     "none of them depends on a path the contract does not promise".
//   - Each image running its own generator, through the inherited entrypoint,
//     over a project that names that generator alone. This is what "runs its
//     plugin with no further configuration" means: no environment, no arguments
//     beyond `generate`, no path to a plugin anywhere.
//   - The bundle generating the committed worked example byte-identically, as
//     the image's own UID and as an overridden one.
//
// platform restricts the check to one of the published platforms; empty runs
// every one of them, and every failure is reported rather than the first.
//
// +check
// +cache="session"
func (m *Avroc) GeneratorImageContract(
	ctx context.Context,
	// Run the check on this platform alone, as `GOOS/GOARCH` — one of the
	// published platforms. Empty covers all of them.
	// +optional
	platform string,
) error {
	platforms := imagePlatforms()
	if platform != "" {
		if !slices.Contains(platforms, dagger.Platform(platform)) {
			return fmt.Errorf("platform %q is not one this repository publishes: %v", platform, platforms)
		}
		platforms = []dagger.Platform{dagger.Platform(platform)}
	}

	var errs []error
	for _, p := range platforms {
		if err := m.generatorImageContractOn(ctx, p); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
		}
	}
	return errors.Join(errs...)
}

// generatorImage is one generator's image for one platform, and the one
// definition of it: GeneratorImage, PublishGenerator, the bundle and the contract
// check all come through here.
//
// The two calls are FROM and COPY. The owner and the mode are the derived
// Dockerfile's `--chown=65532:65532 --chmod=0755`, which docs/container/SPEC.md
// asks for so that the file is the image user's and executable; a world-readable
// world-executable file would satisfy the contract without them, which is what
// makes leaving them out a latent bug rather than an immediate one.
//
// Nothing else is set. In particular Cmd is left empty even though a derived
// image MAY set it: a generator image that supplied a default subcommand would be
// answering a question the caller is entitled to answer, and every invocation in
// the documentation passes `generate` explicitly anyway.
func (m *Avroc) generatorImage(platform dagger.Platform, name string) *dagger.Container {
	exe := generatorExecutable(name)

	return m.image(platform).WithFile(
		pluginDir+"/"+exe,
		m.binaries(platform).File(exe),
		dagger.ContainerWithFileOpts{Owner: imageUser, Permissions: executableMode},
	)
}

// generatorBundleImage is the image holding every built-in generator, built by
// stacking the published generator images rather than by copying binaries out of
// the build.
//
// That is the difference between demonstrating the mechanism and demonstrating
// that this repository can put four files in a directory: each executable is
// taken from the image that publishes it, at the promised path, which is exactly
// what somebody combining two generator images they did not build would write.
func (m *Avroc) generatorBundleImage(platform dagger.Platform) *dagger.Container {
	names := builtinGenerators()

	// FROM the first generator's image, so the bundle is a derived image of a
	// derived image — the case that would break if a generator image had quietly
	// stopped being a valid base.
	image := m.generatorImage(platform, names[0])
	for _, name := range names[1:] {
		path := pluginDir + "/" + generatorExecutable(name)
		image = image.WithFile(
			path,
			m.generatorImage(platform, name).File(path),
			dagger.ContainerWithFileOpts{Owner: imageUser, Permissions: executableMode},
		)
	}
	return image
}

// generatorImageContractOn checks one platform's generator images.
//
// Every image is checked and every failure collected rather than stopping at the
// first: three images built the same way break the same way, and one run should
// say so once for each.
func (m *Avroc) generatorImageContractOn(ctx context.Context, platform dagger.Platform) error {
	var errs []error

	for _, name := range builtinGenerators() {
		image := m.generatorImage(platform, name)
		contents := append(baseImageExecutables(), generatorExecutable(name))

		for _, err := range m.checkImageConfig(ctx, image) {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
		for _, err := range m.checkImageFilesystem(ctx, image, contents) {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
		if err := m.checkGeneratorRuns(ctx, image, name); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}

	bundle := m.generatorBundleImage(platform)
	bundleContents := baseImageExecutables()
	for _, name := range builtinGenerators() {
		bundleContents = append(bundleContents, generatorExecutable(name))
	}

	for _, err := range m.checkImageConfig(ctx, bundle) {
		errs = append(errs, fmt.Errorf("bundle: %w", err))
	}
	for _, err := range m.checkImageFilesystem(ctx, bundle, bundleContents) {
		errs = append(errs, fmt.Errorf("bundle: %w", err))
	}
	for _, err := range m.checkImageGenerates(ctx, bundle) {
		errs = append(errs, fmt.Errorf("bundle: %w", err))
	}

	return errors.Join(errs...)
}

// checkGeneratorRuns requires the image to generate with its own generator and
// nothing else — the acceptance criterion "each inherits the entrypoint and runs
// its plugin with no further configuration", executed.
//
// The project it runs against names that generator alone, so a run that
// succeeded because some other executable was lying around would not: the
// manifest asks for one generator by name, and avroc fails the run at the
// capability handshake if PATH does not resolve it.
//
// What is checked is that files came out, not which files: the committed worked
// example is what pins the bytes each generator produces, and it is checked
// against the bundle below. Duplicating it per generator would be a second copy
// of example/avroc.json's options in a file nobody would think to update.
func (m *Avroc) checkGeneratorRuns(ctx context.Context, image *dagger.Container, name string) error {
	const outDir = "out"

	project, err := generatorProbe(m.Source.File("example/schema.avdl"), name, outDir)
	if err != nil {
		return err
	}

	// WithWorkdir as well as mounting there: since #219 the image declares no
	// working directory, so a run that only mounted would start in / and fail to
	// find avroc.json. See projectMount.
	generated := image.
		WithDirectory(projectMount, project, dagger.ContainerWithDirectoryOpts{Owner: imageUser}).
		WithWorkdir(projectMount).
		WithExec([]string{"generate"}, dagger.ContainerWithExecOpts{UseEntrypoint: true}).
		Directory(projectMount + "/" + outDir)

	entries, err := generated.Entries(ctx)
	if err != nil {
		return fmt.Errorf("generating with %s through the image's entrypoint: %w", generatorExecutable(name), err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("generating with %s through the image's entrypoint wrote nothing under %s", generatorExecutable(name), outDir)
	}
	return nil
}

// generatorProbe is the smallest project that asks for one generator: the worked
// example's schema, and a manifest naming that generator and no other.
//
// It is a fixture rather than a copy of example/avroc.json, and it is written
// here rather than committed because what it exercises is discovery — that the
// executable copied into the image is found on PATH under the name the manifest
// asks for, and runs. A committed variant per generator would be three more
// manifests to keep in step with a contract that is already checked elsewhere.
func generatorProbe(schema *dagger.File, name, outDir string) (*dagger.Directory, error) {
	manifest, err := json.MarshalIndent(map[string]any{
		"inputs": []string{"schema.avdl"},
		"generators": []map[string]any{{
			"name":    name,
			"out":     outDir,
			"options": probeOptions(name),
		}},
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("rendering the probe manifest for %s: %w", name, err)
	}

	return dag.Directory().
		WithFile("schema.avdl", schema).
		WithNewFile("avroc.json", string(manifest)), nil
}

// probeOptions is the least configuration a built-in generator needs to run at
// all. docs/plugin/SPEC.md lets a generator require an option and avroc-gen-go
// requires package_name; the other two accept none, and declare so.
//
// The value is deliberately not example/avroc.json's. Nothing here compares
// output, so a value that matched would only invite somebody to read this as the
// example's configuration and change it when the example changes.
func probeOptions(name string) map[string]string {
	if name == "go" {
		return map[string]string{"package_name": "probe"}
	}
	return nil
}
