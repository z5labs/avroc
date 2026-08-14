// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// This file composes and checks the published base image — the one
// docs/container/SPEC.md describes and strangers' Dockerfiles name paths inside
// (#126, #217).
//
// # Why there is no image built here any more
//
// There used to be, and this comment used to argue at length that there had to
// be. The base-image story opened with a blocker and three ways out — extend the
// devex archetype upstream to accept image customization, build the image here,
// or accept a non-scratch base — and it took the second, because the archetype's
// image half produced one scratch image per binary with /app/<binaryName> as
// entrypoint and nothing else: no PATH, no user, no plugin directory, no second
// executable, no data file. avroc's image is a base other people build FROM and
// has to promise all of those.
//
// #217 is the first way out arriving after all. The refactored archetype makes
// every one of those a property of the standard rather than of this repository:
// /usr/local/bin is its fixed executable directory with PATH composed from the
// same constant so the two cannot drift, 65532:65532 is its pinned non-root user
// with no seam to move it, an image carries no working directory and states that
// as a decision, a contribution arrives with a document describing it, and
// App.WithApp composes one application's executable into another's plugin
// directory — which is what a generator image is, matched platform by platform
// and exec'd before anything is pushed. So what is left here is the two
// contributions that are avroc's own and the checks that hold the contract, and
// the image itself belongs to nobody in this file.
//
// Two things the archetype does *not* express are worth naming, because both
// were promises this repository made and neither is worked around here:
//
//   - A writable 1777 /tmp, which #218 removed the need for by writing each
//     invocation's descriptor into the output tree.
//   - /work as a WorkingDir, which #219 withdrew, and which the archetype now
//     rests a refusal on rather than merely omitting.
//
// A third arrived with the adoption itself and is recorded in
// docs/container/SPEC.md rather than here: the base image no longer carries an
// *empty* /usr/local/bin, because a contribution to any directory the image's
// PATH resolves against is refused — content with no architecture that something
// could find by name is how an arm64 image comes to run an amd64 executable —
// and composition, the sanctioned route, would mean shipping a generator in the
// base. See that document's "The plugin directory".
//
// The third way out, a non-scratch base, stayed rejected on the contract rather
// than on size: docs/container/SPEC.md's "No shell" is what makes extension
// COPY-only, and the archetype's base is scratch too.
//
// # Why the contract is a check rather than a comment
//
// Every promise docs/container/SPEC.md makes is a value in an OCI image
// configuration or a file in the filesystem it describes, so every one of them
// is machine-checkable — and each is the kind of promise that breaks silently.
// A base image whose PATH lost /usr/local/bin still runs avroc perfectly and
// fails only in somebody else's repository, at the point where their generator
// is not found. ImageContract is that document's Compatibility guarantees table
// executed rather than read.
//
// The images built FROM this one — avroc's own three generators, which are the
// first consumers of the contract — are in generator_image.go (#127).
package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"dagger/avroc/internal/dagger"
)

