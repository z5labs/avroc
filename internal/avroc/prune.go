// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// outputRecordFilename is the fixed name of the record avroc writes beside the
// manifest naming every file the last successful run generated.
//
// It is the whole mechanism behind pruning, and it is a committed file rather
// than a marker inside each generated file on purpose. A "DO NOT EDIT" header
// cannot be the mechanism: not every format a generator emits has a comment
// syntax to put one in — `avroc-gen-pcf` and `avroc-gen-json` emit JSON, which
// has none — it would ask every plugin for bookkeeping the contract deliberately
// does not ask for (docs/plugin/SPEC.md, "The output directory"), and it makes
// ownership a property of a file's contents, which a person can copy into a
// hand-written file by accident.
//
// It sits at the project root, next to avroc.json, rather than one per output
// directory, because a generator *removed from the manifest* is the case a
// per-directory record cannot cover: avroc would never look in its output
// directory again, and everything it produced would live there forever. The
// record is the file set of the last run, whatever the manifest says now.
const outputRecordFilename = "avroc.gen.json"

// outputRecordVersion is the schema version of the record format. It lets a
// future avroc detect and migrate an older record instead of misreading it, the
// same way lockfileVersion does.
const outputRecordVersion = 1

// outputRecord is the parsed avroc.gen.json: every file the last successful run
// generated, relative to the project root and slash-separated.
//
// Paths and nothing else. Attributing each file to the generator that produced
// it would read well in a diff and is deliberately absent: the prune does not
// consult it, two generators cannot both produce one path (checkCollisions), and
// it would make renaming a generator churn a file whose real content — the set
// of paths avroc owns — had not changed.
type outputRecord struct {
	Version int      `json:"version"`
	Files   []string `json:"files"`
}

// loadOutputRecord reads and parses avroc.gen.json under projectRoot.
//
// A missing record is not an error: it returns an empty record, so a project
// that has never been generated — or one whose record was never committed —
// prunes nothing rather than guessing which files in the tree are avroc's. That
// is the safe direction of the two: a stale file survives one more run, and no
// file a person wrote is ever removed on a hunch.
//
// It reads through os rather than through cli.Context.OpenDir, unlike the
// manifest and the lockfile. The record is the list of files pruneStale then
// removes with os.Remove, and reading it through an injectable filesystem would
// let the record avroc validates and the tree avroc edits come from two
// different places — which is the one way this file could be made to delete
// something nobody recorded.
func loadOutputRecord(projectRoot string) (*outputRecord, error) {
	data, err := os.ReadFile(filepath.Join(projectRoot, outputRecordFilename))
	if errors.Is(err, fs.ErrNotExist) {
		return &outputRecord{Version: outputRecordVersion}, nil
	}
	if err != nil {
		return nil, err
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var r outputRecord
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", outputRecordFilename, err)
	}

	// Decode stops after the first JSON value; reject any trailing content so a
	// malformed or accidentally concatenated record surfaces as an error rather
	// than being read for the part that parsed.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("unexpected trailing data after JSON object in %s", outputRecordFilename)
	}

	// A version this build does not understand is a format avroc would be guessing
	// at, and the record is authoritative for deletion — so the only versions
	// accepted are the current one and 0, the field omitted, which is read as the
	// current schema for tolerance of a hand-written record. Newer is reported
	// separately because the response to it is to upgrade avroc, and a negative or
	// unknown-older version is not an old avroc's record at all.
	switch {
	case r.Version > outputRecordVersion:
		return nil, fmt.Errorf("%s has version %d, which is newer than this avroc supports (%d); upgrade avroc", outputRecordFilename, r.Version, outputRecordVersion)
	case r.Version != 0 && r.Version != outputRecordVersion:
		return nil, fmt.Errorf("%s has version %d, which is not a version this avroc understands (%d, or the field omitted)", outputRecordFilename, r.Version, outputRecordVersion)
	}

	if err := r.normalize(); err != nil {
		return nil, err
	}
	return &r, nil
}

