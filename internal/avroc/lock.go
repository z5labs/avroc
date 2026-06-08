// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"

	"github.com/z5labs/avroc/internal/cli"
)

// lockFilename is the fixed name of the committed lockfile avroc get writes. It
// is the reproducibility record: each manifest generator's source + version
// pinned to the immutable digest that was resolved and cached.
const lockFilename = "avroc.lock"

// lockfileVersion is the schema version of the lockfile format. It lets future
// avroc releases detect and migrate older lockfiles instead of misreading them.
const lockfileVersion = 1

// lockfile is the parsed avroc.lock: the pinned digest for every generator
// acquired by avroc get.
type lockfile struct {
	Version    int               `json:"version"`
	Generators []lockedGenerator `json:"generators"`
}

// lockedGenerator pins one manifest generator entry. Source and Version mirror
// the manifest request so a manifest edit (a moved or bumped tag) can be
// detected as drift; Digest is the immutable sha256: digest that request
// resolved to.
type lockedGenerator struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// loadLockfile reads and parses avroc.lock under workingDir. A missing lockfile
// is not an error: it returns an empty lockfile so the first avroc get starts
// from a clean slate. Unknown fields are rejected so a corrupt or tampered
// lockfile surfaces as an error rather than being silently ignored.
func loadLockfile(cli cli.Context, workingDir string) (*lockfile, error) {
	data, err := fs.ReadFile(cli.OpenDir(workingDir), lockFilename)
	if errors.Is(err, fs.ErrNotExist) {
		return &lockfile{Version: lockfileVersion}, nil
	}
	if err != nil {
		return nil, err
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var l lockfile
	if err := dec.Decode(&l); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", lockFilename, err)
	}

	// Decode stops after the first JSON value; reject any trailing content so a
	// malformed or accidentally concatenated lockfile surfaces as an error.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("unexpected trailing data after JSON object in %s", lockFilename)
	}

	return &l, nil
}

// find returns the locked entry for a manifest generator request. An entry is
// reusable only when name, source, and version all match: a changed source or
// version is drift and must be re-resolved, not served from the stale pin.
func (l *lockfile) find(name, source, version string) (lockedGenerator, bool) {
	for _, g := range l.Generators {
		if g.Name == name && g.Source == source && g.Version == version {
			return g, true
		}
	}
	return lockedGenerator{}, false
}

// marshalLock renders a lockfile as the canonical JSON avroc get writes:
// generators sorted by name, two-space indentation, and a trailing newline, so
// reruns produce byte-identical output and diffs stay reviewable.
func marshalLock(l *lockfile) ([]byte, error) {
	out := lockfile{
		Version:    l.Version,
		Generators: slices.Clone(l.Generators),
	}
	if out.Version == 0 {
		out.Version = lockfileVersion
	}
	slices.SortFunc(out.Generators, func(a, b lockedGenerator) int {
		return strings.Compare(a.Name, b.Name)
	})

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
