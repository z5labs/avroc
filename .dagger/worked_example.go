// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// This file builds and runs docs/container/SPEC.md's worked example: the
// multi-stage Dockerfile a stranger copies out of that document to add a
// generator avroc has never heard of (#129).
//
// # Why the pipeline builds a document
//
// The extension mechanism is only real if somebody who has never read this
// repository can follow it end to end, and the worked example is the only place
// that claim is made in a form they can run. A worked example that has stopped
// working is worse than none at all, because it is the first thing an adopter
// tries and the last thing anybody here would notice was broken: nothing in the
// Go build, the tests or the image checks reads it, so it can rot through a
// dozen releases in perfect silence. So the Dockerfile in the document is the
// input to this check rather than a copy of it — the fenced block is extracted
// from docs/container/SPEC.md and built, and an edit that breaks it fails CI on
// the pull request that made it.
//
// Extracted rather than committed beside the document for the same reason. A
// Dockerfile in a testdata directory would be the thing that is checked and the
// thing in the document would be the thing people read, and the two would drift
// exactly as far apart as nobody notices.
//
// # Why the final stage is composed rather than built
//
// generator_image.go's header gives the reason in full and it applies unchanged
// here: `FROM ghcr.io/z5labs/avroc:v0` names a *published* image, and a pull
// request has to check the base it just built. There is no way to point a
// Dockerfile's FROM at a container that exists only inside the pipeline.
//
// So the build stage — every line that compiles the generator, including the
// heredocs that make the example runnable with an empty build context and the
// CGO_ENABLED=0 that makes the result run in a scratch image — is handed to
// buildkit exactly as committed, and the final stage is *interpreted*: its FROM
// is required to name the published base, and its COPY is read for the flags and
// the paths the document actually wrote and replayed against the base image this
// pipeline built. Dagger's WithFile is COPY — both are buildkit — and the stage
// runs no exec at all, which is the strongest available form of the COPY-only
// rule.
//
// Interpreting is only honest if it cannot silently diverge, so the parser
// refuses anything it does not know how to replay: an instruction other than
// COPY in the final stage is an error naming it, rather than a line that quietly
// has no effect on the image being checked. A worked example that grows an ENV
// has to teach this function what an ENV means before CI will accept it.
package main

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"dagger/avroc/internal/dagger"
)

const (
	// containerSpec is the document this check reads, and the only source of the
	// Dockerfile it builds.
	containerSpec = "docs/container/SPEC.md"

	// workedExampleHeading is the section the Dockerfile is taken from, matched
	// as a whole line. The document's other fenced Dockerfiles state avroc's own
	// generator images and the combining of two published ones, which
	// generator_image.go already builds and checks; this is the one a stranger
	// is given.
	workedExampleHeading = "## Worked example: adding a generator"

	// publishedBaseImage is the repository half of the reference the worked
	// example's final stage is required to name — without a tag, because which
	// tag the document tells an adopter to pin is docs/container/SPEC.md's
	// decision and a check that fixed it here would fail the day that advice
	// changed.
	publishedBaseImage = "ghcr.io/z5labs/avroc"

	// generatorPrefix is what docs/plugin/SPEC.md's discovery rule makes of a
	// generator's name, and so what the worked example's copied executable has to
	// be called for avroc to find it at all.
	generatorPrefix = "avroc-gen-"
)

// WorkedExampleImage builds the image docs/container/SPEC.md's worked example
// describes: a generator that is not one of avroc's own, compiled by the
// document's own build stage and copied into the plugin directory of the base
// image this pipeline just built (#129).
//
// It is here so that a person can look at the thing the check below judges —
// `dagger call worked-example-image terminal` is not available in an image with
// no shell, but exporting it, listing it or running the CLI in it all are.
func (m *Avroc) WorkedExampleImage(
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
	example, err := m.workedExample(ctx)
	if err != nil {
		return nil, err
	}
	return example.image(m.baseImage(p), p), nil
}