// The published image's contract, as constants. Each one is a row of
// docs/container/SPEC.md's compatibility guarantees table, and changing one here
// without changing it there is the drift ImageContract exists to catch.
//
// pluginDir lives in main.go, because the regeneration stage's scratch container
// resolves generators on PATH out of the same directory the published image does
// and both read the constant; that container is no longer the published layout in
// full, and main.go's comment on the constant says which half it reproduces.
// Two of the rows that used to be here are gone, and each absence is a decision
// rather than an omission. They are recorded as comments in the block below,
// where the constant they replace used to be, because a deleted constant leaves
// nothing for a reader to find and both deletions are things a future change
// would otherwise re-add in good faith.
const (
	// There is no working directory in this image, and its absence is a decision
	// (#219). WorkingDir was /work and /work was created here owned by the image's
	// user, so that a caller could mount a project over it and pass no further
	// arguments. Both are gone: the caller names the mount point on the command
	// line — `-v "$PWD:/work" -w /work` — and the image declares nothing, which is
	// what docs/container/SPEC.md's "The working directory" now says. Nothing here
	// stands in for it, and in particular projectMount — which is where the checks
	// in this module mount a project — is defined beside the check that uses it
	// rather than in this block, because it is the caller's choice and not the
	// image's contract.

	// There is no temporary directory in this image, and its absence is a
	// decision (#218). A 1777 /tmp used to be grafted on here, because avroc
	// wrote each invocation's descriptor into a directory it created under
	// os.TempDir and a scratch image without one failed every generation with
	// `stat /tmp: no such file or directory` — the first thing ImageContract
	// caught. avroc writes the descriptor into the generator's own output tree
	// instead, so the requirement is gone rather than carried: the `docker run`
	// lines docs/container/SPEC.md and the README print need no
	// `--tmpfs /tmp:mode=1777`, and the image is once again exactly scratch plus
	// the files that document names.
	//
	// Since #217 the archetype nonetheless sets TMPDIR=/tmp, as part of a
	// standardized environment whose whole point is that what a process reads out of
	// HOME and TMPDIR is a property of the image rather than of the runtime. So the
	// published image names a temporary directory it does not contain, and that is
	// worth writing down rather than leaving to be discovered: a program that read
	// it would get ENOENT on a path the configuration plainly advertises. What makes
	// it harmless is #218 — avroc reads no ambient temporary directory at all, and
	// internal/avroc.TestAvrocReadsNoAmbientTemporaryDirectory holds that as a
	// property of the source across internal/, cmd/ and avrocpb/, so the thing that
	// would break is a thing the tests refuse. Both halves are checked, and by
	// different checks: that nothing reads it is that test's, and that the image
	// does not carry it is imageContents' — the listing below is exhaustive, so a
	// /tmp appearing in the image is a failure, in either direction.
	//
	// A deployment that wants one mounts a tmpfs or an emptyDir over it, which is
	// what the archetype documents and is the reason nothing may be contributed
	// under that path.

	// imageUID and imageGID are the pinned non-root identity the image runs as.
	// They are numbers rather than a name because the image has no /etc/passwd
	// for a name to resolve against, and because a derived Dockerfile's
	// COPY --chown and a Kubernetes securityContext are both written against
	// the number.
	imageUID = 65532
	imageGID = 65532

	// imageUser is that identity in the form the OCI configuration's User field
	// and Dagger's owner arguments both take.
	imageUser = "65532:65532"

	// descriptorSetPath is where the IR FileDescriptorSet ships, for a plugin
	// author whose language has no protobuf code generation in the build.
	descriptorSetPath = "/usr/local/share/avroc/ir.binpb"

	// appDir is where the archetype puts an application's own binary, and homeDir
	// is what it points HOME at. Neither is avroc's to choose and neither is in
	// docs/container/SPEC.md: they are here because the filesystem listing is
	// exhaustive, so a path the image carries has to be named somewhere even when
	// nothing may depend on it.
	//
	// They are read from this repository rather than imported because the
	// archetype exposes no constant for either, which is the same reason
	// manifestFilename is written out below: a check reads the image the way a
	// person would, and the day either value moves the listing is what says so.
	appDir  = "/app"
	homeDir = "/home/nonroot"

	// homeMode is the mode HOME's directory has: traversable and readable,
	// writable by nobody, and root-owned so that the refusal survives a caller
	// overriding the runtime user. avroc writes nothing under HOME — since #218 it
	// reads no ambient temporary directory either — so an empty read-only
	// directory is the whole of what this row means.
	homeMode = 0o555

	// cliExecutable is the avroc CLI, and the only executable the base image
	// ships. Its path is deliberately not part of the contract; that it is the
	// one and only binary in the base is, because "the base is scratch plus the
	// files docs/container/SPEC.md names" is what makes a generator image's
	// contents exactly what somebody copied into it.
	//
	// It is also the name the archetype gives the binary it builds, which is the
	// basename of the module path in go.mod and not something a caller states —
	// so cliPackage below is the package that produces it and the two agree
	// because this repository's module is called avroc.
	cliExecutable = "avroc"

	// cliPackage is the package the archetype builds the base image's binary
	// from. It is stated where the version is, rather than beside the entrypoint,
	// because the archetype derives the executable's *name* from go.mod and takes
	// only the package to compile.
	cliPackage = "./cmd/" + cliExecutable

	// executableMode is the mode every executable in the plugin directory has.
	//
	// It is 0555 rather than the 0755 this pipeline used to write, because the
	// archetype owns it now: every byte it puts in an image is read-only, on the
	// grounds that an image whose files the application can rewrite is one whose
	// published digest stops describing what is running. What the contract needs
	// is unchanged and is the reason the mode is world-anything at all — an
	// executable any UID can run, so `--user $(id -u):$(id -g)` is an ordinary
	// configuration rather than a workaround.
	executableMode = 0o555

	// contributedFileMode is the mode a contributed data file lands with, and it
	// is the archetype's for executableMode's reason. The FileDescriptorSet is
	// the one file in this image that has it; world-readable is the half the
	// contract depends on, because a caller who overrides the UID reads the same
	// file.
	contributedFileMode = 0o444

	// devVersion is the version every build that is not a release publishes
	// under, and therefore the version every check builds.
	//
	// The archetype requires one: it stamps main.version into the binary and puts
	// the value in an OCI annotation, and it refuses a version that could not be
	// an image tag. A release states its own (release.go's planRelease, from the
	// refs at HEAD); everything else is not a release and says so, rather than
	// borrowing the last one's number. Nothing in docs/container/SPEC.md covers an
	// annotation, so this value is invisible to a consumer — it exists so that
	// "which release is this" has an answer that is never wrong.
	devVersion = "v0.0.0-dev"
)

// imagePlatforms is the set of platforms the image is published for, and the
// single definition of it in this module — the regeneration stage covers the
// same set, because the platforms a generator executable actually runs on are
// the image's.
func imagePlatforms() []dagger.Platform {
	return []dagger.Platform{"linux/amd64", "linux/arm64"}
}

