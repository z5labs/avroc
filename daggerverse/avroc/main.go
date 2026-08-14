// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// Package main implements avroc's companion Dagger module: code generation as a
// pipeline step, for a caller who would rather not write a Dockerfile.
//
// A caller hands Generate a project directory — an avroc.json manifest, the
// schemas it names, whatever output a previous run committed — and gets the tree
// avroc left behind back as a Directory. Nothing is installed on the host, and
// no image is built: the published images are pulled, composed with WithGenerator,
// and run.
//
//	dagger call -m github.com/z5labs/avroc/daggerverse/avroc \
//	  with-generator --name go \
//	  generate --source . \
//	  export --path .
//
// # This is a convenience, not a contract
//
// The contract is the published image: docs/container/SPEC.md says what is on
// PATH, what the entrypoint is, that the caller chooses where a project is mounted
// and which UID the process runs as, and it is that document a third party builds
// against. Everything here is those promises spelled as Dagger calls, and every
// one of them can be written by hand as `docker run --rm -v "$PWD:/work" -w /work`
// instead.
//
// So this module gets no SPEC.md, deliberately (docs/CONVENTIONS.md, "What
// belongs here"). A specification for it would imply the contract is a property
// of the module — that a caller reaching for `docker run`, a Kubernetes Job or a
// Dockerfile were on a lesser path — when the module is the one thing here that
// could be deleted without breaking a single promise avroc makes. What it needs
// to say, it says in this comment and in `dagger call --help`.
//
// It also states nothing about the plugin CLI contract. `avroc-gen-<name>`, the
// argument vector, the descriptor and the exit codes are docs/plugin/SPEC.md's,
// they hold with no container anywhere in the picture, and the only thing this
// module knows about them is the filename discovery searches PATH for.
//
// # Why generators are composed rather than built
//
// WithGenerator takes an executable out of a published generator image and puts
// it in the plugin directory. That is `COPY --from=<image>` — the same two
// instructions docs/container/SPEC.md's worked example gives somebody writing a
// Dockerfile, and the same ones .dagger/generator_image.go uses to build the
// images this pulls from — so a caller whose project names three generators gets
// one image holding three without a build context, a registry to push to, or a
// Dockerfile to keep in step with an avroc release.
//
// WithGeneratorExecutable is the other half, for the generator that has no image
// yet: a File, from `dag.Go().Build()` or from anywhere else, lands at the same
// path under the same name. A generator author checking their plugin against a
// real avroc run needs that before they have published anything at all.
//
// # What this does not do
//
// It does not build avroc from source, and it is not this repository's pipeline.
// `dagger call ci`, the image builds and the contract checks are the root module
// at the repository root; this one is published for other people's pipelines and
// knows nothing about a checkout of avroc. The two meet at exactly one point:
// the root module's CompanionModule check drives this module over example/ with
// the images that pipeline just built, which is how a change that breaks this is
// caught on the pull request that made it rather than by a stranger.
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"dagger/avroc/internal/dagger"
)

const (
	// pluginDir is the directory on the image's PATH that avroc discovers
	// generators in — docs/container/SPEC.md's plugin directory, and the one path
	// in the image this module needs to know. Everything else it needs (the
	// entrypoint, the working directory, the user) it reads off the container
	// rather than assuming.
	pluginDir = "/usr/local/bin"

	// projectDir is where a project is mounted when the image does not say
	// otherwise, and since #219 the published image says nothing: it declares no
	// WorkingDir, so this is the path that is actually used rather than a fallback
	// for a container built some other way. /work is unchanged, because the mount
	// point is this project's convention either way — what moved is which side
	// names it, and it is this module now rather than the image.
	//
	// A container that *does* declare a working directory still wins, which is what
	// keeps New able to accept an image this module did not build.
	projectDir = "/work"

	// executableMode is the mode a generator lands with. It is the derived
	// Dockerfile's `--chmod=0755`: readable and executable by the image's own UID
	// and by any UID a caller overrides it with, which is what keeps
	// `generate --user "$(id -u):$(id -g)"` an ordinary configuration rather than
	// a workaround.
	executableMode = 0o755
)

// Avroc is one composed avroc image, plus the coordinates the next WithGenerator
// call needs to find a published one.
//
// Every function on it is either a builder returning a new Avroc or a terminal
// that runs something, so a call chain reads as the image being assembled and
// then used; nothing here mutates.
type Avroc struct {
	// Container is the image generation runs in, with whatever generators have
	// been composed into it so far.
	// +private
	Container *dagger.Container

	// Repository and Version are what New resolved, kept so that a later
	// WithGenerator with no image of its own pulls the generator that matches the
	// avroc already in the container. A generator from one release beside a CLI
	// from another is a combination nobody tested, and defaulting to it silently
	// is how somebody would end up in it.
	// +private
	Repository string
	// +private
	Version string
}