// normalize holds every path in a record to the same rule the manifest holds an
// output directory to — local to the project, no absolute path and no ".."
// traversal — and rewrites what survives into the one spelling avroc itself
// writes.
//
// The validation is what makes a corrupt or tampered record a failed run rather
// than a deletion. Every path avroc writes into a record came from a generator's
// output directory, which the manifest already required to be local, so a record
// naming anything else was not written by avroc — and the file it names is not
// avroc's to remove.
//
// The rewriting is what makes the validation hold for a path *spelled* another
// way, and it matters twice. "./avroc.gen.json" and "gen/../avroc.gen.json" both
// name the record itself while passing an equality test against the filename, and
// avroc would then delete its own record. "./gen/user.go" names a file a run
// produces as "gen/user.go", and pruneStale compares the two sets as strings — so
// left alone, a record with a redundant "./" would make avroc remove the file it
// had just generated. Cleaning here means every later comparison is between
// paths in one form.
func (r *outputRecord) normalize() error {
	for i, f := range r.Files {
		// On the path as written, because Clean turns "" into ".", and "" is not a
		// file this record may name.
		if !filepath.IsLocal(filepath.FromSlash(f)) {
			return fmt.Errorf("%s records %q, which is not a relative path within the project (no absolute paths or %q traversal): avroc will not remove it", outputRecordFilename, f, "..")
		}

		clean := filepath.Clean(filepath.FromSlash(f))
		if clean == outputRecordFilename {
			return fmt.Errorf("%s records itself as %q, and it is avroc's record rather than generated output", outputRecordFilename, f)
		}
		if clean == "." {
			return fmt.Errorf("%s records %q, which is the project directory rather than a generated file", outputRecordFilename, f)
		}
		r.Files[i] = filepath.ToSlash(clean)
	}
	return nil
}

// marshalOutputRecord renders the record as the canonical JSON avroc writes:
// paths sorted and deduplicated, two-space indentation, and a trailing newline,
// so that two runs over unchanged inputs produce byte-identical bytes and the
// diff of a rename is the two lines it actually is.
func marshalOutputRecord(files []string) ([]byte, error) {
	sorted := slices.Clone(files)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	// Non-nil for an empty set, so a run that generated nothing records an empty
	// array rather than JSON null.
	if sorted == nil {
		sorted = []string{}
	}

	data, err := json.MarshalIndent(outputRecord{Version: outputRecordVersion, Files: sorted}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// writeOutputRecord replaces the record with the file set this run produced.
//
// The write is skipped when the bytes are unchanged, which is the common case: a
// regeneration that produced the same files leaves the committed record and its
// mtime exactly as it found them, so nothing downstream rebuilds because avroc
// ran.
func writeOutputRecord(ctx context.Context, log *slog.Logger, projectRoot string, files []string) error {
	data, err := marshalOutputRecord(files)
	if err != nil {
		return fmt.Errorf("failed to render %s: %w", outputRecordFilename, err)
	}

	dst := filepath.Join(projectRoot, outputRecordFilename)
	if existing, err := os.ReadFile(dst); err == nil && bytes.Equal(existing, data) {
		return nil
	}

	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", outputRecordFilename, err)
	}
	log.InfoContext(ctx, "recorded the generated output",
		slog.String("path", outputRecordFilename),
		slog.Int("files", len(files)),
	)
	return nil
}

// producedFiles names every file a run merged into the project tree, relative to
// the project root and slash-separated: the record the run is about to leave
// behind, and the set the previous run's record is pruned against.
//
// It is derived from the destinations the merge resolved rather than from each
// generator's own relative paths, because two generators need not share an
// output directory: "user.avsc" means a different file under each, and only the
// destination says which.
func producedFiles(projectRoot string, outs []*generatorOutput) ([]string, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, err
	}

	files := make([]string, 0, len(outs))
	for _, out := range outs {
		for _, f := range out.files {
			rel, err := filepath.Rel(root, f.dst)
			if err != nil {
				return nil, fmt.Errorf("generator %q: failed to record %q relative to the project: %w", out.generator, f.dst, err)
			}
			if !filepath.IsLocal(rel) {
				// Unreachable through a valid manifest — Manifest.validate requires
				// every output directory to be local — and reported rather than
				// recorded anyway, because a path outside the project is one a later
				// run would be asked to delete from a record that says it is inside.
				return nil, fmt.Errorf("generator %q produced %q, which is outside the project directory", out.generator, f.dst)
			}
			files = append(files, filepath.ToSlash(rel))
		}
	}
	return files, nil
}