// Image is the published base image for one platform, as a container.
//
// It exists so that a contributor can load or export the image the pipeline
// builds — `dagger call image export --path ./avroc.tar` — and for nothing else.
// Every promise docs/container/SPEC.md makes about it is the archetype's now:
// baseApp is the whole of avroc's contribution, which is the package to compile
// and one data file.
//
// It hands back a *dagger.Container rather than the App it came from because
// Dagger refuses a function that returns an object type belonging to a
// dependency module, so an `app` entrypoint threading the archetype's chain out
// to the command line is not expressible here. That is also why there is no
// Publish beside this one: a publish is signed and attested by construction —
// the archetype fails one it cannot produce provenance for — and the only
// caller that has an OIDC token to exchange is the release workflow, which
// reaches it through Release.
//
// No generator is in it. avroc's own three are composed onto this App (#127,
// #217, GeneratorImage), which is the only arrangement in which they are
// consumers of the extension mechanism rather than a private arrangement that
// happens to look like one.
//
// platform defaults to the engine's own, which is what makes
// `dagger call image` useful from a checkout; a value must be one of the
// published platforms, so a typo is an error rather than an image nobody
// publishes.
func (m *Avroc) Image(
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
	return m.baseImage(p), nil
}

// ImageContract checks the published image against every promise
// docs/container/SPEC.md makes about it (#126).
//
// It is a check rather than a paragraph because each promise is depended on from
// a repository this project cannot see and breaks without breaking anything
// here: an image whose PATH lost the plugin directory runs avroc perfectly and
// fails at the point where somebody else's generator is not found.
//
// Three groups, all on the real image rather than on a description of it:
//
//   - The OCI configuration — Entrypoint, Cmd, User, WorkingDir and PATH.
//   - The filesystem, as an exact list of every path in it with its owner and
//     mode. Exact rather than a spot check, because "the base is scratch plus
//     the files this document names" is the promise, and it is the same
//     assertion as no shell, no libc and no package manager.
//   - The entrypoint being the CLI, by running it: a base holding no generator
//     cannot be asked to generate anything, so what pins Entrypoint to avroc
//     rather than to some other executable is `help` through the entrypoint and
//     the usage avroc prints. Generation is checked where generation is
//     possible, on the images that carry a generator (GeneratorImageContract).
//   - The consequence of declaring no working directory (#219): a `generate` run
//     with none set fails naming the manifest it could not find, rather than
//     succeeding silently or failing in words a caller cannot act on.
//
// platform restricts the check to one of the published platforms; empty runs
// every one of them, and every failure is reported rather than the first,
// because "it holds on amd64 and not on arm64" is the finding.
//
// +check
// +cache="session"
func (m *Avroc) ImageContract(
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
		if err := m.imageContractOn(ctx, p); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
		}
	}
	return errors.Join(errs...)
}

// baseApp is the base image as the archetype's App, and the one definition of
// it: Image, every generator image composed onto it and every check that reads it
// all come through here, so there is no arrangement in which the image a check
// passed is not the image that was pushed.
//
// Two lines are avroc's, and everything docs/container/SPEC.md covers other than
// the FileDescriptorSet's path falls out of the archetype: the executable
// directory and its membership of PATH, the entrypoint, the empty Cmd, the absent
// working directory and the 65532:65532 that owns the files and runs the process.
//
// The FileDescriptorSet arrives as a contribution, which means it arrives with a
// document: every byte an image carries is described, because the SPDX and
// CycloneDX pair a publish attaches accounts for the whole image rather than for
// the binary the chain compiled. A protobuf FileDescriptorSet has no ecosystem
// and therefore no module able to produce a document for it, which is the case
// Z5Labs.FileDocument exists for — so this is a call and not a hand-rolled SPDX
// file in this repository. The licence is stated explicitly rather than left to
// default: MIT is this repository's, and NOASSERTION would be the honest answer to
// a question nobody had asked rather than to one whose answer is known. No version
// is given, because the file is a rendering of this commit's protos and inventing
// a number would put a value in a field consumers compare.
//
// version is the version the App publishes and stamps under. It is a parameter
// rather than devVersion outright so that Release passes the release's own and
// the check stages pass devVersion, and so that the two cannot be different code
// paths — the container a contract check reads is built exactly as the container
// a release pushes is.
func (m *Avroc) baseApp(version string) *dagger.Z5LabsApp {
	set := m.IrDescriptorSet()

	return m.appChain().
		App(version, dagger.Z5LabsGoChainAppOpts{
			Pkg:       cliPackage,
			Platforms: imagePlatforms(),
		}).
		WithFile(descriptorSetPath, set, dag.Z5Labs().FileDocument(set, dagger.Z5LabsFileDocumentOpts{
			License: "MIT",
			Name:    "avroc-ir-descriptor-set",
		}))
}

// baseImage is one platform's base image, for the checks and for the stages that
// run avroc rather than publish it.
//
// It is an accessor onto the App and not an image build: the container it hands
// back is the one the archetype assembled and the one a publish would push, which
// is the whole reason the checks are allowed to read it.
func (m *Avroc) baseImage(platform dagger.Platform) *dagger.Container {
	return m.baseApp(devVersion).Container(platform)
}

// appChain is the Go chain an App is built from — the source with its git
// metadata folded in, and nothing configured about the check stages.
//
// The lint and test configuration deliberately does not travel here. Ci runs the
// checks and App builds; a chain configured for both would make a change to the
// lint version a change to the image's cache key, and the archetype's own comment
// on WithBuild says the same thing from the other side.
func (m *Avroc) appChain() *dagger.Z5LabsGoChain {
	return dag.Z5Labs().Go(m.appSource())
}