// New selects the avroc release to run.
//
// The defaults pull the published base image, which carries the CLI and no
// generator at all — the generators are separate images (docs/container/SPEC.md,
// "avroc's own generators"), so a project that generates anything needs at least
// one WithGenerator call. That is one call more than a bundle image would cost
// and it is deliberate: which generators are in the image is the caller's
// manifest, and an image that shipped all of them would decide it for them.
func New(
	// The avroc release to run, as a `<tag>` on the published images. A
	// major-version tag like v0 follows releases and a full version like v0.2.0
	// pins one; a build that must not move at all pins a digest, which is what
	// --image is for.
	// +optional
	// +default="v0"
	version string,
	// The registry repository the images are pulled from, as `<host>/<path>` with
	// no tag. The generator images derive from it by the rule this project
	// publishes them under: <repository>-gen-<name>.
	//
	// It is an argument because where the images live is not something
	// docs/container/SPEC.md promises — a mirror, an internal registry or an
	// air-gapped copy serving the same digests satisfies every requirement in that
	// document identically, and a caller behind one should not have to give up
	// this module to use it.
	// +optional
	// +default="ghcr.io/z5labs/avroc"
	repository string,
	// Run in this image instead of pulling one. It has to keep the promises
	// docs/container/SPEC.md makes — avroc as the entrypoint, the plugin directory
	// on PATH — because that is all this module drives it through.
	//
	// This is how avroc's own pipeline checks the module against the images it
	// just built rather than against the last release, and how a caller tries a
	// change to avroc before it ships.
	// +optional
	image *dagger.Container,
) *Avroc {
	if image == nil {
		image = dag.Container().From(repository + ":" + version)
	}
	return &Avroc{Container: image, Repository: repository, Version: version}
}

// WithGenerator adds one generator to the image by copying its executable out of
// a generator image.
//
// This is the whole of "adding a plugin without writing a Dockerfile": the file
// is taken from the published image at the path docs/container/SPEC.md promises,
// which is what `COPY --from` does, and works just as well for a generator image
// this project has never heard of — pass it as image.
//
// Repeated calls compose, so a project whose manifest names three generators is
// three calls and one image.
func (m *Avroc) WithGenerator(
	// The generator to add, by the `<name>` avroc.json asks for it by — go, json
	// and pcf are the ones avroc ships. Discovery is by filename, so this is the
	// name in avroc-gen-<name> and nothing else.
	name string,
	// Take the executable from this image instead of pulling the published
	// generator image for name. Any image carrying the generator in the plugin
	// directory will do, including one that is not published.
	// +optional
	image *dagger.Container,
) (*Avroc, error) {
	if err := checkGeneratorName(name); err != nil {
		return nil, err
	}
	if image == nil {
		image = dag.Container().From(m.Repository + "-gen-" + name + ":" + m.Version)
	}
	return m.WithGeneratorExecutable(name, image.File(pluginDir+"/"+generatorExecutable(name)))
}

// WithGeneratorExecutable adds one generator to the image from an executable
// file, for a generator that ships no image — most often one the caller has just
// built in the same pipeline.
//
// The file has to be a Linux executable for the image's platform, statically
// linked or matched to whatever the image provides, which for avroc's own scratch
// base means static. That is the same requirement docs/container/SPEC.md's worked
// example states as `CGO_ENABLED=0`, and it is the caller's to meet: nothing here
// can check it, and a dynamically linked generator fails at exec time with the
// kernel's message rather than avroc's.
func (m *Avroc) WithGeneratorExecutable(
	// The generator's `<name>`, as avroc.json asks for it. The file lands as
	// avroc-gen-<name> whatever it was called before, because discovery is by
	// filename.
	name string,
	// The generator executable.
	executable *dagger.File,
) (*Avroc, error) {
	if err := checkGeneratorName(name); err != nil {
		return nil, err
	}

	// Permissions rather than an owner: the mode is what makes the file runnable,
	// by the image's own UID and by any UID a caller overrides it with, and the
	// owner it would otherwise be given is a property of the image this module was
	// handed rather than one it is entitled to assume.
	next := *m
	next.Container = m.Container.WithFile(
		pluginDir+"/"+generatorExecutable(name),
		executable,
		dagger.ContainerWithFileOpts{Permissions: executableMode},
	)
	return &next, nil
}

