// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/z5labs/avroc/internal/cli"
)

// runGet acquires every OCI-sourced generator declared in avroc.json: it
// resolves each floating tag to an immutable digest, pulls the image into the
// local cache, and writes the pinned digests to avroc.lock. By default a digest
// already pinned in the lockfile for an unchanged source+version is reused so
// reruns are reproducible; -upgrade re-resolves the tags to fresh digests.
func runGet(ctx context.Context, c cli.Context) int {
	flags := flag.NewFlagSet("get", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	upgrade := flags.Bool("upgrade", false, "re-resolve floating tags to fresh digests, ignoring pinned lockfile entries")
	if err := flags.Parse(c.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}

	return getWithFetcher(ctx, c, remoteFetcher{}, *upgrade)
}

// getWithFetcher is the testable core of avroc get: the registry interaction is
// behind the imageFetcher interface so the acquisition logic can be exercised
// without a network or a registry.
func getWithFetcher(ctx context.Context, c cli.Context, fetcher imageFetcher, upgrade bool) int {
	manifest, err := loadManifest(c, c.WorkingDir)
	if err != nil {
		c.Log.ErrorContext(ctx, "failed to load manifest", slog.Any("error", err))
		return 1
	}

	lock, err := loadLockfile(c, c.WorkingDir)
	if err != nil {
		c.Log.ErrorContext(ctx, "failed to load lockfile", slog.Any("error", err))
		return 1
	}

	cacheRoot, err := cacheDir(c.Env)
	if err != nil {
		c.Log.ErrorContext(ctx, "failed to determine cache directory", slog.Any("error", err))
		return 1
	}

	var locked []lockedGenerator
	for _, g := range manifest.Generators {
		if g.Source == "" {
			c.Log.InfoContext(ctx, "skipping generator with no OCI source", slog.String("generator", g.Name))
			continue
		}
		if g.Version == "" {
			c.Log.ErrorContext(ctx, "generator declares a source but no version", slog.String("generator", g.Name), slog.String("source", g.Source))
			return 1
		}

		// Reuse the pinned digest for an unchanged request so reruns are
		// reproducible; -upgrade forces a fresh tag resolution.
		digest := ""
		if !upgrade {
			if entry, ok := lock.find(g.Name, g.Source, g.Version); ok {
				digest = entry.Digest
			}
		}

		if digest == "" {
			resolved, err := fetcher.resolve(ctx, g.Source+":"+g.Version)
			if err != nil {
				c.Log.ErrorContext(ctx, "failed to resolve generator image", slog.String("generator", g.Name), slog.Any("error", err))
				return 1
			}
			digest = resolved
			c.Log.InfoContext(ctx, "resolved generator image",
				slog.String("generator", g.Name),
				slog.String("source", g.Source),
				slog.String("version", g.Version),
				slog.String("digest", digest))
		}

		stored, err := fetcher.pull(ctx, g.Source+"@"+digest, cacheRoot)
		if err != nil {
			c.Log.ErrorContext(ctx, "failed to pull generator image", slog.String("generator", g.Name), slog.Any("error", err))
			return 1
		}

		locked = append(locked, lockedGenerator{
			Name:    g.Name,
			Source:  g.Source,
			Version: g.Version,
			Digest:  stored,
		})
	}

	if len(locked) == 0 {
		c.Log.InfoContext(ctx, "no OCI generators to acquire; nothing to lock")
		return 0
	}

	data, err := marshalLock(&lockfile{Version: lockfileVersion, Generators: locked})
	if err != nil {
		c.Log.ErrorContext(ctx, "failed to render lockfile", slog.Any("error", err))
		return 1
	}

	// Skip the write when nothing changed so a reproducible rerun leaves the
	// committed lockfile (and its mtime) untouched.
	if existing, err := fs.ReadFile(c.OpenDir(c.WorkingDir), lockFilename); err == nil && bytes.Equal(existing, data) {
		c.Log.InfoContext(ctx, "lockfile already up to date", slog.Int("generators", len(locked)))
		return 0
	}

	dst := filepath.Join(c.WorkingDir, lockFilename)
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		c.Log.ErrorContext(ctx, "failed to write lockfile", slog.String("path", dst), slog.Any("error", err))
		return 1
	}

	c.Log.InfoContext(ctx, "wrote lockfile", slog.String("path", lockFilename), slog.Int("generators", len(locked)))
	return 0
}
