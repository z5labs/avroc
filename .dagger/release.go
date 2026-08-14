// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// This file publishes a release: it decides from the refs at HEAD whether there
// is one and hands the archetype the version and the repositories to publish it
// under (#128, #217).
//
// # Why the decision is here and not an `if:` on a job
//
// A workflow that decides has to write the tag scheme down to do it — a `for tag
// in "$VERSION" "${VERSION%.*}" ...` line, or a job-level `if:` naming the ref
// shape that counts as a release. That is a second place the scheme lives,
// beside docs/container/SPEC.md, in a file that runs once per release and is
// exercised nowhere else. The two drift in the direction nobody notices: a
// prerelease moves `latest`, or a patch release forgets to move `v0`, and both
// are discovered by somebody whose `FROM` line resolved to the wrong image.
//
// So the module reads the refs at HEAD, and everything downstream of that
// reading is a function of them. TagScheme runs the same function over a table
// of cases on every pull request, which is what makes the scheme a thing this
// repository checks rather than a thing it intends. The workflow's remaining job
// is to say *where* — the registry, the repository and the credentials — which is
// the half that genuinely is a property of the deployment and not of the release.
//
// # What moved to the archetype, and what did not
//
// The tag *family* moved (#217). Which tags a version implies — `v0.2.0` also
// coming to name `v0.2`, `v0` and `latest`, a prerelease moving none of them — is
// the archetype's now, derived from the version a caller states, checked by its own
// table, and published as one manifest list pushed once with every tag pointing at
// the one digest. So did the signature, the provenance and the SBOMs: every
// published digest carries a recursive cosign signature, a signed SLSA provenance
// statement whose build identity comes from the exchanged OIDC token rather than
// from anything this module could have asserted, and an SPDX and CycloneDX
// document per platform describing the whole image. `cosignPackage`,
// `provenanceStatement`, `attest`, `publishTags` and `versionTags` are gone with
// them.
//
// What did not move is the sentence "this commit is a release". The archetype
// takes a version from its caller and has no opinion about where one comes from,
// and *this repository* releases by tagging a commit: a single canonical version
// tag at HEAD is a release, no tag or a tag that is not a canonical version
// publishes nothing, two version tags is an error because which of them `latest`
// should follow has no defensible answer, and a version carrying `+build`
// metadata is refused because `+` cannot be spelled in an OCI tag and mangling it
// away would publish two releases under one name. planRelease is all four, and
// TagScheme is what checks them.
//
// # Why the contract check is a gate inside Release
//
// docs/container/SPEC.md's guarantees are checked by ImageContract and
// GeneratorImageContract, and the release workflow used to call both as steps of
// their own before calling this. That worked while this module built the image,
// because both calls came through one `image` function; it stops meaning what it
// said once the container is the archetype's, because the guarantee that the
// container a check read is *the very container that will be pushed* holds only
// within one chained call. Two separate `dagger call` invocations are a second,
// cache-identical build that merely agrees with the first.
//
// A published version tag is never repointed here, so "the image was checked, and
// then an identical one was pushed" is not a property worth resting a release on.
// The check is therefore a gate inside this function, over the Apps this function
// is about to publish: releaseContract runs it, and a failure means nothing was
// pushed. The standalone `dagger call image-contract` and
// `dagger call generator-image-contract` stay, because a pull request has no
// release to gate and wants the check on its own.
package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"dagger/avroc/internal/dagger"
)