// Image is the composed image, for a caller who wants to do something with it
// other than generate: publish it to their own registry, run a different avroc
// subcommand, or look at what ended up in it.
//
// Publishing one is a reasonable thing to do — it is the same image a Dockerfile
// would have produced — but it is not a release of avroc and carries none of the
// signatures or attestations one does.
func (m *Avroc) Image() *dagger.Container {
	return m.Container
}

// Generate runs `avroc generate` over a project and returns the tree it left
// behind.
//
// The whole project comes back, not just the generated files: avroc writes each
// generator's output into the directory the manifest names, which is usually
// inside the source tree, and it maintains avroc.gen.json — the record of what
// the last run produced, which the next run prunes against. So the Directory this
// returns is the project as it should now be committed, and the ordinary use is
// to export it straight over the source:
//
//	dagger call ... generate --source . export --path .
//
// Nothing is passed to avroc but the subcommand. The manifest is the sole source
// of inputs, generators and their options, and a flag here that could override
// any of it would be a second place a project's generation is configured.
func (m *Avroc) Generate(
	ctx context.Context,
	// The project directory: an avroc.json manifest, the schemas it names, and
	// whatever output a previous run committed.
	source *dagger.Directory,
	// Run as this user, as `UID:GID`. The image's own pinned UID is used when this
	// is empty, which is right for a Dagger caller, since the Directory that comes
	// back is exported by the engine as whoever runs the export. Set it when the
	// output has to arrive owned by a particular UID.
	// +optional
	user string,
) (*dagger.Directory, error) {
	c := m.Container
	if user != "" {
		c = c.WithUser(user)
	}

	// Both read off the container rather than assumed, because New accepts an
	// image this module did not build. The owner has to be the UID the process
	// runs as or avroc cannot write its output beside the schemas; the working
	// directory has to be the image's, because `avroc generate` reads avroc.json
	// from the working directory and takes no path argument that could say
	// otherwise.
	owner, err := c.User(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the user the image runs as: %w", err)
	}
	workdir, err := c.Workdir(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the image's working directory: %w", err)
	}
	if strings.TrimSpace(workdir) == "" {
		workdir = projectDir
	}

	// WithWorkdir as well as mounting there, because the published image declares
	// no working directory (#219) and therefore runs from / — where there is no
	// avroc.json — so the mount alone would leave the project somewhere avroc
	// never looks. This was written against a hypothetical image that declared
	// none; it is now the image this module pulls by default.
	//
	// UseEntrypoint, so this is the same invocation as
	// `docker run --rm -v "$PWD:/work" -w /work <image> generate`: the arguments
	// are avroc's, and nothing here names the CLI by path.
	return c.
		WithDirectory(workdir, source, dagger.ContainerWithDirectoryOpts{Owner: owner}).
		WithWorkdir(workdir).
		WithExec([]string{"generate"}, dagger.ContainerWithExecOpts{UseEntrypoint: true}).
		Directory(workdir), nil
}

// generatorExecutable is the filename avroc searches PATH for when a manifest
// asks for name — docs/plugin/SPEC.md's discovery rule, and the only thing this
// module knows about the plugin contract.
func generatorExecutable(name string) string {
	return "avroc-gen-" + name
}

// checkGeneratorName enforces docs/plugin/SPEC.md's rule on a generator name:
// non-empty, and no `/`.
//
// It is checked here because name is interpolated into a path in the image and
// into an image reference, and neither failure says what went wrong. A name with
// a separator writes the executable to a directory that is not on PATH, and the
// run then fails much later, at the capability handshake, complaining that a
// generator the caller plainly asked for cannot be found; an empty one produces
// `avroc-gen-`, which is a filename avroc will never search for. A `..` needs no
// rule of its own, because without a separator it cannot leave the plugin
// directory.
//
// That rule and no more. docs/plugin/SPEC.md's preference for lowercase ASCII,
// digits and hyphens is a **SHOULD** — a convention about names that are easy to
// type, not a constraint on what avroc will run — and a module that refused a
// name avroc would have resolved would be making the contract smaller from the
// outside.
func checkGeneratorName(name string) error {
	switch {
	case name == "":
		return errors.New("a generator name is required: it is the <name> in avroc-gen-<name>, which is what avroc resolves on PATH")
	case strings.Contains(name, "/"):
		return fmt.Errorf(
			"generator name %q contains a /: a name is a single filename component (docs/plugin/SPEC.md), and one carrying a path separator would put the executable somewhere avroc never searches",
			name,
		)
	}
	return nil
}