// binaries builds every executable this repository ships, for one platform.
//
// It no longer builds anything that goes into the base image — the archetype
// compiles the CLI itself, from cliPackage — and what is left is the two callers
// that need a loose executable rather than an image: the generator Apps, which
// package a prebuilt binary because the archetype names a Go application after
// its module and avroc's three generators share one module (generator_image.go),
// and CompanionModule, which hands executables to the companion module the way an
// adopter with no generator image would.
//
// They are cross-compiled by the toolchain container rather than compiled under
// emulation, and CGO is off because the image is scratch: there is no libc in it,
// and a dynamically linked executable would find no loader and fail with `no such
// file or directory` naming a file that is plainly there.
func (m *Avroc) binaries(platform dagger.Platform) *dagger.Directory {
	return dag.Go().Build(m.Source, dagger.GoBuildOpts{
		Pkg:        "./cmd/...",
		Platform:   string(platform),
		DisableCgo: true,
	})
}

// imageContractOn checks one platform's image.
//
// Every group is run and every failure collected rather than stopping at the
// first: a change that broke the entrypoint most likely broke the user and the
// working directory too, and one run should say so.
func (m *Avroc) imageContractOn(ctx context.Context, platform dagger.Platform) error {
	return errors.Join(m.checkBaseImage(ctx, m.baseImage(platform))...)
}

// checkBaseImage is every assertion the base image is held to, over a container
// somebody else chose.
//
// It takes the container rather than a platform so that there is exactly one
// definition of "the base image is correct" and three call sites for it:
// ImageContract, which builds one per published platform; the release gate, which
// passes the very container it is about to push (release.go's releaseContract);
// and nothing else. The reviewer of #217 caught the release gate having grown a
// second, hand-copied list — which would have meant a check added here silently
// stopping short of a release, the exact hole putting the gate inside Release was
// meant to close.
//
// Every group is run and every failure collected rather than stopping at the
// first: a change that broke the entrypoint most likely broke the user and the
// working directory too, and one run should say so.
func (m *Avroc) checkBaseImage(ctx context.Context, image *dagger.Container) []error {
	errs := m.checkImageConfig(ctx, image)
	errs = append(errs, m.checkImageFilesystem(ctx, image, baseImageExecutables())...)
	if err := m.checkImageIsTheCLI(ctx, image); err != nil {
		errs = append(errs, err)
	}
	if err := m.checkAMissingWorkingDirectoryIsLegible(ctx, image); err != nil {
		errs = append(errs, err)
	}
	return errs
}

// checkImageIsTheCLI runs the entrypoint and requires avroc's usage back.
//
// The behavioural half of the entrypoint guarantee, and the only half a base
// image can carry on its own: docs/container/SPEC.md promises that a caller's
// arguments are avroc's arguments, and `help` is the one subcommand that needs
// no project, no generator and no writable directory to answer. An entrypoint
// that had been repointed at some other executable would answer differently or
// not at all.
//
// Generation is the stronger check and it is on the generator images, because a
// base holding no generator has nothing to generate with — which is itself the
// point of #127, and is why this check exists rather than the base quietly
// keeping a copy of every built-in so that it could be run here.
func (m *Avroc) checkImageIsTheCLI(ctx context.Context, image *dagger.Container) error {
	const want = "Usage: avroc <command>"

	out, err := image.
		WithExec([]string{"help"}, dagger.ContainerWithExecOpts{UseEntrypoint: true}).
		Stdout(ctx)
	if err != nil {
		return fmt.Errorf("running `help` through the image's entrypoint: %w", err)
	}
	if !strings.Contains(out, want) {
		return fmt.Errorf("`help` through the image's entrypoint printed %q, which does not contain %q: the entrypoint is not the avroc CLI", out, want)
	}
	return nil
}

// checkAMissingWorkingDirectoryIsLegible runs `generate` with no working directory
// and no project, and requires the run to fail saying it could not find the
// manifest.
//
// This is the other half of the working directory guarantee (#219), and it is the
// half that is easy to leave unchecked. Since the image declares no WorkingDir, an
// unset one lands the process in / — so the new way to hold this image wrong is to
// mount a project and forget the `-w`, which is now the single most likely mistake
// a caller of this release makes. docs/container/SPEC.md promises what happens
// then: the run "fails in avroc's own words, naming the manifest it could not
// find, before it has done any work". Every other stage in this module passes the
// flag, so without this the one new failure mode is the one path nothing runs.
//
// The base image is where it belongs even though the base ships no generator,
// because loadManifest runs before the capability handshake: a run from / fails on
// the missing avroc.json and never reaches the point where having no generator
// would matter. That ordering is the thing being relied on, and it is why the
// assertion is on the message and not merely on the exit code — a future change
// that moved the manifest read after the handshake, or that failed with something
// a person cannot act on, would leave the document's sentence wrong with the exit
// code still non-zero.
func (m *Avroc) checkAMissingWorkingDirectoryIsLegible(ctx context.Context, image *dagger.Container) error {
	run := image.WithExec(
		[]string{"generate"},
		dagger.ContainerWithExecOpts{UseEntrypoint: true, Expect: dagger.ReturnTypeAny},
	)

	code, err := run.ExitCode(ctx)
	if err != nil {
		return fmt.Errorf("running `generate` with no working directory: %w", err)
	}
	if code == 0 {
		return errors.New("`generate` with no working directory and no project exited 0: the image declares no WorkingDir, so this run started in / where there is no avroc.json, and a success there means a caller who forgets `-w` is told nothing")
	}

	stderr, err := run.Stderr(ctx)
	if err != nil {
		return fmt.Errorf("reading stderr from `generate` with no working directory: %w", err)
	}
	if !strings.Contains(stderr, manifestFilename) {
		return fmt.Errorf("`generate` with no working directory exited %d but its stderr never mentions %s, so a caller who forgot `-w` cannot tell what went wrong; it said: %s", code, manifestFilename, stderr)
	}
	return nil
}