// Release publishes this repository's images for the release at HEAD (#128,
// #217).
//
// Whether there is a release at all is decided here, from the refs at HEAD: a
// single version tag pointing at HEAD is a release, and anything else — a branch
// alone, no tag, two version tags — is not. A run with no release publishes
// nothing and succeeds, because "this commit is not a release" is an answer
// rather than a fault; a run with two version tags fails, because which of them
// `latest` should follow is not a question this function is entitled to guess.
//
// registry is the registry alone — `ghcr.io` — and repository is the base image's
// path within it — `z5labs/avroc`. They are two arguments because the archetype
// separates them, and it separates them so that a mirror or an internal registry
// serves the same release by changing one of the two and nothing else. The
// generator images derive from repository by the rule docs/container/SPEC.md names
// them under, so the caller supplies one location rather than four.
//
// Every published image is signed and carries provenance and an SBOM, and none of
// that is optional: a publish the archetype cannot produce provenance for fails
// rather than publishing without it. That is why the OIDC arguments are required
// here and why there is no `dagger call publish` beside this one — the only caller
// with a token to exchange is the workflow.
//
// The git metadata is New's, not an argument here. It used to be one, because the
// release was the only thing in this module that read git; since #217 every image
// build reads it too, so New binds it once and this reads m.GitDir. Two independent
// inputs for one fact is what that avoids, and the way it would have gone wrong is
// specific: `dagger call --git-dir=A release --git-dir=B` was accepted, and would
// have taken the release *decision* from one tree while the archetype stamped and
// annotated the images from the other.
//
// It returns a report naming every reference published, grouped by repository, each
// pinned to the digest it resolved to.
//
// +cache="never"
func (m *Avroc) Release(
	ctx context.Context,
	// The registry to publish to, without a repository path — `ghcr.io`.
	registry string,
	// The base image's repository within that registry, without a tag —
	// `z5labs/avroc`.
	repository string,
	// The registry username to authenticate as.
	username string,
	// The registry password or token to authenticate with.
	password *dagger.Secret,
	// The CI provider's OIDC token request endpoint —
	// `ACTIONS_ID_TOKEN_REQUEST_URL` on GitHub Actions. The publish exchanges a
	// token from it for the identity that signs, so a run that publishes needs it.
	idTokenRequestUrl string,
	// The bearer token for that endpoint — `ACTIONS_ID_TOKEN_REQUEST_TOKEN` on
	// GitHub Actions. A secret, because it mints identity tokens.
	idTokenRequestToken *dagger.Secret,
) (string, error) {
	// The refs are read before the arguments are checked, because "this commit is
	// not a release" is the common answer and reaching it should not depend on a
	// credential nobody is going to use.
	refs, err := headRefs(ctx, m.Source, m.GitDir)
	if err != nil {
		return "", err
	}
	version, err := planRelease(refs)
	if err != nil {
		return "", err
	}
	if version == "" {
		return fmt.Sprintf("no version tag points at HEAD (refs: %s); nothing published", strings.Join(refs, ", ")), nil
	}

	// Every credential the run needs is checked before the first byte moves. A
	// publish that reached the registry and then found it had no way to sign
	// would leave a tag pointing at an unattested image, and a published tag is
	// never repointed.
	switch {
	case registry == "":
		return "", errors.New("registry is required: it is the registry alone, such as `ghcr.io`, and never a repository path")
	case repository == "":
		return "", errors.New("repository is required: it is the base image's path within the registry, without a tag")
	case username == "" || password == nil:
		return "", errors.New("username and password are both required: a release is pushed to a registry that authenticates")
	case idTokenRequestUrl == "" || idTokenRequestToken == nil:
		return "", errors.New("idTokenRequestUrl and idTokenRequestToken are both required: every published image is signed, and signing exchanges a workload identity token")
	}

	images := m.releaseImages(repository, version)

	// The gate. Nothing is pushed until every published image has been checked
	// against docs/container/SPEC.md, and it is checked on the containers these
	// very Apps carry — see this file's comment for why that is not the same as
	// having run `dagger call image-contract` a moment earlier.
	if err := m.releaseContract(ctx, images); err != nil {
		return "", fmt.Errorf("refusing to publish %s: %w", version, err)
	}

	var report strings.Builder
	for _, image := range images {
		published, err := image.app.
			WithRegistry(registry, username, password).
			WithOidc(idTokenRequestUrl, idTokenRequestToken).
			Publish(ctx, []string{image.repository})
		if err != nil {
			return report.String(), fmt.Errorf("publishing %s/%s: %w", registry, image.repository, err)
		}

		// Grouped by repository, one reference per line beneath it. Publish returns
		// its references repository-major in tag-family order and this passes one
		// repository per call, so the grouping is this loop's rather than an
		// assumption about that ordering — which matters, because the archetype's
		// own documentation calls the grouping a thing it may yet give a structured
		// return type. A flat list of `<ref>@<digest>` lines is what a person reads
		// this for, and the repository heading is what makes four of them legible.
		fmt.Fprintf(&report, "%s/%s\n", registry, image.repository)
		for _, ref := range published {
			fmt.Fprintf(&report, "  %s\n", ref)
		}
	}
	return report.String(), nil
}

