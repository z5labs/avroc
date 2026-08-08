// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// This file publishes a release: it decides from the refs at HEAD whether there
// is one, works out which tags it carries, pushes every image under all of them,
// and signs each published digest with provenance and an SBOM beside it (#128).
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
// is to say *where* — the registry repository and the credentials — which is the
// half that genuinely is a property of the deployment and not of the release.
//
// # Why cosign, and why built rather than pulled
//
// docs/container/SPEC.md's "Tags and what pinning one buys" promises a consumer
// can verify a signature before trusting a tag, and a promise like that is only
// worth what the verifying command is worth. cosign's keyless flow is the one a
// consumer already has: `cosign verify` with the certificate identity and issuer
// is a command somebody can run without this project having published a key,
// rotated it, or asked anybody to trust a key distribution channel. The signing
// identity is the workflow itself, certified by the public sigstore CA from the
// OIDC token GitHub mints for the run, and it is recorded in a public
// transparency log — so verification checks who built the image rather than who
// holds a secret.
//
// It is built by `go install` at a pinned version rather than pulled as a tool
// image, which is what every other container in this module does: the toolchain
// is the one dag.Go() already provides, the pin is a module version rather than
// a tag somebody can repoint, and there is no image reference here to keep in
// step with an upstream release.
//
// # What is attached, and to what
//
// Everything is attached to the *digest* the publish returned, never to a tag.
// A tag is a name that moves; an attestation about a name that moves says
// nothing. Three kinds, for each of the four published images:
//
//   - A signature over the manifest, which is what `cosign verify` checks.
//   - A SLSA v1 provenance statement naming the source commit, the ref that
//     triggered the release and the workflow that ran it.
//   - An SPDX SBOM per executable per platform, produced by the shared `go`
//     module from the very binaries this pipeline compiled. One per platform
//     rather than one per image, because each document is tied to a specific
//     binary's SHA-256 and a single document would have to name one of them and
//     be wrong about the rest.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"dagger/avroc/internal/dagger"
)

const (
	// cosignPackage is the signing tool, pinned to a module version. `go
	// install` refuses an unpinned package, which is the property that makes
	// the binary this release signs with the binary the pin names.
	//
	// renovate: datasource=go depName=github.com/sigstore/cosign/v2
	cosignPackage = "github.com/sigstore/cosign/v2/cmd/cosign@v2.6.5"

	// rollingTag is docs/container/SPEC.md's rolling tag, the one that moves on
	// every release.
	rollingTag = "latest"

	// buildType names this pipeline in the provenance predicate. It is a URI
	// because SLSA requires one, and it points at the file that implements it
	// rather than at a specification somebody would have to be told about
	// separately: what "built by avroc's release" means is what is written here.
	buildType = "https://github.com/z5labs/avroc/blob/main/.dagger/release.go"

	// provenancePredicate and sbomPredicate are cosign's names for the two
	// attestation shapes. slsaprovenance1 is SLSA v1, which is the version whose
	// predicate provenanceStatement renders; spdxjson is the SPDX 2.3 JSON the
	// `go` module emits.
	provenancePredicate = "slsaprovenance1"
	sbomPredicate       = "spdxjson"
)