// WorkedExample is the worked example executed rather than read (#129): it
// extracts the Dockerfile from docs/container/SPEC.md, builds it against the
// base image this pipeline produced, and runs the result over the example
// project's schema.
//
// Five groups, and each one is a sentence in that document that would otherwise
// only be a sentence:
//
//   - The Dockerfile is what the document says it is. Two stages, the second of
//     them FROM the published base, containing no RUN — the COPY-only rule the
//     absence of a shell imposes — and copying exactly one executable, named
//     avroc-gen-<name> for a name that is not one of avroc's own generators.
//   - Those requirements have teeth: each one is run again over a copy of the
//     document broken in exactly that way, and has to reject it. See rules.
//   - It builds. The build stage is handed to buildkit as committed, so a
//     heredoc that no longer parses, a Go program that no longer compiles or a
//     toolchain that has moved on fails here.
//   - The image is the base plus that one file, with the owner and the mode the
//     document's own COPY line asked for. This is the machine-checkable form of
//     "only COPY ran", and it is also what catches a destination path that is no
//     longer on PATH.
//   - It generates. The image runs `generate` through the inherited entrypoint
//     over a project built from the committed example's schema, as the image's
//     own UID and as an overridden one, and has to produce output owned by
//     whoever ran it.
//
// One platform — the engine's own — rather than the published matrix. What is
// under test is a document, and the document is platform-agnostic: the
// platform-specific claims are the image's, and ImageContract and Regeneration
// already run on every platform avroc publishes for. Building the golang stage
// under emulation would buy a slower answer to a question nobody asked.
//
// +check
// +cache="session"
func (m *Avroc) WorkedExample(ctx context.Context) error {
	example, err := m.workedExample(ctx)
	if err != nil {
		return err
	}

	platform, err := dag.DefaultPlatform(ctx)
	if err != nil {
		return fmt.Errorf("resolving the engine's platform: %w", err)
	}

	want, err := example.contents()
	if err != nil {
		return err
	}

	image := example.image(m.baseImage(platform), platform)

	var errs []error
	if err := example.rules(); err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, m.checkImageConfig(ctx, image)...)
	errs = append(errs, m.checkImageContents(ctx, image, want)...)
	errs = append(errs, m.checkWorkedExampleGenerates(ctx, image, example.generator, example.mount)...)
	return errors.Join(errs...)
}

// workedExample reads the Dockerfile out of the document and validates it.
func (m *Avroc) workedExample(ctx context.Context) (*workedExample, error) {
	spec, err := m.Source.File(containerSpec).Contents(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", containerSpec, err)
	}

	dockerfile, err := workedExampleDockerfile(spec)
	if err != nil {
		return nil, err
	}
	mount, err := workedExampleMount(spec)
	if err != nil {
		return nil, err
	}

	example, err := newWorkedExample(dockerfile)
	if err != nil {
		return nil, err
	}
	example.mount = mount
	example.spec = spec
	return example, nil
}

// workedExample is the document's Dockerfile, parsed and judged.
type workedExample struct {
	// dockerfile is the fenced block verbatim, as it appears in the document.
	dockerfile string
	// buildStage is the Dockerfile that builds the generator: everything before
	// the final FROM, with the document's own preamble — the syntax directive —
	// kept, since a heredoc depends on it.
	buildStage string
	// base is the image reference the final stage is FROM, tag and all.
	base string
	// copy is the final stage's single COPY, read for the flags and paths it
	// actually wrote.
	copy dockerfileCopy
	// executable is the file the COPY lands, and generator is the name a
	// manifest asks for it by.
	executable string
	generator  string
	// mount is where the document's own `docker run` mounts the project and
	// points the working directory, read out of the console block beside the
	// Dockerfile rather than written here (#219).
	//
	// It is extracted for the reason the Dockerfile is: since the image declares
	// no working directory, the `-w` in that command is load bearing, and a copy
	// of it in this file would be the one that is checked while the one in the
	// document is the one people read.
	mount string
	// spec is the whole document, kept so that rules can break the console block
	// the way it breaks the Dockerfile. mount is extracted from the document
	// rather than from the Dockerfile, so its failure path is not reachable from
	// the dockerfile field alone.
	spec string
}

