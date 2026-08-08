// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// This file builds and checks the published base image — the one
// docs/container/SPEC.md describes and strangers' Dockerfiles name paths inside
// (#126).
//
// # Why the image is built here rather than by the GoApp archetype
//
// The base-image story opened with a blocker and three ways out: extend the
// devex GoApp archetype upstream to accept image customization, build the image
// in this module, or accept a non-scratch base. This file is the second, and the
// reason is that the first two are not alternatives at the same level.
//
// GoApp's imageForPlatform produces a scratch image holding one binary at
// /app/<binaryName>, with that path as the entrypoint and nothing else set: no
// PATH, no USER, no working directory, no second binary, no data file. avroc's
// image has to promise all six, because it is a base that other people build
// FROM — and four of the six are not settings GoApp is missing so much as a
// different shape of image. avroc publishes an image that is a base: one binary
// in a directory on PATH, into which other images COPY plugins that are found
// by a PATH search rather than run as the entrypoint (#127); GoApp publishes one
// image per binary and has no notion of a derived image. Teaching GoApp that shape
// would be teaching it avroc's plugin model, which is a contract in this
// repository's docs/ and belongs nowhere else. What would be left upstream after
// the customization was general enough to keep — a settable USER, WORKDIR and
// PATH — is a handful of Container calls, which is precisely what is below.
//
// The third way out, a non-scratch base, was rejected on the contract rather
// than on size. docs/container/SPEC.md's "No shell" is a load-bearing promise:
// it is what makes extension COPY-only, and what makes the image's contents
// exactly the executables somebody deliberately put there. A distroless or
// busybox base would be a smaller change here and a larger one there.
//
// What is *not* rebuilt here is anything about what "checked" means. fmt, vet,
// lint and `go test -race` still route through the Z5Labs standard by way of Ci,
// the binaries are still built by the shared devex Go module's Build, and this
// file adds only the image layout the standard has no opinion about. That is the
// split main.go's package comment anticipated, and no upstream devex change was
// needed to reach it.
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
// is deliberately the same layout as the published image and both read it.
const (
	// workDir is the working directory a caller mounts a project at.
	workDir = "/work"

	// tmpDir is a writable temporary directory, and it is the one thing in the
	// image that docs/container/SPEC.md does not name — deliberately, since that
	// document lists it under everything in the filesystem that is
	// implementation detail rather than a covered guarantee.
	//
	// It is here because avroc writes each invocation's descriptor into a
	// directory it creates under os.TempDir, so a scratch image without one
	// fails every generation with `stat /tmp: no such file or directory` — the
	// first thing ImageContract caught. tmpDirMode is 1777, root-owned,
	// world-writable and sticky, which is the ordinary arrangement and the one
	// that keeps working when a caller overrides the UID with
	// `--user $(id -u):$(id -g)`.
	tmpDir     = "/tmp"
	tmpDirMode = 0o1777

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

	// cliExecutable is the avroc CLI, and the only executable the base image
	// ships. Its path is deliberately not part of the contract; that it is the
	// one and only binary in the base is, because "the base is scratch plus the
	// files docs/container/SPEC.md names" is what makes a generator image's
	// contents exactly what somebody copied into it.
	cliExecutable = "avroc"

	// executableMode is the mode every executable in the plugin directory has:
	// readable and executable by the image's user and by any UID a caller
	// overrides it with, which is what makes `--user $(id -u):$(id -g)` an
	// ordinary configuration rather than a workaround.
	executableMode = 0o755
)

// imagePlatforms is the set of platforms the image is published for, and the
// single definition of it in this module — the regeneration stage covers the
// same set, because the platforms a generator executable actually runs on are
// the image's.
func imagePlatforms() []dagger.Platform {
	return []dagger.Platform{"linux/amd64", "linux/arm64"}
}