// pruneStale removes every file the previous run recorded that this run did not
// produce.
//
// That comparison is the whole of the mechanism, and it is what makes an output
// directory shared with hand-written source safe rather than merely tolerated: a
// file a person wrote is in no record, so it is never a candidate. avroc removes
// what it recorded having written, and nothing else.
//
// It runs after the merge rather than before it, so a run that fails, collides,
// or produces a path it should not have leaves the tree exactly as it found it —
// the same all-or-nothing the merge already gives. The cost is that a rename
// takes the tree through a moment holding both files; a crash in that moment
// leaves the stale one, and the next run removes it, because the record still
// names it.
//
// A path that is no longer a regular file is left alone and reported. A person
// who replaced a generated file with a directory or a link has taken it over,
// and avroc removing it would be avroc deleting something it did not write.
func pruneStale(ctx context.Context, log *slog.Logger, projectRoot string, previous, produced []string) error {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return err
	}

	current := make(map[string]struct{}, len(produced))
	for _, f := range produced {
		current[f] = struct{}{}
	}

	// Sorted, so a run that removes several files logs them in an order that is a
	// function of the file set rather than of the record's layout.
	stale := make([]string, 0, len(previous))
	for _, rel := range previous {
		if _, ok := current[rel]; ok {
			continue
		}
		stale = append(stale, rel)
	}
	slices.Sort(stale)

	var emptied []string
	for _, rel := range stale {
		path, err := safeOutputPath(root, rel)
		if err != nil {
			return fmt.Errorf("refusing to remove the recorded output %q: %w", rel, err)
		}

		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			// Already gone — removed by hand, or by an earlier run that got this far
			// and then failed. The record catching up is all that is left to do.
			continue
		}
		if err != nil {
			return fmt.Errorf("failed to inspect the stale generated file %q: %w", rel, err)
		}
		if !info.Mode().IsRegular() {
			log.WarnContext(ctx, "leaving a recorded output that is no longer a regular file",
				slog.String("path", rel),
				slog.String("mode", info.Mode().String()),
			)
			continue
		}

		if err := os.Remove(path); err != nil {
			return fmt.Errorf("failed to remove the stale generated file %q: %w", rel, err)
		}
		log.InfoContext(ctx, "removed a stale generated file", slog.String("path", rel))
		emptied = append(emptied, filepath.Dir(path))
	}

	slices.Sort(emptied)
	for _, dir := range slices.Compact(emptied) {
		removeEmptyDirs(root, dir)
	}
	return nil
}

// removeEmptyDirs removes dir and every ancestor of it below root that pruning
// left empty.
//
// A generated file removed from a directory it was the only occupant of would
// otherwise leave the directory behind — and planMerge already refuses to
// materialize an empty directory a generator made, so an empty one in the tree
// is something a person then has to wonder about.
//
// Best effort, and deliberately not an error: os.Remove on a directory that
// still holds something fails, which is exactly the signal to stop climbing, and
// a directory that could not be removed is untidiness rather than a failed run.
// os.Remove rather than os.RemoveAll for the same reason — nothing a person put
// in one of these directories is removed as a side effect.
func removeEmptyDirs(root, dir string) {
	prefix := root + string(filepath.Separator)
	for dir != root && strings.HasPrefix(dir, prefix) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
