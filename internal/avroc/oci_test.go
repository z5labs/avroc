// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/z5labs/avroc/internal/cli"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// pushTestImage spins up an in-memory OCI registry, pushes a random image to
// src:tag, and returns the registry-local source reference and the image's
// digest. The host is addressed as "localhost:<port>" so go-containerregistry
// talks plain HTTP to it (no TLS, no name.Insecure needed). The returned stop
// func shuts the registry down, which later asserts cache hits do no network.
func pushTestImage(t *testing.T, repo, tag string) (src, digest string, stop func()) {
	t.Helper()

	s := httptest.NewServer(registry.New())
	// httptest listens on 127.0.0.1; address it as localhost so ggcr uses HTTP.
	host := "localhost" + strings.TrimPrefix(s.URL, "http://127.0.0.1")
	src = host + "/" + repo

	img, err := random.Image(1024, 2)
	if err != nil {
		s.Close()
		t.Fatalf("random.Image: %v", err)
	}

	ref, err := name.ParseReference(src + ":" + tag)
	if err != nil {
		s.Close()
		t.Fatalf("ParseReference: %v", err)
	}
	if err := remote.Write(ref, img, remote.WithAuthFromKeychain(authn.DefaultKeychain)); err != nil {
		s.Close()
		t.Fatalf("remote.Write: %v", err)
	}

	h, err := img.Digest()
	if err != nil {
		s.Close()
		t.Fatalf("img.Digest: %v", err)
	}
	return src, h.String(), s.Close
}

// pushTestIndex pushes a multi-arch image index and returns its source
// reference and the platform-independent index digest.
func pushTestIndex(t *testing.T, repo, tag string) (src, digest string, stop func()) {
	t.Helper()

	s := httptest.NewServer(registry.New())
	host := "localhost" + strings.TrimPrefix(s.URL, "http://127.0.0.1")
	src = host + "/" + repo

	idx, err := random.Index(1024, 1, 2)
	if err != nil {
		s.Close()
		t.Fatalf("random.Index: %v", err)
	}
	ref, err := name.ParseReference(src + ":" + tag)
	if err != nil {
		s.Close()
		t.Fatalf("ParseReference: %v", err)
	}
	if err := remote.WriteIndex(ref, idx, remote.WithAuthFromKeychain(authn.DefaultKeychain)); err != nil {
		s.Close()
		t.Fatalf("remote.WriteIndex: %v", err)
	}
	h, err := idx.Digest()
	if err != nil {
		s.Close()
		t.Fatalf("idx.Digest: %v", err)
	}
	return src, h.String(), s.Close
}

func TestOCIRemoteFetcher(t *testing.T) {
	ctx := context.Background()

	t.Run("resolve returns the pushed image digest", func(t *testing.T) {
		src, want, stop := pushTestImage(t, "avroc-gen-go", "v0.1.0")
		defer stop()

		got, err := remoteFetcher{}.resolve(ctx, src+":v0.1.0")
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("resolve = %q, want %q", got, want)
		}
	})

	t.Run("pull caches the image and verifyCached passes", func(t *testing.T) {
		src, want, stop := pushTestImage(t, "avroc-gen-go", "v0.1.0")
		defer stop()
		cache := t.TempDir()

		got, err := remoteFetcher{}.pull(ctx, src+"@"+want, cache)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("pull = %q, want %q", got, want)
		}
		if err := verifyCached(cache, want); err != nil {
			t.Errorf("verifyCached after pull: %v", err)
		}
	})

	t.Run("pull by digest is a no-network cache hit once cached", func(t *testing.T) {
		src, want, stop := pushTestImage(t, "avroc-gen-go", "v0.1.0")
		cache := t.TempDir()

		if _, err := (remoteFetcher{}).pull(ctx, src+"@"+want, cache); err != nil {
			t.Fatal(err)
		}
		// Shut the registry down: a real second pull would now fail. A verified
		// cache hit must succeed without touching the network.
		stop()

		got, err := remoteFetcher{}.pull(ctx, src+"@"+want, cache)
		if err != nil {
			t.Fatalf("expected cache hit with registry down, got: %v", err)
		}
		if got != want {
			t.Errorf("pull = %q, want %q", got, want)
		}
	})

	t.Run("multi-arch index resolves and caches by its platform-independent digest", func(t *testing.T) {
		src, want, stop := pushTestIndex(t, "avroc-gen-go", "v0.1.0")
		defer stop()
		cache := t.TempDir()

		// resolve returns the index digest, not a per-platform image digest.
		got, err := remoteFetcher{}.resolve(ctx, src+":v0.1.0")
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("resolve = %q, want index digest %q", got, want)
		}

		stored, err := remoteFetcher{}.pull(ctx, src+"@"+want, cache)
		if err != nil {
			t.Fatal(err)
		}
		if stored != want {
			t.Errorf("pull = %q, want %q", stored, want)
		}
		if err := verifyCached(cache, want); err != nil {
			t.Errorf("verifyCached for index: %v", err)
		}
	})

	t.Run("verifyCached fails for an uncached digest", func(t *testing.T) {
		cache := t.TempDir()
		err := verifyCached(cache, "sha256:"+strings.Repeat("a", 64))
		if err == nil {
			t.Fatal("expected error for uncached digest, got nil")
		}
	})

	t.Run("verifyCached rejects a malformed digest", func(t *testing.T) {
		if err := verifyCached(t.TempDir(), "not-a-digest"); err == nil {
			t.Fatal("expected error for malformed digest, got nil")
		}
	})
}