// Release publishes this repository's images for the release at HEAD, signs each
// one, and attaches provenance and an SBOM (#128).
//
// Whether there is a release at all is decided here, from the refs at HEAD: a
// single version tag pointing at HEAD is a release, and anything else — a branch
// alone, no tag, two version tags — is not. A run with no release publishes
// nothing and succeeds, because "this commit is not a release" is an answer
// rather than a fault; a run with two version tags fails, because which of them
// `latest` should follow is not a question this function is entitled to guess.
//
// repository is the base image's full repository, without a tag —
// `ghcr.io/z5labs/avroc`. The generator images derive from it by the same rule
// docs/container/SPEC.md names them under, so the caller supplies one location
// rather than four. Where to publish is the caller's, because a mirror or an
// internal registry serves the same release; which tags exist is not, for the
// reason this file's comment gives.
//
// It returns a report naming every repository published, the digest it resolved
// to and the tags now pointing at it.
//
// +cache="never"
func (m *Avroc) Release(
	ctx context.Context,
	// The repository's git metadata. The refs at HEAD are what decides whether
	// this commit is a release, so a checkout without tags publishes nothing.
	// +defaultPath="/.git"
	gitDir *dagger.Directory,
	// The base image's repository, without a tag — `ghcr.io/z5labs/avroc`.
	repository string,
	// The registry username to authenticate as.
	username string,
	// The registry password or token to authenticate with.
	password *dagger.Secret,
	// The CI provider's OIDC token request endpoint —
	// `ACTIONS_ID_TOKEN_REQUEST_URL` on GitHub Actions. cosign exchanges a token
	// from it for the short-lived certificate that signs, so a run that
	// publishes needs it.
	idTokenRequestUrl string,
	// The bearer token for that endpoint — `ACTIONS_ID_TOKEN_REQUEST_TOKEN` on
	// GitHub Actions. A secret, because it mints identity tokens.
	idTokenRequestToken *dagger.Secret,
	// What ran this release, for the provenance predicate's builder — the
	// workflow reference on GitHub Actions. The module cannot know what invoked
	// it, and provenance that guessed would be provenance about nothing.
	builder string,
	// The run this release came from, for the provenance predicate — a URL to
	// the workflow run on GitHub Actions.
	// +optional
	invocation string,
) (string, error) {
	refs, err := headRefs(ctx, m.Source, gitDir)
	if err != nil {
		return "", err
	}
	plan, err := planRelease(refs)
	if err != nil {
		return "", err
	}
	if plan.version == "" {
		return fmt.Sprintf("no version tag points at HEAD (refs: %s); nothing published", strings.Join(refs, ", ")), nil
	}

	// Every credential the run needs is checked before the first byte moves. A
	// publish that reached the registry and then found it had no way to sign
	// would leave a tag pointing at an unattested image, and a published tag is
	// never repointed.
	switch {
	case repository == "":
		return "", errors.New("repository is required: it is the base image's full repository, without a tag")
	case username == "" || password == nil:
		return "", errors.New("username and password are both required: a release is pushed to a registry that authenticates")
	case idTokenRequestUrl == "" || idTokenRequestToken == nil:
		return "", errors.New("idTokenRequestUrl and idTokenRequestToken are both required: every published image is signed, and signing exchanges a workload identity token")
	case builder == "":
		return "", errors.New("builder is required: the provenance predicate names what ran the release, and this module cannot know that")
	}

	commit, err := headCommit(ctx, m.Source, gitDir)
	if err != nil {
		return "", err
	}
	source, err := sourceURI(ctx, m.Source, gitDir)
	if err != nil {
		return "", err
	}

	startedOn := time.Now().UTC()
	signer := m.cosign(idTokenRequestUrl, idTokenRequestToken, registryHost(repository), username, password, startedOn)

	var report strings.Builder
	for _, image := range m.releaseImages(repository, username, password) {
		digest, err := publishTags(ctx, image, plan.tags)
		if err != nil {
			return report.String(), err
		}

		predicate, err := provenanceStatement(provenanceFacts{
			Repository: image.repository,
			Version:    plan.version,
			Tags:       plan.tags,
			Source:     source,
			Commit:     commit,
			Builder:    builder,
			Invocation: invocation,
			StartedOn:  startedOn,
		})
		if err != nil {
			return report.String(), err
		}
		if err := m.attest(ctx, signer, image, digest, predicate); err != nil {
			return report.String(), err
		}

		fmt.Fprintf(&report, "%s\n  digest: %s\n  tags:   %s\n", image.repository, digest, strings.Join(plan.tags, ", "))
	}
	return report.String(), nil
}