// newWorkedExample parses text and requires it to be the shape
// docs/container/SPEC.md describes. Every requirement is checked and every
// failure reported, because an example edited in one place is usually wrong in
// two.
func newWorkedExample(text string) (*workedExample, error) {
	preamble, stages, err := parseDockerfile(text)
	if err != nil {
		return nil, fmt.Errorf("%s's worked example: %w", containerSpec, err)
	}
	if len(stages) != 2 {
		return nil, fmt.Errorf("%s's worked example has %d stages, want 2: a stage that builds the generator and a stage FROM the avroc image that copies it in", containerSpec, len(stages))
	}
	build, final := stages[0], stages[1]

	var errs []error
	if build.name == "" {
		errs = append(errs, errors.New("the build stage has no `AS <name>`, so the final stage's COPY --from has nothing to name"))
	}

	// The final stage is the whole of the extension mechanism, so its FROM is
	// required to be the published base rather than merely some image: an
	// example that had drifted onto a generator image, or onto a distribution
	// with a shell in it, would build and run perfectly and would be teaching
	// somebody the wrong thing.
	if repo, tag, ok := strings.Cut(final.from, ":"); !ok || repo != publishedBaseImage {
		errs = append(errs, fmt.Errorf("the final stage is FROM %q, want a tag of %q: the worked example is what an adopter builds on the published base", final.from, publishedBaseImage))
	} else if tag == "" {
		errs = append(errs, fmt.Errorf("the final stage is FROM %q, which names no tag", final.from))
	}

	var copies []dockerfileCopy
	for _, instruction := range final.instructions {
		switch instruction.keyword {
		case "COPY":
			c, err := parseCopy(instruction.args)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			copies = append(copies, c)
		case "RUN":
			// The one instruction the absence of a shell makes impossible, and
			// the reason the rule is stated as a rule in the document rather
			// than as a nuance.
			errs = append(errs, fmt.Errorf("the final stage runs `RUN %s`: there is no shell in the avroc image, so extension is COPY-only", instruction.args))
		case "ENTRYPOINT":
			errs = append(errs, fmt.Errorf("the final stage sets `ENTRYPOINT %s`: a derived image inherits avroc's entrypoint and must not replace it", instruction.args))
		default:
			errs = append(errs, fmt.Errorf("the final stage's `%s %s` is an instruction this check does not know how to replay against the base image the pipeline built; teach .dagger/worked_example.go what it means rather than leaving it unchecked", instruction.keyword, instruction.args))
		}
	}
	if len(copies) != 1 {
		errs = append(errs, fmt.Errorf("the final stage has %d COPY instructions, want exactly 1", len(copies)))
		return nil, errors.Join(errs...)
	}

	c := copies[0]
	if c.from != build.name {
		errs = append(errs, fmt.Errorf("the COPY is --from=%q, want the build stage %q", c.from, build.name))
	}
	if dir := path.Dir(c.dst); dir != pluginDir {
		errs = append(errs, fmt.Errorf("the COPY lands %s in %s, want the plugin directory %s: nothing else in the image is on PATH", c.dst, dir, pluginDir))
	}

	// The filename is the whole of discovery: avroc searches PATH for
	// avroc-gen-<name> when a manifest asks for <name>, so an example copying
	// its executable under any other name would build an image whose generator
	// avroc could not find.
	executable := path.Base(c.dst)
	generator, ok := strings.CutPrefix(executable, generatorPrefix)
	switch {
	case !ok:
		errs = append(errs, fmt.Errorf("the COPY lands %q, which is not named %s<name>: avroc finds a generator by that name and would not find this one", executable, generatorPrefix))
	case slices.Contains(builtinGenerators(), generator):
		// The point of the example is a generator this project does not ship.
		// One that had drifted onto a built-in would be checking that avroc can
		// run its own code, which every other stage already does.
		errs = append(errs, fmt.Errorf("the worked example adds %q, which is one of avroc's own generators (%v): the example exists to show a generator avroc has never heard of being added", generator, builtinGenerators()))
	}

	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("%s's worked example: %w", containerSpec, err)
	}
	return &workedExample{
		dockerfile: text,
		buildStage: strings.Join([]string{preamble, build.text}, "\n"),
		base:       final.from,
		copy:       c,
		executable: executable,
		generator:  generator,
	}, nil
}