// TagScheme is the half of docs/container/SPEC.md's tag table this repository
// still decides, executed rather than read.
//
// What it covers is what planRelease answers: whether the refs at HEAD are a
// release at all, and which version it is. The tag *family* that version implies
// is the archetype's since #217, and is checked by its own table over its own
// literals — restating it here would be a second copy of somebody else's rule,
// which is the thing this whole file exists to avoid having.
//
// This is the part of a release that cannot be checked by releasing: a commit
// that published when it should not have, or published under a version nobody
// tagged, is discovered afterwards, and by then the tag is out and this project's
// own contract says it is never repointed.
//
// Every expected value below is a literal, never one of this file's own
// constants, so the check cannot move with the code it checks.
//
// +check
// +cache="session"
func (m *Avroc) TagScheme() error {
	cases := []struct {
		refs    []string
		version string
		fails   bool
	}{
		// A release: one canonical version tag at HEAD, whatever else is there.
		{refs: []string{"refs/tags/v0.2.0", "refs/heads/main"}, version: "v0.2.0"},
		{refs: []string{"refs/tags/v1.10.3"}, version: "v1.10.3"},
		// A prerelease is a release, and it is the archetype that then declines to
		// move `v0`, `v0.2` and `latest` onto it.
		{refs: []string{"refs/tags/v0.3.0-rc.1"}, version: "v0.3.0-rc.1"},
		// Not a release. A branch, a tag that is not a version, and a tag that
		// looks like one but is not canonical all publish nothing rather than
		// publishing something surprising.
		{refs: []string{"refs/heads/main"}},
		{refs: []string{}},
		{refs: []string{"refs/tags/nightly", "refs/heads/main"}},
		{refs: []string{"refs/tags/0.2.0"}},
		{refs: []string{"refs/tags/v0.2"}},
		{refs: []string{"refs/tags/v01.2.0"}},
		// Build metadata cannot be spelled in an OCI tag, so a version carrying
		// it is refused rather than silently mangled into one that can. The
		// archetype refuses it too, and this repository refuses it first: the
		// version never reaches a publish, so the refusal is the same whether or
		// not a registry was ever going to be contacted.
		{refs: []string{"refs/tags/v0.2.0+build.5"}, fails: true},
		// Two version tags at HEAD: which of them `latest` should follow is not
		// a question with a defensible answer, so it is an error and not a
		// choice.
		{refs: []string{"refs/tags/v0.2.0", "refs/tags/v0.3.0"}, fails: true},
	}

	var errs []error
	for _, c := range cases {
		version, err := planRelease(c.refs)
		switch {
		case c.fails && err == nil:
			errs = append(errs, fmt.Errorf("%v: planned %q, want an error", c.refs, version))
		case c.fails:
		case err != nil:
			errs = append(errs, fmt.Errorf("%v: %w", c.refs, err))
		case version != c.version:
			errs = append(errs, fmt.Errorf("%v: version is %q, want %q", c.refs, version, c.version))
		}
	}
	return errors.Join(errs...)
}

// planRelease reads the refs at HEAD and returns the version being released, or
// the empty string when this commit is not a release.
//
// A ref counts only if it is a tag whose name is a canonical version. Everything
// else — branches, remote refs, `refs/stash`, a tag named `nightly` — is ignored
// rather than rejected, because a release commit routinely carries several refs
// and none of the others is evidence of a mistake.
func planRelease(refs []string) (string, error) {
	var versions []string
	for _, ref := range refs {
		name, ok := strings.CutPrefix(strings.TrimSpace(ref), "refs/tags/")
		if !ok {
			continue
		}
		if _, ok := parseVersion(name); ok {
			versions = append(versions, name)
		}
	}

	switch len(versions) {
	case 0:
		return "", nil
	case 1:
	default:
		return "", fmt.Errorf("HEAD carries more than one version tag (%s): which release this is, and which of them the moving tags should follow, is not a question this pipeline can answer", strings.Join(versions, ", "))
	}

	// Parsed a second time rather than carried out of the loop, and the `ok` is
	// checked rather than discarded. It cannot be false today — the loop above only
	// kept refs parseVersion already accepted — and that is exactly why it is worth
	// a branch: `versionTags` used to be the thing that refused an unparseable tag,
	// and without this the day the filter above admits something looser is the day a
	// zero `version` passes the build-metadata check and an unrecognised tag reaches
	// App.Publish as the release's version.
	v, ok := parseVersion(versions[0])
	if !ok {
		return "", fmt.Errorf("%q reached the release plan without being a canonical version tag, which is a bug in this function", versions[0])
	}

	// An OCI tag is [A-Za-z0-9_.-], which has no `+` in it. Refusing is the only
	// honest answer: mangling the metadata away would publish `v0.2.0` and
	// `v0.2.0+build.5` as the same tag, and this project's contract says a
	// published version tag is never repointed. The archetype refuses it as well;
	// refusing it here is what keeps the answer the same for a commit nobody was
	// ever going to publish.
	if v.build != "" {
		return "", fmt.Errorf("version tag %q carries build metadata, which cannot be spelled in an OCI tag: release it under a version without one", versions[0])
	}
	return versions[0], nil
}

// version is a parsed canonical version tag.
type version struct {
	major      string
	minor      string
	patch      string
	prerelease string
	build      string
}