// TagScheme is docs/container/SPEC.md's tag table executed rather than read.
//
// The table is the part of a release that cannot be checked by releasing: a tag
// that moved when it should not have is discovered by a consumer whose `FROM`
// line resolved to something they did not ask for, and by then the tag has been
// published and this project's own contract says it is never repointed. So the
// derivation is a pure function of the version, and this runs it over the cases
// that matter — including the ones with no release in them, since "publishes
// nothing" is as much a promise as "publishes four tags".
//
// Every expected tag below is a literal, never one of this file's own constants.
// A table written in terms of rollingTag would move with it and pass on a
// release that had quietly started publishing something else under a different
// name, which is the one failure this check exists to catch.
//
// +check
// +cache="session"
func (m *Avroc) TagScheme() error {
	cases := []struct {
		refs    []string
		version string
		tags    []string
		fails   bool
	}{
		// A release: the full version tag, plus the three that move.
		{
			refs:    []string{"refs/tags/v0.2.0", "refs/heads/main"},
			version: "v0.2.0",
			tags:    []string{"v0.2.0", "v0.2", "v0", "latest"},
		},
		{
			refs:    []string{"refs/tags/v1.10.3"},
			version: "v1.10.3",
			tags:    []string{"v1.10.3", "v1.10", "v1", "latest"},
		},
		// A prerelease publishes itself and moves nothing: `v0`, `v0.2` and
		// `latest` are what a derived Dockerfile pins to get fixes, and a
		// release candidate is not a fix anybody asked to be given.
		{
			refs:    []string{"refs/tags/v0.3.0-rc.1"},
			version: "v0.3.0-rc.1",
			tags:    []string{"v0.3.0-rc.1"},
		},
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
		// it is refused rather than silently mangled into one that can.
		{refs: []string{"refs/tags/v0.2.0+build.5"}, fails: true},
		// Two version tags at HEAD: which of them `latest` should follow is not
		// a question with a defensible answer, so it is an error and not a
		// choice.
		{refs: []string{"refs/tags/v0.2.0", "refs/tags/v0.3.0"}, fails: true},
	}

	var errs []error
	for _, c := range cases {
		plan, err := planRelease(c.refs)
		switch {
		case c.fails && err == nil:
			errs = append(errs, fmt.Errorf("%v: planned %v, want an error", c.refs, plan.tags))
		case c.fails:
		case err != nil:
			errs = append(errs, fmt.Errorf("%v: %w", c.refs, err))
		case plan.version != c.version:
			errs = append(errs, fmt.Errorf("%v: version is %q, want %q", c.refs, plan.version, c.version))
		case !slices.Equal(plan.tags, c.tags):
			errs = append(errs, fmt.Errorf("%v: tags are %v, want %v", c.refs, plan.tags, c.tags))
		}
	}
	return errors.Join(errs...)
}

// releasePlan is what the refs at HEAD decided: the version being released, and
// every tag that ends up pointing at it. A zero value is "this commit is not a
// release".
type releasePlan struct {
	version string
	tags    []string
}

// planRelease reads the refs at HEAD and returns what to publish.
//
// A ref counts only if it is a tag whose name is a canonical version. Everything
// else — branches, remote refs, `refs/stash`, a tag named `nightly` — is ignored
// rather than rejected, because a release commit routinely carries several refs
// and none of the others is evidence of a mistake.
func planRelease(refs []string) (releasePlan, error) {
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
		return releasePlan{}, nil
	case 1:
	default:
		return releasePlan{}, fmt.Errorf("HEAD carries more than one version tag (%s): which release this is, and which of them the moving tags should follow, is not a question this pipeline can answer", strings.Join(versions, ", "))
	}

	tags, err := versionTags(versions[0])
	if err != nil {
		return releasePlan{}, err
	}
	return releasePlan{version: versions[0], tags: tags}, nil
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