// checkImageConfig checks the fields of the OCI image configuration a derived
// image inherits.
func (m *Avroc) checkImageConfig(ctx context.Context, image *dagger.Container) []error {
	var errs []error

	// The structural half of the entrypoint guarantee. Exactly one element,
	// because "the arguments a caller passes to docker run are avroc's
	// arguments" is only true when there is nothing in Entrypoint for them to
	// arrive after; and that element has to be an executable the image actually
	// ships, because Entrypoint is otherwise free to name a path that is not
	// there and fail at run time in somebody else's pipeline.
	//
	// It is deliberately not compared to a literal, since the CLI's own path is
	// implementation detail and pinning it here would make a promise this
	// project has explicitly not made. What pins the entrypoint to the CLI is
	// behaviour rather than shape: checkImageIsTheCLI on the base, and
	// checkImageGenerates on the images that carry a generator, which runs
	// `generate` through it and byte-compares the result.
	//
	// The base image's executables rather than the checked image's, because this
	// runs on derived images too and a derived image inherits its Entrypoint: a
	// generator image whose Entrypoint had become its own generator would be a
	// different program wearing avroc's filesystem, which is exactly the edit
	// docs/container/SPEC.md forbids.
	executables := baseImageExecutablePaths()
	entrypoint, err := image.Entrypoint(ctx)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("reading Entrypoint: %w", err))
	case len(entrypoint) == 0:
		errs = append(errs, errors.New("the image's Entrypoint is empty: a derived image inherits no program"))
	case len(entrypoint) > 1:
		errs = append(errs, fmt.Errorf("the image's Entrypoint is %v, want exactly one element: a caller's arguments are avroc's arguments, and anything else here would come before them", entrypoint))
	case !slices.Contains(executables, entrypoint[0]):
		errs = append(errs, fmt.Errorf("the image's Entrypoint is %v, which is not one of the executables the image ships (%v)", entrypoint, executables))
	}

	args, err := image.DefaultArgs(ctx)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("reading Cmd: %w", err))
	case len(args) != 0:
		errs = append(errs, fmt.Errorf("the image's Cmd is %v, want empty: a caller's arguments are avroc's arguments", args))
	}

	user, err := image.User(ctx)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("reading User: %w", err))
	case user != imageUser:
		errs = append(errs, fmt.Errorf("the image's User is %q, want %q", user, imageUser))
	}

	// WorkingDir is asserted *empty* rather than not asserted at all (#219). The
	// image no longer declares one, and the temptation is to delete the check
	// with the value — but an unasserted field is exactly how a base layer's
	// inherited value arrives unnoticed, which is what this whole file exists to
	// stop. So the assertion flips from a value to its absence: the day something
	// sets a working directory again, here or upstream, this says so.
	workdir, err := image.Workdir(ctx)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("reading WorkingDir: %w", err))
	case workdir != "":
		errs = append(errs, fmt.Errorf("the image's WorkingDir is %q, want it empty: since #219 the caller names the mount point and points the working directory at it, and the image declares none", workdir))
	}

	path, err := image.EnvVariable(ctx, "PATH")
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("reading PATH: %w", err))
	case !slices.Contains(strings.Split(path, ":"), pluginDir):
		errs = append(errs, fmt.Errorf("PATH is %q, which does not contain the plugin directory %q", path, pluginDir))
	}

	return errs
}