// rules requires the checks newWorkedExample makes to actually reject the
// documents they are about.
//
// It is the same shape as TagScheme and it is here for the same reason: a check
// whose failure path has never run is a check nobody knows the state of, and
// this one's failure path is the entire point — "a change that breaks the worked
// example fails CI" is a claim about what happens to a *broken* document, and
// the committed document is by construction not one.
//
// Each case edits the committed example rather than writing a Dockerfile from
// scratch, so every case stays a document that differs from the real one in
// exactly one way. The edit is required to have changed something, which is what
// stops a case going vacuous the day the example renames its generator: an edit
// that matched nothing would otherwise re-check the committed document and pass.
func (e *workedExample) rules() error {
	builtin := pluginDir + "/" + generatorExecutable(builtinGenerators()[0])

	cases := []struct {
		broken string
		edit   func(string) string
	}{{
		// The rule the absence of a shell imposes, and the one an adopter
		// writing a Dockerfile from habit breaks first.
		broken: "a RUN in the stage built FROM the avroc image",
		edit:   func(s string) string { return s + "\nRUN chmod 0755 " + e.copy.dst },
	}, {
		broken: "an ENTRYPOINT replacing avroc's",
		edit:   func(s string) string { return s + "\nENTRYPOINT [\"" + e.copy.dst + "\"]" },
	}, {
		// Not forbidden by docs/container/SPEC.md — a derived image may set ENV
		// — but not replayable either, so it has to stop CI rather than quietly
		// apply to nothing.
		broken: "an instruction the check cannot replay against the built base",
		edit:   func(s string) string { return s + "\nENV AVROC_GREETING=hi" },
	}, {
		broken: "the plugin copied somewhere that is not on PATH",
		edit:   func(s string) string { return strings.Replace(s, e.copy.dst, "/opt/avroc/"+e.executable, 1) },
	}, {
		broken: "the plugin copied under a name avroc's discovery would not find",
		edit:   func(s string) string { return strings.Replace(s, e.copy.dst, pluginDir+"/"+e.generator, 1) },
	}, {
		broken: "the example drifting onto one of avroc's own generators",
		edit:   func(s string) string { return strings.Replace(s, e.copy.dst, builtin, 1) },
	}, {
		broken: "a final stage built FROM something that is not the avroc image",
		edit:   func(s string) string { return strings.Replace(s, "FROM "+e.base, "FROM debian:stable-slim", 1) },
	}, {
		broken: "no final stage at all",
		edit:   func(s string) string { return strings.SplitN(s, "FROM "+e.base, 2)[0] },
	}}

	var errs []error
	for _, c := range cases {
		edited := c.edit(e.dockerfile)
		if edited == e.dockerfile {
			errs = append(errs, fmt.Errorf("%s: the case changed nothing, so it is checking the committed example rather than a broken one", c.broken))
			continue
		}

		if _, err := newWorkedExample(edited); err == nil {
			errs = append(errs, fmt.Errorf("%s: accepted, want rejected", c.broken))
		}
	}

	// The COPY's owner and mode are judged by entry rather than by
	// newWorkedExample, because they are what the filesystem check needs and not
	// what the document forbids — so they are checked here directly, over the
	// three spellings an image with no passwd file cannot be checked against.
	owners := []struct {
		broken string
		copy   dockerfileCopy
	}{
		{"a COPY that states no mode", dockerfileCopy{chown: imageUser}},
		{"a COPY owned by a name rather than a number", dockerfileCopy{chown: "avroc:avroc", chmod: executableMode}},
		{"a COPY whose --chown names no group", dockerfileCopy{chown: "65532", chmod: executableMode}},
	}
	for _, c := range owners {
		if _, err := c.copy.entry(); err == nil {
			errs = append(errs, fmt.Errorf("%s: accepted, want rejected", c.broken))
		}
	}

	// The mount point comes out of the document's console block rather than out of
	// its Dockerfile (#219, workedExampleMount), so its failure path is not
	// reachable by editing e.dockerfile and needs cases of its own. Same rule as
	// above: the edit must change something, or the case is re-checking the
	// committed document.
	mounts := []struct {
		broken string
		edit   func(string) string
	}{{
		// The one this story creates. A `docker run` printed without `-w` is a
		// command that lands in / and cannot find the manifest, and it is the first
		// thing an adopter copies.
		broken: "the documented `docker run` losing its -w",
		edit:   func(s string) string { return strings.Replace(s, " -w "+e.mount, "", 1) },
	}, {
		// The other half of "correct as printed": a command whose -w and -v have
		// drifted apart runs, finds no manifest, and reads as the caller's mistake.
		broken: "the documented `docker run` pointing -w somewhere it did not mount",
		edit:   func(s string) string { return strings.Replace(s, "-w "+e.mount, "-w /elsewhere", 1) },
	}, {
		broken: "the console block the invocation is printed in being removed",
		edit: func(s string) string {
			return strings.Replace(s, "```console\n$ docker build", "```text\n$ docker build", 1)
		},
	}}
	for _, c := range mounts {
		// The edit is applied to the worked example's section alone. This document
		// prints the same `docker run` in several sections, so an unscoped Replace
		// edits an *earlier* one, leaves the extracted command untouched and reports
		// the committed value back — a case that passes while checking nothing. The
		// negative control caught exactly that, which is what it is for.
		edited, err := editWorkedExampleSection(e.spec, c.edit)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.broken, err))
			continue
		}
		if edited == e.spec {
			errs = append(errs, fmt.Errorf("%s: the case changed nothing, so it is checking the committed document rather than a broken one", c.broken))
			continue
		}
		if got, err := workedExampleMount(edited); err == nil {
			errs = append(errs, fmt.Errorf("%s: accepted (as %q), want rejected", c.broken, got))
		}
	}

	return errors.Join(errs...)
}