// versionTags is docs/container/SPEC.md's tag table, as a function.
//
// A release publishes the full version tag, which never moves, and the three
// that do: the minor tag, the major tag and the rolling tag. A prerelease
// publishes its own tag and nothing else — the moving tags are what a derived
// Dockerfile pins to pick up fixes without an edit, and a release candidate is
// not a fix anybody consented to be given.
func versionTags(tag string) ([]string, error) {
	v, ok := parseVersion(tag)
	if !ok {
		return nil, fmt.Errorf("%q is not a canonical version tag", tag)
	}
	// An OCI tag is [A-Za-z0-9_.-], which has no `+` in it. Refusing is the only
	// honest answer: mangling the metadata away would publish `v0.2.0` and
	// `v0.2.0+build.5` as the same tag, and this project's contract says a
	// published version tag is never repointed.
	if v.build != "" {
		return nil, fmt.Errorf("version tag %q carries build metadata, which cannot be spelled in an OCI tag: release it under a version without one", tag)
	}
	if v.prerelease != "" {
		return []string{tag}, nil
	}
	return []string{
		tag,
		"v" + v.major + "." + v.minor,
		"v" + v.major,
		rollingTag,
	}, nil
}

// releaseImage is one image a release publishes: where it goes, how it is
// pushed, and which executables it ships — the last being what the SBOMs
// describe.
type releaseImage struct {
	repository  string
	executables []string
	publish     func(ctx context.Context, address string) (string, error)
}

// releaseImages is every image a release publishes, derived from the base
// image's repository.
//
// The generator repositories are `<base>-gen-<name>`, which is the naming
// docs/container/SPEC.md uses throughout — `avroc-gen-go` beside `avroc`. The
// set of generators comes from builtinGenerators, so a fourth one is published
// by being added there and nowhere else.
//
// Each entry pushes through Publish or PublishGenerator rather than through a
// path of its own. Those two are what a person calls to put an image on a test
// registry, and a release that pushed by some other route would be exercising
// something nobody can reproduce by hand.
func (m *Avroc) releaseImages(repository, username string, password *dagger.Secret) []releaseImage {
	images := []releaseImage{{
		repository:  repository,
		executables: baseImageExecutables(),
		publish: func(ctx context.Context, address string) (string, error) {
			return m.Publish(ctx, address, username, password)
		},
	}}
	for _, name := range builtinGenerators() {
		images = append(images, releaseImage{
			repository:  repository + "-gen-" + name,
			executables: append(baseImageExecutables(), generatorExecutable(name)),
			publish: func(ctx context.Context, address string) (string, error) {
				return m.PublishGenerator(ctx, name, address, username, password)
			},
		})
	}
	return images
}

// publishTags pushes one image under every tag the release carries, and returns
// the digest they all point at.
//
// Every tag is a separate push of the same content, so the registry stores one
// manifest and the tags are names for it. The digests are required to agree,
// which is the machine-checkable form of the promise the tag table makes: `v0.2`
// and `v0.2.0` are the same image, or one of them is lying.
func publishTags(ctx context.Context, image releaseImage, tags []string) (string, error) {
	var digest string
	for _, tag := range tags {
		ref, err := image.publish(ctx, image.repository+":"+tag)
		if err != nil {
			return "", fmt.Errorf("publishing %s:%s: %w", image.repository, tag, err)
		}
		_, pushed, ok := strings.Cut(ref, "@")
		if !ok {
			return "", fmt.Errorf("publishing %s:%s returned %q, which is not digest-qualified", image.repository, tag, ref)
		}
		switch {
		case digest == "":
			digest = pushed
		case pushed != digest:
			return "", fmt.Errorf("%s:%s published as %s but %s:%s published as %s: every tag of one release names one image", image.repository, tag, pushed, image.repository, tags[0], digest)
		}
	}
	return digest, nil
}