// imageContents is every path the image is allowed to contain, with the owner
// and mode each one must have.
//
// It is exhaustive on purpose. docs/container/SPEC.md's "No shell" says the base
// is scratch plus the files that document names, and an exhaustive list is the
// only form of that claim which stays true when somebody adds a file: a spot
// check for /bin/sh passes on an image carrying a busybox under another name.
//
// The parent directories are root-owned and that is intentional — the plugin
// directory is the only one promised to the image's user, and a root-owned parent
// with mode 0755 is what stops the running user replacing the tree above it.
//
// There is no working directory row (#219). The image declares no WorkingDir and
// creates no directory to be one, so the listing loses /work and gains nothing:
// where a project is mounted is the caller's, and a path the caller chooses is
// not a path this image can be exhaustive about.
//
// executables is what is expected in the plugin directory, which is the one
// thing that differs between the base image and an image built FROM it: the
// difference between the two listings is exactly the set of files somebody
// COPYed in, and checking a derived image against this is how "only COPY ran in
// the stage built FROM the base" becomes an assertion rather than a claim.
func imageContents(pluginExecutables []string) map[string]imageEntry {
	const dirMode = 0o755

	contents := map[string]imageEntry{
		// The application's own directory and the CLI in it. Neither path is
		// covered by docs/container/SPEC.md — that document says the CLI's own
		// path is implementation detail and a derived image reaches it through
		// the entrypoint — and both are listed here anyway, because the listing
		// is exhaustive or it is nothing. appDir is root-owned so the running
		// user cannot unlink the binary it is about to exec.
		appDir:                   {0, 0, dirMode},
		cliPath():                {imageUID, imageGID, executableMode},
		"/home":                  {0, 0, dirMode},
		homeDir:                  {0, 0, homeMode},
		"/usr":                   {0, 0, dirMode},
		"/usr/local":             {0, 0, dirMode},
		"/usr/local/share":       {0, 0, dirMode},
		"/usr/local/share/avroc": {0, 0, dirMode},
		descriptorSetPath:        {imageUID, imageGID, contributedFileMode},
	}

	// The plugin directory appears exactly when something is in it, and that is
	// the row #217 changed rather than a looseness in the check (see this file's
	// comment and docs/container/SPEC.md's "The plugin directory"). The base
	// image carries no generator, and a contribution to a directory the image's
	// PATH resolves against is refused by the archetype — so the base's listing
	// has no /usr/local/bin at all, and a generator image's has it because
	// composing a generator created it.
	// It is root-owned, where this pipeline used to make it the image user's.
	// Nothing depends on the ownership of the directory — a derived image's COPY
	// runs as root in the builder, and 0755 is traversable by every UID, so the
	// executable inside it is reachable by the running user and by an overridden
	// one. What a consumer does depend on is the mode and ownership of the
	// *executable*, which is the row below and is unchanged in substance.
	// docs/container/SPEC.md's "What it means for ownership" says so now.
	if len(pluginExecutables) > 0 {
		contents[pluginDir] = imageEntry{0, 0, dirMode}
	}
	for _, name := range pluginExecutables {
		contents[pluginDir+"/"+name] = imageEntry{imageUID, imageGID, executableMode}
	}
	return contents
}

// baseImageExecutables is what ships in the base image's plugin directory:
// nothing at all.
//
// avroc's own generators are not here, and their absence is the substance of
// #127. A base that carried them would make the three published generator images
// decorative — the built-ins would be reachable by a path nobody copied them to,
// which is precisely the private arrangement that would leave the extension
// mechanism untested by its own author.
//
// The CLI is not here either, and that is #217: the archetype puts an
// application's own binary in appDir and names it absolutely in the entrypoint,
// so the plugin directory is for what an extension adds and for nothing else.
// docs/container/SPEC.md already said the CLI's path was implementation detail,
// which is what made that free to change.
func baseImageExecutables() []string {
	return nil
}

// cliPath is where the CLI lands inside the image.
//
// It is derived from the archetype's layout rather than promised: appDir plus the
// binary's name, which the archetype takes from go.mod's module path. Nothing
// outside this module may depend on it — docs/container/SPEC.md's "The CLI's own
// path is not part of the contract" — and the checks depend on it only to be
// exhaustive about the filesystem and to recognise the entrypoint.
func cliPath() string {
	return appDir + "/" + cliExecutable
}

// baseImageExecutablePaths is every executable the base image ships, where it
// lands, which is what an Entrypoint would have to name.
func baseImageExecutablePaths() []string {
	paths := []string{cliPath()}
	for _, name := range baseImageExecutables() {
		paths = append(paths, pluginDir+"/"+name)
	}
	return paths
}

// imageEntry is one path's expected ownership and mode.
type imageEntry struct {
	uid  int
	gid  int
	mode int
}

func (e imageEntry) String() string {
	return fmt.Sprintf("%d:%d %04o", e.uid, e.gid, e.mode)
}

// checkImageFilesystem compares every path in the image against imageContents.
//
// The listing is produced by one `find` in a container that has one, over the
// image's root filesystem mounted as data. It cannot be produced by running
// anything *in* the image, which is the point of the image having no shell, and
// a Dagger Entries walk would report the paths without their owners — and
// ownership is half of what is being checked.
//
// executables is what the plugin directory is expected to hold: the CLI alone
// for the base image, and the CLI plus whatever was copied in for an image built
// FROM it.
func (m *Avroc) checkImageFilesystem(ctx context.Context, image *dagger.Container, executables []string) []error {
	return m.checkImageContents(ctx, image, imageContents(executables))
}