// image is the worked example's final stage, applied to base.
//
// The build stage is built with an empty context — the document says the
// example is runnable as written with one, and the heredocs are what make that
// true, so a build that needed a file from somewhere would fail here rather
// than in an adopter's checkout.
//
// It is built for the same platform as the image it is copied into, which is
// what an adopter's `docker build` does and the one thing this composition
// could get wrong that their Dockerfile cannot: a plugin built for the builder's
// architecture and copied into an image of another's would land a file that
// exists, is executable, and cannot run.
func (e *workedExample) image(base *dagger.Container, platform dagger.Platform) *dagger.Container {
	built := dag.Directory().
		WithNewFile("Dockerfile", e.buildStage).
		DockerBuild(dagger.DirectoryDockerBuildOpts{Platform: platform})

	opts := dagger.ContainerWithFileOpts{Owner: e.copy.chown}
	if e.copy.chmod != 0 {
		opts.Permissions = e.copy.chmod
	}
	return base.WithFile(e.copy.dst, built.File(e.copy.src), opts)
}

// contents is every path the built image is allowed to hold: the base image's,
// plus the one file the example's COPY landed, owned and moded as that COPY
// asked for.
//
// Reading the owner and the mode out of the document rather than assuming them
// is what makes this a check of the example rather than a check of this
// function's opinion — docs/container/SPEC.md permits a world-executable
// root-owned file as well as the chowned one the example writes, and both
// satisfy it.
func (e *workedExample) contents() (map[string]imageEntry, error) {
	entry, err := e.copy.entry()
	if err != nil {
		return nil, err
	}
	// The copied file is named as a plugin-directory executable so that the
	// directory itself gets a row: since #217 the base image carries no
	// /usr/local/bin, and this example's COPY is what creates it — which is
	// exactly the sentence docs/container/SPEC.md now makes about a derived image.
	// The entry is then overwritten with what the document actually asked for,
	// which is the whole point of reading it rather than assuming it.
	contents := imageContents([]string{path.Base(e.copy.dst)})
	contents[e.copy.dst] = entry
	return contents, nil
}

// checkWorkedExampleGenerates runs the built image the way the document says to
// run it, and requires generated output back.
//
// The project is the committed example's schema with a manifest naming the
// example's generator and no other — the "project whose manifest asks for the
// hello generator" the document tells the reader to build. It is assembled here
// rather than committed for generatorProbe's reason: what it exercises is
// discovery, and a committed variant would be one more manifest to keep in step.
//
// Twice, as two users, because the document gives two invocations: the default,
// and the `--user $(id -u):$(id -g)` a caller writing into a bind mount is told
// to use. What is checked is that files came out and who owns them, not which
// files — what the example's generator writes is the document's business, and a
// pipeline asserting its bytes would be a second copy of a Go program nobody
// would think to update.
// mount is the path the document's own `docker run` mounts at and points `-w`
// at, extracted from the console block rather than written here (#219,
// workedExampleMount). That is what makes the flag in that command checked as
// printed: a `-w` dropped from the document changes where this check mounts, and a
// `-w` removed from it altogether fails the extraction outright.
func (m *Avroc) checkWorkedExampleGenerates(ctx context.Context, image *dagger.Container, generator, mount string) []error {
	const outDir = "out"

	project, err := generatorProbe(m.Source.File("example/schema.avdl"), generator, outDir)
	if err != nil {
		return []error{err}
	}

	executable := generatorExecutable(generator)

	var errs []error
	for _, user := range []string{imageUser, "1234:1234"} {
		// WithWorkdir as well as mounting there: since #219 the image declares no
		// working directory, and the `-w` this is came out of the document.
		generated := image.
			WithUser(user).
			WithDirectory(mount, project, dagger.ContainerWithDirectoryOpts{Owner: user}).
			WithWorkdir(mount).
			WithExec([]string{"generate"}, dagger.ContainerWithExecOpts{UseEntrypoint: true}).
			Directory(mount + "/" + outDir)

		entries, err := generated.Entries(ctx)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("as user %s, generating with %s through the image's entrypoint: %w", user, executable, err))
			continue
		case len(entries) == 0:
			errs = append(errs, fmt.Errorf("as user %s, generating with %s through the image's entrypoint wrote nothing under %s", user, executable, outDir))
			continue
		}
		if err := m.checkOutputOwnership(ctx, generated, user); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// workedExampleDockerfile extracts the fenced Dockerfile from the worked