// cosign is the container every signature and attestation is produced in.
//
// The binary is built by the shared Go module at a pinned version rather than
// pulled as a tool image, for the reason this file's comment gives. It is
// installed into a container that has certificate authorities and a shell — the
// signing flow talks to a public CA, a transparency log and the registry — and
// which is emphatically not the published image, since that one deliberately has
// none of those things.
//
// COSIGN_YES is what makes the flow non-interactive: without it cosign asks for
// confirmation before writing to the public transparency log, and a release
// would hang waiting for a person who is not there.
func (m *Avroc) cosign(
	idTokenRequestUrl string,
	idTokenRequestToken *dagger.Secret,
	host, username string,
	password *dagger.Secret,
	startedOn time.Time,
) *dagger.Container {
	return dag.Go().
		Container(dag.Directory()).
		WithFile("/usr/local/bin/cosign", dag.Go().Install(cosignPackage), dagger.ContainerWithFileOpts{
			Permissions: executableMode,
		}).
		// The instant this release started, in the environment so that every
		// signing command below is a different command on every run. Signing is
		// side-effecting — it writes to a registry and to a public transparency
		// log — and an exec whose arguments are identical to a previous one is
		// one Dagger is entitled to skip. A release that silently skipped its
		// own signatures would report success having signed nothing.
		WithEnvVariable("AVROC_RELEASE_STARTED_ON", startedOn.Format(time.RFC3339Nano)).
		WithEnvVariable("COSIGN_YES", "true").
		// The two halves of the workload identity exchange, in the environment
		// cosign's GitHub Actions provider reads them from. The token is a
		// secret variable rather than a plain one because it mints identity
		// tokens for this repository.
		WithEnvVariable("ACTIONS_ID_TOKEN_REQUEST_URL", idTokenRequestUrl).
		WithSecretVariable("ACTIONS_ID_TOKEN_REQUEST_TOKEN", idTokenRequestToken).
		WithSecretVariable("REGISTRY_PASSWORD", password).
		// Through stdin rather than on the command line: an argument is visible
		// to anything that can list processes in the container, and a token that
		// can write packages is worth the extra line.
		WithExec([]string{
			"sh", "-c",
			`printf '%s' "$REGISTRY_PASSWORD" | cosign login "$1" --username "$2" --password-stdin`,
			"sh", host, username,
		})
}

// attest signs one published digest and attaches its provenance and SBOMs.
//
// The signature comes first: it is the claim the other two are read under, and a
// digest carrying attestations nobody signed is worse than one carrying none,
// because it looks verified from a distance.
//
// Everything names the digest rather than a tag, and the digest is the one
// publishTags resolved — so what is signed is what was pushed, not whatever the
// tag has come to mean by the time cosign resolves it.
//
// The signature reaches further than the attestations do, and deliberately:
// signing is recursive over the index's per-platform manifests, while the
// attestations go on the index digest alone. An attestation is a statement about
// the release, and the release is the index; docs/container/SPEC.md says so
// explicitly, so that nobody goes looking for a provenance statement on the
// manifest their runtime happened to pull.
func (m *Avroc) attest(
	ctx context.Context,
	signer *dagger.Container,
	image releaseImage,
	digest string,
	predicate *dagger.File,
) error {
	const predicateAt = "/attestations/predicate.json"

	ref := image.repository + "@" + digest

	// --recursive because what is published is an index over two platforms, and
	// signing the index alone leaves the manifest a consumer's runtime actually
	// pulls unsigned. `cosign verify` against the tag would still pass, which is
	// the shape of gap worth closing rather than documenting.
	c := signer.
		WithExec([]string{"cosign", "sign", "--recursive", ref}).
		WithFile(predicateAt, predicate).
		WithExec([]string{"cosign", "attest", "--type", provenancePredicate, "--predicate", predicateAt, ref})

	// One SBOM per executable per platform. Each document is tied to the
	// SHA-256 of the binary it describes, so a single document for an image
	// holding two executables on two platforms would name one of the four and
	// be wrong about the other three.
	for _, platform := range imagePlatforms() {
		binaries := m.binaries(platform)
		for _, executable := range image.executables {
			at := fmt.Sprintf("/attestations/%s-%s.spdx.json", executable, strings.ReplaceAll(string(platform), "/", "-"))
			c = c.
				WithFile(at, dag.Go().Spdx(binaries.File(executable), m.Source)).
				WithExec([]string{"cosign", "attest", "--type", sbomPredicate, "--predicate", at, ref})
		}
	}

	if _, err := c.Sync(ctx); err != nil {
		return fmt.Errorf("signing and attesting %s: %w", ref, err)
	}
	return nil
}

// provenanceFacts is everything the provenance predicate says, gathered before
// anything is rendered so that one release's four statements agree about the
// commit, the ref and the run that produced them.
type provenanceFacts struct {
	Repository string
	Version    string
	Tags       []string
	Source     string
	Commit     string
	Builder    string
	Invocation string
	StartedOn  time.Time
}