// checkImageContents is checkImageFilesystem against a listing given outright
// rather than derived from a set of executables in the plugin directory.
//
// It exists because docs/container/SPEC.md's worked example states the owner and
// the mode of the file it copies in, and the check that builds that example
// (worked_example.go) reads both out of the committed Dockerfile rather than
// assuming them: the SPEC permits a world-executable root-owned file as well as
// the chowned one the example uses, so the expected entry is a function of the
// document and not a constant here.
func (m *Avroc) checkImageContents(ctx context.Context, image *dagger.Container, want map[string]imageEntry) []error {
	const mountedAt = "/image"

	// Numeric %U and %G rather than %u and %g: the listing container has no
	// passwd entry for 65532, so the symbolic forms would print the number
	// anyway on a good day and a name on a bad one.
	listing, err := dag.Go().
		Container(m.Source).
		WithMountedDirectory(mountedAt, image.Rootfs()).
		WithExec([]string{
			"find", mountedAt, "-mindepth", "1", "-printf", `%U %G %m %p\n`,
		}).
		Stdout(ctx)
	if err != nil {
		return []error{fmt.Errorf("listing the image filesystem: %w", err)}
	}

	got := make(map[string]imageEntry, len(want))

	var errs []error
	for _, line := range strings.Split(strings.TrimSpace(listing), "\n") {
		if line == "" {
			continue
		}
		entry, path, err := parseFindLine(line, mountedAt)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		got[path] = entry
	}

	for path, wantEntry := range want {
		gotEntry, ok := got[path]
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("%s: missing from the image", path))
		case gotEntry != wantEntry:
			errs = append(errs, fmt.Errorf("%s: is %v, want %v", path, gotEntry, wantEntry))
		}
	}
	for path := range got {
		if _, ok := want[path]; !ok {
			errs = append(errs, fmt.Errorf("%s: present in the image and not in the contract; the base is scratch plus the files docs/container/SPEC.md names, plus whatever a derived image COPYed in, and nothing else", path))
		}
	}

	// Sorted, because a map walk would order the failures differently on every
	// run and make two reports of one break look like two breaks.
	slices.SortFunc(errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
	return errs
}

// parseFindLine reads one `%U %G %m %p` line and strips the mount prefix, so
// that the paths compared are the paths inside the image.
func parseFindLine(line, prefix string) (imageEntry, string, error) {
	fields := strings.Fields(line)
	if len(fields) != 4 {
		return imageEntry{}, "", fmt.Errorf("unreadable listing line %q", line)
	}

	var entry imageEntry
	if _, err := fmt.Sscanf(fields[0], "%d", &entry.uid); err != nil {
		return imageEntry{}, "", fmt.Errorf("unreadable uid in %q: %w", line, err)
	}
	if _, err := fmt.Sscanf(fields[1], "%d", &entry.gid); err != nil {
		return imageEntry{}, "", fmt.Errorf("unreadable gid in %q: %w", line, err)
	}
	if _, err := fmt.Sscanf(fields[2], "%o", &entry.mode); err != nil {
		return imageEntry{}, "", fmt.Errorf("unreadable mode in %q: %w", line, err)
	}
	return entry, strings.TrimPrefix(fields[3], prefix), nil
}