// Image builds the published base image for one platform.
//
// It is the image docs/container/SPEC.md describes, and every promise that
// document makes is made here: the CLI in /usr/local/bin with that directory on
// PATH, the CLI as the entrypoint with an empty Cmd, /work as the working
// directory, UID and GID 65532 owning both directories and running the process,
// the IR FileDescriptorSet at its documented path, and nothing else in the
// filesystem at all.
//
// No generator is in it. avroc's own three are shipped as images built FROM this
// one (#127, GeneratorImage), which is the only arrangement in which they are
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
	return m.image(p), nil
}

// Publish pushes the image to address as a multi-platform index covering every
// platform in imagePlatforms, and returns the digest-qualified reference it
// pushed.
//
// address carries the tag, because this function's job is to push one reference
// and not to decide which references exist. Which tags a release carries is
// Release's, derived from the refs at HEAD in one place (#128); this is what a
// person calls to put an image on a test registry, and what Release calls once
// per tag.
//
// One index rather than one image per platform, so that a `FROM` line naming the
// tag resolves to the manifest for whatever platform the builder is on, which is
// what makes the published set of platforms invisible to a derived Dockerfile.
func (m *Avroc) Publish(
	ctx context.Context,
	// The full image reference to push, including the tag.
	address string,
	// The registry username to authenticate as. Both this and password are
	// needed for a registry that requires authentication, which is every
	// registry this project publishes to.
	// +optional
	username string,
	// The registry password or token to authenticate with.
	// +optional
	password *dagger.Secret,
) (string, error) {
	return publishIndex(ctx, address, username, password, m.image)
}