// provenanceStatement renders the SLSA v1 provenance predicate for one image.
//
// It is the predicate alone rather than a whole in-toto statement: cosign builds
// the statement and fills in the subject from the digest it is signing, which is
// the one field that must not be taken on this side's word.
//
// What it claims is deliberately narrow. Every field is something read from the
// repository or handed in by the run — the commit, the ref, the workflow — and
// nothing is asserted about hermeticity or reproducibility that this pipeline
// does not check. A predicate that claimed more than it knew would be worse than
// none, because a consumer would act on it.
func provenanceStatement(facts provenanceFacts) (*dagger.File, error) {
	platforms := make([]string, 0, len(imagePlatforms()))
	for _, p := range imagePlatforms() {
		platforms = append(platforms, string(p))
	}

	predicate := map[string]any{
		"buildDefinition": map[string]any{
			"buildType": buildType,
			"externalParameters": map[string]any{
				"repository": facts.Repository,
				"version":    facts.Version,
				"tags":       facts.Tags,
				"platforms":  platforms,
			},
			"internalParameters": map[string]any{},
			"resolvedDependencies": []any{
				map[string]any{
					"uri":    "git+" + facts.Source + "@refs/tags/" + facts.Version,
					"digest": map[string]any{"gitCommit": facts.Commit},
				},
			},
		},
		"runDetails": map[string]any{
			"builder": map[string]any{"id": facts.Builder},
			"metadata": map[string]any{
				"invocationId": facts.Invocation,
				"startedOn":    facts.StartedOn.Format(time.RFC3339),
			},
		},
	}

	body, err := json.MarshalIndent(predicate, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("rendering the provenance predicate for %s: %w", facts.Repository, err)
	}
	return dag.Directory().WithNewFile("predicate.json", string(body)+"\n").File("predicate.json"), nil
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

// headCommit is the full SHA at HEAD, which is what the provenance names as the
// source it was built from. Full rather than abbreviated: an abbreviation is a
// prefix somebody has to resolve against a repository they may not have.
func headCommit(ctx context.Context, source, gitDir *dagger.Directory) (string, error) {
	out, err := gitContainer(source, gitDir).
		WithExec([]string{"git", "rev-parse", "HEAD"}).
		Stdout(ctx)
	if err != nil {
		return "", fmt.Errorf("reading the commit at HEAD: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// sourceURI is where the source came from, as the provenance's resolved
// dependency. It is read from the checkout rather than passed in, so it names
// the repository that was actually built and not the one somebody meant.
func sourceURI(ctx context.Context, source, gitDir *dagger.Directory) (string, error) {
	out, err := gitContainer(source, gitDir).
		WithExec([]string{"git", "config", "--get", "remote.origin.url"}).
		Stdout(ctx)
	if err != nil {
		return "", fmt.Errorf("reading the source repository's origin: %w", err)
	}
	// The `.git` suffix is how a clone URL is written and not part of the
	// repository's identity; dropping it is what makes the provenance's URI the
	// one a person would recognise.
	return strings.TrimSuffix(strings.TrimSpace(out), ".git"), nil
}

// gitContainer is a container holding the source with its git metadata grafted
// back on.
//
// New drops `.git` from the source it binds, because none of the check stages
// reads git metadata and leaving it in would make every commit a cache miss for
// all of them. A release does read it, so it arrives here as its own argument
// and is mounted rather than folded into the source — which keeps the release's
// need from costing every other stage its cache.
func gitContainer(source, gitDir *dagger.Directory) *dagger.Container {
	return dag.Go().Container(source).WithMountedDirectory("/src/.git", gitDir)
}

// registryHost is the host part of a repository reference, which is what cosign
// authenticates against. `ghcr.io/z5labs/avroc` is a repository on `ghcr.io`;
// everything after the first slash is a path within it.
func registryHost(repository string) string {
	host, _, _ := strings.Cut(repository, "/")
	return host
}