// TestGetEndToEnd exercises the real remoteFetcher through getWithFetcher
// against an in-memory registry: resolve -> pull -> cache -> lock, then a
// reproducible rerun with the registry shut down to prove the cache + pin make
// the second run offline.
func TestGetEndToEnd(t *testing.T) {
	src, digest, stop := pushTestImage(t, "avroc-gen-go", "v0.1.0")

	dir := t.TempDir()
	writeManifest(t, dir, fmt.Sprintf(`{
		"generators": [
			{"name": "go", "source": %q, "version": "v0.1.0", "out": "gen"}
		]
	}`, src))

	c := getContext(dir)
	if code := getWithFetcher(context.Background(), c, remoteFetcher{}, false); code != 0 {
		stop()
		t.Fatalf("first get exit = %d, want 0", code)
	}

	lock := readLock(t, dir)
	if len(lock.Generators) != 1 {
		stop()
		t.Fatalf("locked generators = %d, want 1", len(lock.Generators))
	}
	if got := lock.Generators[0].Digest; got != digest {
		stop()
		t.Fatalf("locked digest = %q, want %q", got, digest)
	}

	cachePath := filepath.Join(dir, "cache")
	if err := verifyCached(cachePath, digest); err != nil {
		stop()
		t.Fatalf("verifyCached after get: %v", err)
	}

	// Shut the registry down: a reproducible rerun must reuse the pin and serve
	// the image from cache without any network access.
	stop()
	if code := getWithFetcher(context.Background(), getContext(dir), remoteFetcher{}, false); code != 0 {
		t.Fatalf("offline rerun exit = %d, want 0", code)
	}
	if got := readLock(t, dir).Generators[0].Digest; got != digest {
		t.Fatalf("digest changed on rerun = %q, want %q", got, digest)
	}
}

func TestCacheDir(t *testing.T) {
	t.Run("honors AVROC_CACHE override", func(t *testing.T) {
		env := cli.EnvironmentFunc(func(k string) (string, bool) {
			if k == "AVROC_CACHE" {
				return "/tmp/custom-avroc", true
			}
			return "", false
		})
		got, err := cacheDir(env)
		if err != nil {
			t.Fatal(err)
		}
		if got != "/tmp/custom-avroc" {
			t.Errorf("cacheDir = %q, want /tmp/custom-avroc", got)
		}
	})

	t.Run("falls back to user cache dir when unset", func(t *testing.T) {
		env := cli.EnvironmentFunc(func(string) (string, bool) { return "", false })
		got, err := cacheDir(env)
		if err != nil {
			t.Fatal(err)
		}
		if got == "" || !strings.HasSuffix(got, "avroc") {
			t.Errorf("cacheDir = %q, want a path ending in avroc", got)
		}
	})
}

// ensure imageCachePath stays digest-addressed and stable.
func TestImageCachePath(t *testing.T) {
	h, err := v1.NewHash("sha256:" + strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	got := imageCachePath("/cache", h)
	// Build the expected path with filepath.Join so the separator matches the
	// host OS (backslash on Windows), keeping the assertion platform-agnostic.
	want := filepath.Join("/cache", "images", "sha256-"+strings.Repeat("b", 64))
	if got != want {
		t.Errorf("imageCachePath = %q, want %q", got, want)
	}
}