// publishIndex pushes one multi-platform index built from build, which is called
// once per published platform.
//
// It is shared by Publish and PublishGenerator rather than written twice: the
// credential handling and the index shape are properties of publishing an avroc
// image, not of which image is being published, and a second copy would be a
// second chance to get the anonymous-push case wrong.
func publishIndex(
	ctx context.Context,
	address, username string,
	password *dagger.Secret,
	build func(dagger.Platform) *dagger.Container,
) (string, error) {
	platforms := imagePlatforms()
	variants := make([]*dagger.Container, 0, len(platforms))
	for _, p := range platforms {
		variants = append(variants, build(p))
	}

	// Half a credential is refused rather than quietly dropped. Skipping the
	// auth because one of the two is missing turns a typo into an anonymous
	// push, which fails at the registry with a message about permissions rather
	// than about the argument that was actually wrong — and on a registry that
	// happened to allow it, would not fail at all.
	switch {
	case username != "" && password == nil:
		return "", errors.New("username was given without password: both are needed to authenticate, and publishing with neither pushes anonymously")
	case username == "" && password != nil:
		return "", errors.New("password was given without username: both are needed to authenticate, and publishing with neither pushes anonymously")
	}

	c := dag.Container()
	if username != "" {
		c = c.WithRegistryAuth(address, username, password)
	}
	return c.Publish(ctx, address, dagger.ContainerPublishOpts{PlatformVariants: variants})
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

// image is the base image for one platform, and the one definition of it: Image,
// Publish and every image built FROM this one all come through here, so there is
// no arrangement in which the image a check passed is not the image that was
// pushed, nor one in which a generator image is built on a base nobody checked.
//
// The order of the calls below is the order docs/container/SPEC.md states the
// promises in, which is not an accident worth preserving so much as one worth
// not breaking: WithEntrypoint clears Cmd, so it comes before the WithoutDefaultArgs
// that asserts Cmd is empty rather than after it.
func (m *Avroc) image(platform dagger.Platform) *dagger.Container {
	cli := dag.Directory().WithFile(
		cliExecutable,
		m.binaries(platform).File(cliExecutable),
		dagger.DirectoryWithFileOpts{Permissions: executableMode},
	)

	return dag.Container(dagger.ContainerOpts{Platform: platform}).
		// The plugin directory, owned by the image's user so that a generator
		// copied in by a derived image lands somewhere that user can execute
		// from. The CLI goes here too; its path is explicitly not part of the
		// contract, and this is simply the one directory the image has.
		WithDirectory(pluginDir, cli, dagger.ContainerWithDirectoryOpts{
			Owner: imageUser,
		}).
		// The FileDescriptorSet, world-readable because a caller who overrides
		// the UID reads the same file.
		WithFile(descriptorSetPath, m.IrDescriptorSet(), dagger.ContainerWithFileOpts{
			Owner:       imageUser,
			Permissions: 0o644,
		}).
		// The working directory has to exist in the base image and be owned by
		// the image's user: a caller who mounts over it inherits nothing
		// surprising, and one who does not can still generate into it.
		WithDirectory(workDir, dag.Directory(), dagger.ContainerWithDirectoryOpts{
			Owner: imageUser,
		}).
		// A temporary directory, because avroc writes each invocation's
		// descriptor into one. Not owned by the image's user: 1777 is what makes
		// it usable by whichever UID the container is actually running as.
		WithDirectory("/", tmpDirectory()).
		// PATH is set outright rather than appended to, because a scratch image
		// has no PATH to append to. Only the guarantee that it contains the
		// plugin directory is covered; the rest of the value is not.
		WithEnvVariable("PATH", pluginDir).
		WithWorkdir(workDir).
		WithUser(imageUser).
		WithEntrypoint([]string{pluginDir + "/" + cliExecutable}).
		WithoutDefaultArgs()
}

// binaries builds every executable this repository ships, for one platform.
//
// One build for the base image and the three generator images alike, so the CLI
// a generator image inherits and the generator copied into it are the same
// binaries the base image was checked with. They are cross-compiled by the
// toolchain container rather than compiled under emulation, and CGO is off
// because the image is scratch: there is no libc in it, and a dynamically linked
// executable would find no loader and fail with `no such file or directory`
// naming a file that is plainly there.
func (m *Avroc) binaries(platform dagger.Platform) *dagger.Directory {
	return dag.Go().Build(m.Source, dagger.GoBuildOpts{
		Pkg:        "./cmd/...",
		Platform:   string(platform),
		DisableCgo: true,
	})
}

// tmpDirectory is a directory holding nothing but `tmp`, with mode 1777, for
// grafting onto the image's root.
//
// It is staged by a real mkdir in a container that has one rather than by
// WithDirectory's permissions argument, because that argument sets the mode of
// the files copied *into* a directory and not the mode of the directory itself
// — which leaves /tmp root-owned at 0755, and every generation failing with
// `permission denied` the moment the process is not root. That was the second
// thing ImageContract caught, and it is the reason the check runs the image
// rather than only reading its configuration.
func tmpDirectory() *dagger.Directory {
	const staging = "/staging"

	return dag.Go().
		Container(dag.Directory()).
		WithExec([]string{"install", "-d", "-m", "1777", staging + tmpDir}).
		Directory(staging)
}

// imageContractOn checks one platform's image.
//
// Every group is run and every failure collected rather than stopping at the
// first: a change that broke the entrypoint most likely broke the user and the
// working directory too, and one run should say so.
func (m *Avroc) imageContractOn(ctx context.Context, platform dagger.Platform) error {
	image := m.image(platform)

	errs := m.checkImageConfig(ctx, image)
	errs = append(errs, m.checkImageFilesystem(ctx, image, baseImageExecutables())...)
	if err := m.checkImageIsTheCLI(ctx, image); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
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

	workdir, err := image.Workdir(ctx)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("reading WorkingDir: %w", err))
	case workdir != workDir:
		errs = append(errs, fmt.Errorf("the image's WorkingDir is %q, want %q", workdir, workDir))
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
// The parent directories are root-owned and that is intentional — only the
// plugin directory and the working directory are promised to the image's user,
// and a root-owned parent with mode 0755 is what stops the running user
// replacing the tree above them.
//
// executables is what is expected in the plugin directory, which is the one
// thing that differs between the base image and an image built FROM it: the
// difference between the two listings is exactly the set of files somebody
// COPYed in, and checking a derived image against this is how "only COPY ran in
// the stage built FROM the base" becomes an assertion rather than a claim.
func imageContents(executables []string) map[string]imageEntry {
	const (
		dirMode  = 0o755
		dataMode = 0o644
	)

	contents := map[string]imageEntry{
		"/usr":                   {0, 0, dirMode},
		"/usr/local":             {0, 0, dirMode},
		"/usr/local/share":       {0, 0, dirMode},
		"/usr/local/share/avroc": {0, 0, dirMode},
		descriptorSetPath:        {imageUID, imageGID, dataMode},
		pluginDir:                {imageUID, imageGID, dirMode},
		workDir:                  {imageUID, imageGID, dirMode},
		tmpDir:                   {0, 0, tmpDirMode},
	}
	for _, name := range executables {
		contents[pluginDir+"/"+name] = imageEntry{imageUID, imageGID, executableMode}
	}
	return contents
}

// baseImageExecutables is what ships in the base image's plugin directory: the
// CLI, and nothing else.
//
// avroc's own generators are not here, and their absence is the substance of
// #127. A base that carried them would make the three published generator images
// decorative — the built-ins would be reachable by a path nobody copied them to,
// which is precisely the private arrangement that would leave the extension
// mechanism untested by its own author.
func baseImageExecutables() []string {
	return []string{cliExecutable}
}

// baseImageExecutablePaths is baseImageExecutables where they land, which is what
// an Entrypoint would have to name.
func baseImageExecutablePaths() []string {
	names := baseImageExecutables()
	paths := make([]string, 0, len(names))
	for _, name := range names {
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

	want := imageContents(executables)
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
// Twice, as two different users. The first run is the documented default. The
// second overrides the UID the way a caller writing output into a bind mount is
// told to, and is what turns "an overridden UID is an ordinary configuration"
// from a sentence into something that fails when it stops being true.
func (m *Avroc) checkImageGenerates(ctx context.Context, image *dagger.Container) []error {
	committed := m.Source.Directory("example")

	var errs []error
	for _, user := range []string{imageUser, "1234:1234"} {
		generated := generateInImage(image, committed, user)

		if err := m.diffAgainstCommitted(ctx, committed, generated, user); err != nil {
			errs = append(errs, err)
		}
		if err := m.checkOutputOwnership(ctx, generated, user); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// generateInImage runs `generate` through the image's own entrypoint over a
// copy of the worked example mounted at the working directory, and returns the
// tree it left behind.
//
// UseEntrypoint is what makes this the same invocation as
// `docker run --rm -v "$PWD:/work" <image> generate`: the arguments are avroc's
// arguments, and nothing here names the CLI by path. No environment is set
// either — the image's own PATH is what has to resolve the generators copied
// into it, which is the one job it has.
func generateInImage(image *dagger.Container, example *dagger.Directory, user string) *dagger.Directory {
	return image.
		WithUser(user).
		WithDirectory(workDir, example, dagger.ContainerWithDirectoryOpts{Owner: user}).
		WithExec([]string{"generate"}, dagger.ContainerWithExecOpts{UseEntrypoint: true}).
		Directory(workDir)
}

// diffAgainstCommitted requires the generated tree to be byte-identical to the
// committed worked example.
//
// diff rather than a directory digest, for the reason the regeneration stage
// gives: a digest reports only that something differs, and covers metadata two
// runs are entitled to disagree about.
func (m *Avroc) diffAgainstCommitted(
	ctx context.Context,
	committed, generated *dagger.Directory,
	user string,
) error {
	const (
		committedAt = "/image-contract/committed"
		generatedAt = "/image-contract/generated"
	)

	_, err := dag.Go().
		Container(m.Source).
		WithMountedDirectory(committedAt, committed).
		WithMountedDirectory(generatedAt, generated).
		WithExec([]string{"diff", "--recursive", "--unified", "--text", committedAt, generatedAt}).
		Sync(ctx)
	if err != nil {
		return fmt.Errorf("as user %s, the image did not reproduce the committed example: %w", user, err)
	}
	return nil
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