// parseVersion reads a canonical `vMAJOR.MINOR.PATCH` tag, with the optional
// prerelease and build parts semantic versioning allows.
//
// Canonical is meant strictly: `0.2.0` without the `v`, `v0.2` without the
// patch, and `v01.2.0` with a leading zero are all not versions, and a commit
// tagged with one of them publishes nothing. That is the safe direction — a tag
// this function does not recognise costs a release nobody meant to make, where
// one it recognised too eagerly would move `latest` onto something that was
// never a release.
func parseVersion(tag string) (version, bool) {
	// Compiled per call rather than kept as package state: this runs a handful
	// of times per release and once per case in TagScheme, and a package-level
	// variable is a piece of shared state to reason about for no measurable
	// gain.
	re := regexp.MustCompile(`^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z.-]+))?(?:\+([0-9A-Za-z.-]+))?$`)
	fields := re.FindStringSubmatch(tag)
	if fields == nil {
		return version{}, false
	}
	return version{
		major:      fields[1],
		minor:      fields[2],
		patch:      fields[3],
		prerelease: fields[4],
		build:      fields[5],
	}, true
}

// releaseImage is one image a release publishes: the repository it goes to and
// the application that is pushed there.
type releaseImage struct {
	repository string
	app        *dagger.Z5LabsApp
	// generator is the built-in this image carries, or the empty string for the
	// base. It is what tells releaseContract which listing to hold the image to,
	// and it is a name rather than a listing so that the check reads the same
	// table GeneratorImageContract reads.
	generator string
}

// releaseImages is every image a release publishes, derived from the base image's
// repository.
//
// The generator repositories are `<base>-gen-<name>`, which is the naming
// docs/container/SPEC.md uses throughout — `avroc-gen-go` beside `avroc`. The set
// of generators comes from builtinGenerators, so a fourth one is published by
// being added there and nowhere else.
//
// Every entry is an App built at the release's version, and every one of them is
// built exactly as the check stages build theirs — the version is the only
// difference, which is what makes `dagger call image-contract` on a pull request a
// statement about the image that release will push.
//
// The bundle is deliberately absent, as it always was: it is a convenience for
// checking that three generators compose, and docs/container/SPEC.md publishes one
// image per generator.
func (m *Avroc) releaseImages(repository, version string) []releaseImage {
	images := []releaseImage{{
		repository: repository,
		app:        m.baseApp(version),
	}}
	for _, name := range builtinGenerators() {
		images = append(images, releaseImage{
			repository: repository + "-gen-" + name,
			app:        m.generatorImageApp(name, version),
			generator:  name,
		})
	}
	return images
}

// releaseContract holds every image a release is about to publish to
// docs/container/SPEC.md, on every platform, before anything is pushed.
//
// It runs `checkBaseImage` and `checkGeneratorImage` — the *same* functions
// ImageContract and GeneratorImageContract run, not a list assembled here — over
// the containers these Apps carry rather than over a second build of them. That is
// the whole point, and it is a mechanism rather than an intention: a check added to
// either of those two is a check this gate acquires, where a hand-copied sequence
// would have let one be added and the release stop short of it. It was a
// hand-copied sequence in the first draft of #217, and a reviewer caught it.
//
// Every image and every platform is checked and every failure collected: a release
// that is wrong is wrong on both architectures, and one run should say so once for
// each.
func (m *Avroc) releaseContract(ctx context.Context, images []releaseImage) error {
	var errs []error
	for _, image := range images {
		for _, platform := range imagePlatforms() {
			container := image.app.Container(platform)

			var found []error
			if image.generator == "" {
				found = m.checkBaseImage(ctx, container)
			} else {
				found = m.checkGeneratorImage(ctx, container, image.generator)
			}
			for _, err := range found {
				errs = append(errs, fmt.Errorf("%s on %s: %w", image.repository, platform, err))
			}
		}
	}
	return errors.Join(errs...)
}

// headRefs is every ref pointing at HEAD, as git reports them.
func headRefs(ctx context.Context, source, gitDir *dagger.Directory) ([]string, error) {
	out, err := gitContainer(source, gitDir).
		WithExec([]string{"git", "for-each-ref", "--points-at", "HEAD", "--format=%(refname)"}).
		Stdout(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the refs at HEAD: %w", err)
	}

	var refs []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if ref := strings.TrimSpace(line); ref != "" {
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

// gitContainer is a container holding the source with its git metadata grafted
// back on.
//
// New drops `.git` from the source it binds, because none of the check stages
// reads git metadata and leaving it in would make every commit a cache miss for
// all of them. A release does read it, so it arrives here as its own argument
// and is mounted rather than folded into the source — which keeps the release's
// need from costing every other stage its cache.
//
// It is the only place in this file that runs git. The commit, its committer time
// and the origin remote used to be read here too, for the provenance predicate
// this module rendered; the archetype reads them itself now, out of appSource, and
// its provenance takes every identifying field from the exchanged OIDC token's
// claims rather than from anything a caller could have stated.
func gitContainer(source, gitDir *dagger.Directory) *dagger.Container {
	return dag.Go().Container(source).WithMountedDirectory("/src/.git", gitDir)
}