// example's section of the document.
//
// Both ends are located rather than assumed: a heading that has been renamed and
// a section whose Dockerfile has been removed are different failures, and each
// one says so, because a check that silently found nothing to build is a check
// that has stopped running.
func workedExampleDockerfile(spec string) (string, error) {
	const fence = "```"

	lines := strings.Split(spec, "\n")
	start := slices.Index(lines, workedExampleHeading)
	if start < 0 {
		return "", fmt.Errorf("%s has no %q section", containerSpec, workedExampleHeading)
	}

	for i := start + 1; i < len(lines); i++ {
		switch {
		case strings.HasPrefix(lines[i], "## "):
			return "", fmt.Errorf("%s's %q section contains no ```dockerfile block", containerSpec, workedExampleHeading)
		case strings.TrimSpace(lines[i]) == fence+"dockerfile":
			for j := i + 1; j < len(lines); j++ {
				if strings.TrimSpace(lines[j]) == fence {
					return strings.Join(lines[i+1:j], "\n"), nil
				}
			}
			return "", fmt.Errorf("%s's worked example has an unterminated ```dockerfile block", containerSpec)
		}
	}
	return "", fmt.Errorf("%s's %q section contains no ```dockerfile block", containerSpec, workedExampleHeading)
}

// workedExampleMount reads the mount point out of the `docker run` the worked
// example's section prints, taking it from the `-w` flag that command carries.
//
// It exists because the image declares no working directory since #219, which
// makes that flag part of the documented invocation rather than decoration: a
// reader who copies the command without it lands in / and gets an error about a
// manifest they can see. So the flag is *extracted* and used as this check's mount
// point, exactly as the Dockerfile is extracted and built — a `-w` hard-coded here
// would be the copy that is checked while the document's is the copy that is read,
// and the two would drift the moment somebody reflowed the command.
//
// Both ends are located rather than assumed and every failure names what was
// missing, for workedExampleDockerfile's reason: a section whose console block has
// been reworded and one whose `docker run` has genuinely lost its `-w` are
// different findings, and a check that quietly found nothing has stopped running.
func workedExampleMount(spec string) (string, error) {
	const fence = "```"

	lines := strings.Split(spec, "\n")
	start := slices.Index(lines, workedExampleHeading)
	if start < 0 {
		return "", fmt.Errorf("%s has no %q section", containerSpec, workedExampleHeading)
	}

	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			break
		}
		if strings.TrimSpace(lines[i]) != fence+"console" {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == fence {
				break
			}
			if !strings.Contains(lines[j], "docker run") {
				continue
			}
			// The command is wrapped across lines with a trailing backslash, so
			// the flag and its value are not reliably on the line `docker run` is
			// on. Join the continuations before looking.
			command := lines[j]
			for strings.HasSuffix(strings.TrimSpace(command), `\`) && j+1 < len(lines) {
				j++
				command = strings.TrimSuffix(strings.TrimSpace(command), `\`) + " " + lines[j]
			}

			return dockerRunMount(strings.TrimSpace(command))
		}
	}
	return "", fmt.Errorf("%s's %q section contains no ```console block running `docker run`", containerSpec, workedExampleHeading)
}

// editWorkedExampleSection applies edit to the worked example's section of the
// document and returns the whole document with that section replaced.
//
// It exists because this document prints `docker run` in several places, so an
// edit meant to break the worked example's invocation will silently break an
// earlier section's instead — leaving the command under test untouched and the
// case passing on a document it did not change in the way it claimed to.
func editWorkedExampleSection(spec string, edit func(string) string) (string, error) {
	head, section, found := strings.Cut(spec, workedExampleHeading)
	if !found {
		return "", fmt.Errorf("%s has no %q section", containerSpec, workedExampleHeading)
	}
	return head + workedExampleHeading + edit(section), nil
}

// dockerRunMount reads the project mount point out of one `docker run` command
// line, and requires the command to be internally consistent: the path `-w` names
// has to be the path `-v` mounts the project at.
//
// Both halves matter, and the second is the one worth having. Since #219 the image
// declares no working directory, so `-v` and `-w` are two statements of the same
// path that a person edits separately — the shape of mistake this catches is a
// mount point changed in one of them, which produces a command that runs, finds no
// manifest at the working directory, and fails in a way the document says is a
// caller's error rather than the document's.
//
// It returns the mount point, which is what the check then generates at, so a
// document that renamed its mount is followed rather than contradicted.
func dockerRunMount(command string) (string, error) {
	fields := strings.Fields(command)

	value := func(flag string) (string, bool) {
		for k, field := range fields {
			if field == flag && k+1 < len(fields) {
				return fields[k+1], true
			}
			// The joined form, `-w=/work`, which docker also accepts.
			if after, ok := strings.CutPrefix(field, flag+"="); ok {
				return after, true
			}
		}
		return "", false
	}

	workdir, ok := value("-w")
	if !ok {
		workdir, ok = value("--workdir")
	}
	if !ok {
		return "", fmt.Errorf("%s's worked example runs `%s`, which carries no -w: the image declares no working directory (#219), so that command lands in / and cannot find %s. Add the flag to the document rather than to this check", containerSpec, command, manifestFilename)
	}

	volume, ok := value("-v")
	if !ok {
		volume, ok = value("--volume")
	}
	if !ok {
		return "", fmt.Errorf("%s's worked example runs `%s`, which mounts nothing: there is no project for -w %s to point at", containerSpec, command, workdir)
	}

	// `-v <host>:<container>[:<options>]`; the container path is what -w has to
	// agree with. The host side may itself be quoted and contain no colon here.
	parts := strings.Split(strings.Trim(volume, `"'`), ":")
	if len(parts) < 2 {
		return "", fmt.Errorf("%s's worked example runs `%s`, whose -v %s names no container path", containerSpec, command, volume)
	}
	target := parts[1]

	if target != workdir {
		return "", fmt.Errorf("%s's worked example runs `%s`, which mounts the project at %s and sets the working directory to %s: the two have to be the same path, or avroc starts somewhere there is no %s", containerSpec, command, target, workdir, manifestFilename)
	}
	return workdir, nil
}

// dockerfileInstruction is one instruction, with its keyword upper-cased and
// its arguments as one logical line — continuations joined, heredoc bodies left
// where they are.
type dockerfileInstruction struct {
	keyword string
	args    string
}

// dockerfileStage is one FROM and everything under it, kept both as structure
// and as the verbatim text that produced it: the structure is what the checks
// read, and the text is what is handed back to buildkit.
type dockerfileStage struct {
	from         string
	name         string
	text         string
	instructions []dockerfileInstruction
}

// parseDockerfile splits a Dockerfile into the preamble before its first FROM —
// the syntax directive, which the heredocs need — and its stages.
//
// It is not a Dockerfile implementation and does not try to be. It knows the
// three things that would otherwise make it misread the committed example:
// comments and blank lines are not instructions, a line ending in a backslash
// continues onto the next, and a heredoc's body is data rather than
// instructions. Anything it cannot make sense of is an error, since the whole
// point is to fail on a document it no longer understands rather than to build
// something else.
func parseDockerfile(text string) (string, []dockerfileStage, error) {
	// Compiled here rather than kept as package state: it is this function's
	// alone, and matching the redirection's delimiter with or without quotes is
	// all it does.
	heredoc := regexp.MustCompile(`<<-?['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?`)

	lines := strings.Split(text, "\n")

	var (
		preamble []string
		stages   []dockerfileStage
		current  *dockerfileStage
		body     []string
	)
	flush := func() {
		if current == nil {
			return
		}
		current.text = strings.Join(body, "\n")
		stages = append(stages, *current)
	}

	for i := 0; i < len(lines); i++ {
		raw := []string{lines[i]}
		trimmed := strings.TrimSpace(lines[i])

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			if current == nil {
				preamble = append(preamble, lines[i])
			} else {
				body = append(body, lines[i])
			}
			continue
		}

		var logical strings.Builder
		for {
			logical.WriteString(" ")
			logical.WriteString(strings.TrimSuffix(trimmed, `\`))
			if !strings.HasSuffix(trimmed, `\`) {
				break
			}
			i++
			if i >= len(lines) {
				return "", nil, errors.New("a line continuation runs off the end of the file")
			}
			raw = append(raw, lines[i])
			trimmed = strings.TrimSpace(lines[i])
		}

		// A heredoc's body belongs to the instruction that opened it, so it is
		// collected as text and never read for keywords: the example's build
		// stage writes a Go program this way, and a parser that read it as
		// instructions would find whatever the program happened to say.
		for _, match := range heredoc.FindAllStringSubmatch(logical.String(), -1) {
			for {
				i++
				if i >= len(lines) {
					return "", nil, fmt.Errorf("the heredoc opened with %s is never terminated by %s", match[0], match[1])
				}
				raw = append(raw, lines[i])
				if strings.TrimSpace(lines[i]) == match[1] {
					break
				}
			}
		}

		keyword, args, _ := strings.Cut(strings.TrimSpace(logical.String()), " ")
		keyword = strings.ToUpper(keyword)
		args = strings.TrimSpace(args)

		if keyword == "FROM" {
			flush()
			stage, err := parseFrom(args)
			if err != nil {
				return "", nil, err
			}
			current, body = &stage, raw
			continue
		}
		if current == nil {
			return "", nil, fmt.Errorf("`%s %s` appears before any FROM", keyword, args)
		}
		current.instructions = append(current.instructions, dockerfileInstruction{keyword: keyword, args: args})
		body = append(body, raw...)
	}
	flush()

	if len(stages) == 0 {
		return "", nil, errors.New("there is no FROM in it")
	}
	return strings.Join(preamble, "\n"), stages, nil
}

// parseFrom reads `FROM <image> [AS <name>]`.
func parseFrom(args string) (dockerfileStage, error) {
	fields := strings.Fields(args)
	for len(fields) > 0 && strings.HasPrefix(fields[0], "--") {
		fields = fields[1:]
	}

	switch {
	case len(fields) == 1:
		return dockerfileStage{from: fields[0]}, nil
	case len(fields) == 3 && strings.EqualFold(fields[1], "AS"):
		return dockerfileStage{from: fields[0], name: fields[2]}, nil
	default:
		return dockerfileStage{}, fmt.Errorf("`FROM %s` is not `FROM <image>` or `FROM <image> AS <name>`", args)
	}
}

// dockerfileCopy is one COPY instruction, read for everything that decides
// where the file lands and who may run it.
type dockerfileCopy struct {
	from  string
	chown string
	chmod int
	src   string
	dst   string
}

// parseCopy reads `COPY [--from=<stage>] [--chown=<u>:<g>] [--chmod=<mode>] <src> <dst>`.
//
// A flag it does not know is an error rather than something skipped: --link,
// --exclude and the rest all change what the instruction does, and replaying a
// COPY while ignoring half of it would check an image the document does not
// describe.
func parseCopy(args string) (dockerfileCopy, error) {
	var c dockerfileCopy

	fields := strings.Fields(args)
	for len(fields) > 0 && strings.HasPrefix(fields[0], "--") {
		flag, value, ok := strings.Cut(fields[0], "=")
		if !ok {
			return c, fmt.Errorf("`COPY %s`: the flag %s carries no value", args, fields[0])
		}
		switch flag {
		case "--from":
			c.from = value
		case "--chown":
			c.chown = value
		case "--chmod":
			mode, err := strconv.ParseInt(value, 8, 32)
			if err != nil {
				return c, fmt.Errorf("`COPY %s`: --chmod=%s is not an octal mode: %w", args, value, err)
			}
			c.chmod = int(mode)
		default:
			return c, fmt.Errorf("`COPY %s`: %s is a flag .dagger/worked_example.go does not know how to replay", args, flag)
		}
		fields = fields[1:]
	}

	if len(fields) != 2 {
		return c, fmt.Errorf("`COPY %s` has %d operands, want a source and a destination", args, len(fields))
	}
	c.src, c.dst = fields[0], fields[1]
	return c, nil
}

// entry is the owner and mode the copied file lands with, as the filesystem
// check expects to find it.
//
// The two are not symmetric, because only one of them can be inferred. A COPY
// with no --chown lands the file root-owned, which is a fact about COPY and is
// what this returns — docs/container/SPEC.md permits a world-executable
// root-owned plugin as well as the chowned one the example writes, so an
// example that dropped the flag is still an example this check can judge. A
// COPY with no --chmod lands the *source's* mode, which is a property of the
// build stage rather than of anything the document states, so there is nothing
// to infer: it is an error, since a check that guessed would pass on an image
// whose plugin is not executable.
//
// A --chown that is stated has to be numeric, for the image's own reason: there
// is no passwd file in it for a name to resolve against.
func (c dockerfileCopy) entry() (imageEntry, error) {
	entry := imageEntry{mode: c.chmod}
	if c.chmod == 0 {
		return entry, fmt.Errorf("the worked example's `COPY %s %s` states no --chmod, so what the file lands as is a property of the build stage rather than of the document", c.src, c.dst)
	}
	if c.chown == "" {
		return entry, nil
	}

	uid, gid, ok := strings.Cut(c.chown, ":")
	if !ok {
		return entry, fmt.Errorf("the worked example's --chown=%s names no group: the image has no /etc/passwd, so both halves are numbers and both have to be there", c.chown)
	}
	var err error
	if entry.uid, err = strconv.Atoi(uid); err != nil {
		return entry, fmt.Errorf("the worked example's --chown=%s does not name a numeric user: the image has no /etc/passwd for a name to resolve against", c.chown)
	}
	if entry.gid, err = strconv.Atoi(gid); err != nil {
		return entry, fmt.Errorf("the worked example's --chown=%s does not name a numeric group: the image has no /etc/group for a name to resolve against", c.chown)
	}
	return entry, nil
}