// checkImageGenerates runs the image the way docs/container/SPEC.md says to run
// it, and checks both what it produced and who owns it.
//
// It needs every generator example/avroc.json names, so it runs against the
// image that combines all three (#127) rather than against the base, which ships
// none. That is not a weakening of the check: the tree it compares is the same
// committed worked example, produced through the same entrypoint, by generators
// that reached the image the way a stranger's generator reaches theirs.
//
// Twice, and the two runs differ along both axes a caller can vary. The first is
// the documented invocation: the image's own UID, mounted at the path every
// `docker run` in the documents prints. The second overrides the UID the way a
// caller writing output into a bind mount is told to — which is what turns "an
// overridden UID is an ordinary configuration" from a sentence into something
// that fails when it stops being true — *and* mounts somewhere else entirely,
// which is the same treatment for docs/container/SPEC.md's "`/work` there is the
// caller's choice and nothing more" (#219).
//
// The second mount point is what stops projectMount becoming the old workDir
// under a new name. Since the image declares no working directory, the claim is
// that no path is privileged; a suite that only ever mounted at /work would go on
// passing the day something in avroc, in a check helper or in a `Directory(…)`
// read grew a dependence on that literal, and checkImageConfig's `workdir != ""`
// would not see it — an image with an empty WorkingDir and a hard-coded /work
// somewhere below is exactly the state this asserts against.
func (m *Avroc) checkImageGenerates(ctx context.Context, image *dagger.Container) []error {
	committed := m.Source.Directory("example")

	runs := []struct {
		user  string
		mount string
	}{
		{imageUser, projectMount},
		{"1234:1234", "/srv/somewhere-else"},
	}

	var errs []error
	for _, run := range runs {
		generated := generateInImage(image, committed, run.user, run.mount)

		if err := m.diffAgainstCommitted(ctx, committed, generated, run.user, run.mount); err != nil {
			errs = append(errs, err)
		}
		if err := m.checkOutputOwnership(ctx, generated, run.user); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// projectMount is the path the checks in this module mount a project at when they
// are running the *documented* invocation, and it is the **caller's** choice
// rather than the image's (#219).
//
// It is deliberately not in the block of contract constants at the top of this
// file, and that placement is the point of the story: the image declares no
// WorkingDir and creates no directory to be one, so nothing about the image
// depends on this value. /work is chosen because it is the path
// docs/container/SPEC.md's invocations print and the daggerverse module defaults
// to, so a check using it runs the documented command rather than a private
// variant of it.
//
// That it is not privileged is asserted rather than stated: generateInImage takes
// the mount point as an argument and checkImageGenerates gives one of its two runs
// a different one. See the comment there.
const projectMount = "/work"

// manifestFilename is the file `avroc generate` reads out of the working
// directory, and the string a run that could not find it has to name.
//
// It is written here rather than imported from internal/avroc: .dagger is a
// separate Go module, and this is a check reading avroc's output the way a person
// does. The duplication is the same kind internal/plugin.OutputPath keeps against
// avroc's own path check — a value asserted from the outside, not shared with the
// thing being asserted about.
const manifestFilename = "avroc.json"

// generateInImage runs `generate` through the image's own entrypoint over a
// copy of the worked example mounted at mount, and returns the tree it left
// behind.
//
// UseEntrypoint is what makes this the same invocation as
// `docker run --rm -v "$PWD:<mount>" -w <mount> <image> generate`: the arguments
// are avroc's arguments, and nothing here names the CLI by path. No environment is
// set either — the image's own PATH is what has to resolve the generators copied
// into it, which is the one job it has.
//
// mount is a parameter rather than projectMount read directly, because since #219
// the image declares no working directory and the document therefore promises that
// any mount point does. A function that could only mount at one path would make
// that promise uncheckable.
//
// WithWorkdir as well as mounting there, for the same reason: without it the
// process runs from /, where there is no avroc.json, and the mount alone would
// leave the project somewhere avroc never looks. That is the `-w` in the command
// line above, and daggerverse/avroc and .dagger/main.go had both already written it
// for this reason.
func generateInImage(image *dagger.Container, example *dagger.Directory, user, mount string) *dagger.Directory {
	return image.
		WithUser(user).
		WithDirectory(mount, example, dagger.ContainerWithDirectoryOpts{Owner: user}).
		WithWorkdir(mount).
		WithExec([]string{"generate"}, dagger.ContainerWithExecOpts{UseEntrypoint: true}).
		Directory(mount)
}

// diffAgainstCommitted requires the generated tree to be byte-identical to the
// committed worked example.
//
// mount is in the message because the two runs differ by it as well as by user
// (#219): "the image did not reproduce the committed example" is a different
// finding when it happened at a mount point other than the documented one, and a
// report that named only the user would send the reader looking at the UID.
func (m *Avroc) diffAgainstCommitted(
	ctx context.Context,
	committed, generated *dagger.Directory,
	user, mount string,
) error {
	if err := m.diffTrees(ctx, committed, generated, "/image-contract"); err != nil {
		return fmt.Errorf("as user %s, mounted at %s, the image did not reproduce the committed example: %w", user, mount, err)
	}
	return nil
}

// diffTrees requires two directories to be byte-identical, mounting them beneath
// at so that the paths in the report say which run produced which side.
//
// diff rather than a directory digest, for the reason the regeneration stage
// gives: a digest reports only that something differs, and covers metadata two
// runs are entitled to disagree about.
//
// It is shared by every check in this module that compares a generated tree
// against a committed one — the image contract's and the companion module's —
// because what "identical" means here is a property of the trees rather than of
// which check is asking, and a second copy would be a second chance to compare
// them less strictly.
func (m *Avroc) diffTrees(ctx context.Context, want, got *dagger.Directory, at string) error {
	wantAt := at + "/want"
	gotAt := at + "/got"

	// --text because every file either generator writes is text, and a diff that
	// said only "binary files differ" would report the failure without reporting
	// what it was.
	_, err := dag.Go().
		Container(m.Source).
		WithMountedDirectory(wantAt, want).
		WithMountedDirectory(gotAt, got).
		WithExec([]string{"diff", "--recursive", "--unified", "--text", wantAt, gotAt}).
		Sync(ctx)
	return err
}

// checkOutputOwnership requires every file the run produced to be owned by the
// UID and GID the container was running as.
//
// That is what makes `--user $(id -u):$(id -g)` the recommended invocation
// rather than a workaround: a caller's host kernel sees a number, and the number
// it sees has to be theirs.
func (m *Avroc) checkOutputOwnership(ctx context.Context, generated *dagger.Directory, user string) error {
	const mountedAt = "/output"

	listing, err := dag.Go().
		Container(m.Source).
		WithMountedDirectory(mountedAt, generated).
		WithExec([]string{"find", mountedAt, "-mindepth", "1", "-printf", `%U:%G %p\n`}).
		Stdout(ctx)
	if err != nil {
		return fmt.Errorf("listing the output of the run as user %s: %w", user, err)
	}

	var wrong []string
	for _, line := range strings.Split(strings.TrimSpace(listing), "\n") {
		if line == "" {
			continue
		}
		owner, path, ok := strings.Cut(line, " ")
		if !ok {
			return fmt.Errorf("unreadable listing line %q", line)
		}
		if owner != user {
			wrong = append(wrong, fmt.Sprintf("%s is owned by %s", strings.TrimPrefix(path, mountedAt), owner))
		}
	}
	if len(wrong) > 0 {
		slices.Sort(wrong)
		return fmt.Errorf("running as user %s wrote output owned by somebody else: %s", user, strings.Join(wrong, "; "))
	}
	return nil
}

// imagePlatform resolves the platform argument Image takes: empty is the
// engine's own, and anything else has to be a platform this repository actually
// publishes, so that a typo is an error rather than an image nobody ships.
func imagePlatform(ctx context.Context, platform string) (dagger.Platform, error) {
	if platform == "" {
		return dag.DefaultPlatform(ctx)
	}
	platforms := imagePlatforms()
	if !slices.Contains(platforms, dagger.Platform(platform)) {
		return "", fmt.Errorf("platform %q is not one this repository publishes: %v", platform, platforms)
	}
	return dagger.Platform(platform), nil
}
