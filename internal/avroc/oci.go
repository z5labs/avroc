// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/z5labs/avroc/internal/cli"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// imageFetcher resolves a generator's floating tag to an immutable digest and
// pulls the referenced image into the local cache. It is an interface so the
// get command can be unit-tested without a registry; remoteFetcher is the
// real, daemonless implementation backed by go-containerregistry.
type imageFetcher interface {
	// resolve returns the host-platform image digest the reference (a
	// source:tag) currently points at.
	resolve(ctx context.Context, ref string) (string, error)
	// pull fetches the content at ref into cacheDir and returns its digest. A
	// digest reference (source@sha256:...) already present and verified in the
	// cache is a no-op that performs no network access.
	pull(ctx context.Context, ref, cacheDir string) (string, error)
}

// remoteFetcher pulls images directly from a registry over HTTP, with no Docker
// daemon. Registry auth is resolved through authn.DefaultKeychain, which reads
// ~/.docker/config.json and platform keychains, so a prior `docker login
// ghcr.io` transparently authenticates private and ghcr.io pulls.
type remoteFetcher struct{}

func remoteOptions(ctx context.Context) []remote.Option {
	return []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	}
}

// resolve returns the digest the tag points at. No platform is selected, so the
// digest is the manifest the registry serves for the tag — an image manifest
// for a single-arch image, or the multi-arch index digest otherwise. Keeping it
// platform-independent means a committed avroc.lock is identical on every
// developer machine and CI runner rather than churning per OS/arch.
func (remoteFetcher) resolve(ctx context.Context, ref string) (string, error) {
	r, err := name.ParseReference(ref)
	if err != nil {
		return "", fmt.Errorf("invalid image reference %q: %w", ref, err)
	}
	desc, err := remote.Head(r, remoteOptions(ctx)...)
	if err != nil {
		return "", fmt.Errorf("failed to resolve %q: %w", ref, err)
	}
	return desc.Digest.String(), nil
}

func (remoteFetcher) pull(ctx context.Context, ref, cacheDir string) (string, error) {
	r, err := name.ParseReference(ref)
	if err != nil {
		return "", fmt.Errorf("invalid image reference %q: %w", ref, err)
	}

	// A digest reference whose content is already cached and verifies needs no
	// network access — the populated cache is the offline path.
	if dr, ok := r.(name.Digest); ok {
		if err := verifyCached(cacheDir, dr.DigestStr()); err == nil {
			return dr.DigestStr(), nil
		}
	}

	desc, err := remote.Get(r, remoteOptions(ctx)...)
	if err != nil {
		return "", fmt.Errorf("failed to pull %q: %w", ref, err)
	}

	if err := writeToCache(cacheDir, desc); err != nil {
		return "", err
	}
	// Re-open and verify what was written so a torn or corrupt cache entry is
	// caught here rather than at generation time.
	if err := verifyCached(cacheDir, desc.Digest.String()); err != nil {
		return "", fmt.Errorf("cached image %q failed verification: %w", desc.Digest, err)
	}
	return desc.Digest.String(), nil
}

// writeToCache stores the descriptor's content (a single image or a full
// multi-arch index, faithfully) as a self-contained OCI layout under a
// per-digest directory. The directory is rebuilt from scratch so a previous
// partial write cannot leave mixed content behind.
func writeToCache(cacheDir string, desc *remote.Descriptor) error {
	dir := imageCachePath(cacheDir, desc.Digest)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("failed to clear cache dir %q: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create cache dir %q: %w", dir, err)
	}
	lp, err := layout.Write(dir, empty.Index)
	if err != nil {
		return fmt.Errorf("failed to initialize cache layout %q: %w", dir, err)
	}

	if desc.MediaType.IsIndex() {
		idx, err := desc.ImageIndex()
		if err != nil {
			return fmt.Errorf("failed to read image index %q: %w", desc.Digest, err)
		}
		if err := lp.AppendIndex(idx); err != nil {
			return fmt.Errorf("failed to write image index to cache %q: %w", dir, err)
		}
		return nil
	}

	img, err := desc.Image()
	if err != nil {
		return fmt.Errorf("failed to read image %q: %w", desc.Digest, err)
	}
	if err := lp.AppendImage(img); err != nil {
		return fmt.Errorf("failed to write image to cache %q: %w", dir, err)
	}
	return nil
}

// verifyCached confirms the content pinned to digest is present in the cache and
// that its recomputed digest matches. It handles both single images and
// multi-arch indexes. It is the verify-against-lock primitive: get uses it to
// skip re-pulling, and the execution work (#70) uses it before running a cached
// generator.
func verifyCached(cacheDir, digest string) error {
	h, err := v1.NewHash(digest)
	if err != nil {
		return fmt.Errorf("invalid digest %q: %w", digest, err)
	}
	dir := imageCachePath(cacheDir, h)
	lp, err := layout.FromPath(dir)
	if err != nil {
		return fmt.Errorf("image %q not cached: %w", digest, err)
	}
	ii, err := lp.ImageIndex()
	if err != nil {
		return fmt.Errorf("failed to read cache layout for %q: %w", digest, err)
	}
	manifest, err := ii.IndexManifest()
	if err != nil {
		return fmt.Errorf("failed to read cache index for %q: %w", digest, err)
	}

	for _, d := range manifest.Manifests {
		if d.Digest != h {
			continue
		}
		got, err := recomputeCachedDigest(ii, d)
		if err != nil {
			return err
		}
		if got != h {
			return fmt.Errorf("cached digest %q does not match expected %q", got, h)
		}
		return nil
	}
	return fmt.Errorf("image %q not found in cache", digest)
}

// recomputeCachedDigest re-reads the cached object referenced by d and returns
// its computed digest, so verification is content-based rather than trusting the
// recorded descriptor.
func recomputeCachedDigest(ii v1.ImageIndex, d v1.Descriptor) (v1.Hash, error) {
	if d.MediaType.IsIndex() {
		child, err := ii.ImageIndex(d.Digest)
		if err != nil {
			return v1.Hash{}, fmt.Errorf("cached index %q not readable: %w", d.Digest, err)
		}
		return child.Digest()
	}
	child, err := ii.Image(d.Digest)
	if err != nil {
		return v1.Hash{}, fmt.Errorf("cached image %q not readable: %w", d.Digest, err)
	}
	return child.Digest()
}

// imageCachePath is the per-digest directory holding one image's OCI layout.
func imageCachePath(cacheDir string, h v1.Hash) string {
	return filepath.Join(cacheDir, "images", h.Algorithm+"-"+h.Hex)
}

// cacheDir resolves the root directory pulled images are cached under:
// AVROC_CACHE when set, otherwise the user cache dir joined with "avroc".
func cacheDir(env cli.Environment) (string, error) {
	if dir, ok := env.LookupEnv("AVROC_CACHE"); ok && dir != "" {
		return dir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine user cache dir (set AVROC_CACHE to override): %w", err)
	}
	return filepath.Join(base, "avroc"), nil
}
