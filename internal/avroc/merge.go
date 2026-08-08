// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// newScratchDir creates the private, empty directory one generator invocation
// writes into, and which its --out names.
//
// docs/plugin/SPEC.md's "The output directory" is what this implements: the
// directory is avroc's, made for this invocation and this generator alone, and
// a plugin MAY assume it exists, is writable and is empty. That emptiness is the
// whole mechanism behind the merge — the set of files a run produced is exactly
// the set found in the directory afterwards, with no marker inside a file and no
// bookkeeping asked of the plugin.
//
// It is created *inside* the project's output tree rather than under TMPDIR, so
// that every file in it is already on the same filesystem as its destination.
// That is what lets mergeOutput move each file with a rename rather than a copy,
// which is in turn what keeps the merge short enough to be hard to interrupt.
// The leading dot keeps it out of a shell glob for the moment it exists.
func newScratchDir(output, generatorName string) (string, error) {
	dir, err := os.MkdirTemp(output, "."+generatorName+"-out-")
	if err != nil {
		return "", fmt.Errorf("failed to create scratch directory: %w", err)
	}
	return dir, nil
}

// mergedFile is one file a merge will move: where the generator left it, where
// it is going, and the slash-separated path relative to the output tree that
// names it in a report.
type mergedFile struct {
	rel string
	src string
	dst string
}

// mergeOutput moves everything a generator left in its scratch directory into
// the project's output tree and reports what it moved, each path relative to
// that tree and slash-separated.
//
// It is called only after a zero exit, which is the whole of the success signal
// (docs/plugin/SPEC.md, "Exit codes and diagnostics"): a failed invocation's
// scratch directory is discarded instead, so nothing a failing generator left
// behind reaches the project.
//
// The merge is in two phases, and the split is the point of it. The first
// resolves every file and creates every directory the second will need, so that
// a path the generator should not have produced, or a directory that cannot be
// created, fails the run before a single file has been moved — nothing is
// adopted as output, and no existing file in the tree is replaced. The directory
// creation is part of that phase and is the one thing it does write, so a
// refused merge can leave an empty directory behind; what it cannot leave is a
// file. The second phase then does nothing but rename, which is atomic per file
// — so a run interrupted mid-merge leaves whole files where it got to and never
// a half-written one, and the window in which it can be interrupted at all is a
// sequence of metadata operations rather than a copy of every byte the generator
// produced.
//
// Detecting two generators that produced the same path is #118's and is not
// done here; nor is removing a file an earlier run produced and this one did
// not, which is #119's.
func mergeOutput(scratch, output string) ([]string, error) {
	files, err := planMerge(scratch, output)
	if err != nil {
		return nil, err
	}

	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(f.dst), 0o755); err != nil {
			return nil, fmt.Errorf("failed to create the output directory for %q: %w", f.rel, err)
		}
	}

	merged := make([]string, 0, len(files))
	for _, f := range files {
		if err := moveIntoPlace(f.src, f.dst); err != nil {
			return nil, fmt.Errorf("failed to merge %q into the output directory: %w", f.rel, err)
		}
		merged = append(merged, f.rel)
	}
	return merged, nil
}

// planMerge resolves every file in a scratch directory to its destination in the
// output tree, refusing anything a generator is not allowed to have produced.
//
// A directory that holds no files contributes nothing: a generator that made one
// produced no output there, and materializing it in the project tree would leave
// a directory a person then has to wonder about.
func planMerge(scratch, output string) ([]mergedFile, error) {
	var files []mergedFile

	err := filepath.WalkDir(scratch, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == scratch {
			return nil
		}

		rel, err := filepath.Rel(scratch, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)

		if d.IsDir() {
			return nil
		}
		// WalkDir does not follow symbolic links, so a link the generator
		// planted arrives here as a link rather than as the thing it points at
		// — including one to a directory it then wrote through, which is the
		// escape a "..-free relative path" check on its own does not see.
		// Refusing it is avroc enforcing the boundary rather than trusting it:
		// avroc cannot stop a generator writing to /etc with its own
		// privileges, but it can refuse to carry the result into the project.
		if !d.Type().IsRegular() {
			return fmt.Errorf("refusing to merge %q: a generator may produce only regular files and directories, and this is %s", name, entryKind(d.Type()))
		}

		dst, err := safeOutputPath(output, name)
		if err != nil {
			return fmt.Errorf("refusing to merge %q: %w", name, err)
		}
		files = append(files, mergedFile{rel: name, src: p, dst: dst})
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Sorted, so that both the report and the order a merge would be
	// interrupted in are a function of the file set rather than of the order
	// the filesystem happened to hand the directory over in.
	slices.SortFunc(files, func(a, b mergedFile) int { return strings.Compare(a.rel, b.rel) })
	return files, nil
}

// entryKind names what a non-regular directory entry is, so that the refusal
// says which of the several ways a generator can go wrong it went.
func entryKind(mode fs.FileMode) string {
	switch {
	case mode&fs.ModeSymlink != 0:
		return "a symbolic link"
	case mode&fs.ModeNamedPipe != 0:
		return "a named pipe"
	case mode&fs.ModeSocket != 0:
		return "a socket"
	case mode&fs.ModeDevice != 0:
		return "a device"
	default:
		return fmt.Sprintf("of mode %s", mode)
	}
}

// moveIntoPlace moves one generated file to dst, replacing whatever was there.
//
// A rename is atomic, so dst is the file the previous run left or the file this
// one produced and never a mixture of the two. newScratchDir keeps both sides on
// one filesystem so that this is the path taken; a mount point nested inside the
// output tree can still put them apart, and the copy that covers that case
// stages into dst's own directory and renames, so dst still appears whole or not
// at all.
func moveIntoPlace(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	return copyIntoPlace(src, dst)
}

// copyIntoPlace is moveIntoPlace's cross-filesystem case: copy src into a
// temporary file beside dst, rename that into place, and remove src — so that a
// merge is a move on both sides of a mount point and mergeOutput's scratch
// directory is empty afterwards either way.
func copyIntoPlace(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		_ = in.Close()
	}()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".avroc-")
	if err != nil {
		return err
	}
	staged := tmp.Name()

	if err := stage(tmp, in, info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		_ = os.Remove(staged)
		return err
	}
	if err := os.Rename(staged, dst); err != nil {
		_ = os.Remove(staged)
		return err
	}

	// Best effort, and deliberately not an error: dst is already the file the
	// generator produced, so the merge of it has succeeded, and failing the run
	// now over a leftover in a directory the caller removes wholesale would
	// report a problem the user cannot act on. Closed first, because a source
	// still open is a source some filesystems will not unlink.
	_ = in.Close()
	_ = os.Remove(src)
	return nil
}

// stage writes src's contents and mode into the already-created temporary file
// and closes it, so that a rename of it is a rename of the finished file.
func stage(tmp *os.File, src io.Reader, mode fs.FileMode) error {
	if _, err := io.Copy(tmp, src); err != nil {
		return err
	}
	// The mode a generator gave the file it produced, not the one CreateTemp
	// chose: an executable a generator emitted stays executable.
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	return tmp.Close()
}

// safeOutputPath resolves a generator-produced relative path against the output
// root, rejecting anything that is absolute or escapes the root. The supplied
// path uses forward slashes regardless of OS. It returns the OS-native absolute
// path to write to.
//
// docs/plugin/SPEC.md puts the requirement on the plugin — every file beneath
// --out, not through "..", not through an absolute path — and this is avroc
// enforcing it rather than trusting it, so an escape is a failed run. It is
// checked on the merge side rather than only inside internal/plugin because a
// third-party generator imports nothing from this repository, and the boundary
// has to hold for one of those too.
func safeOutputPath(root, p string) (string, error) {
	if p == "" {
		return "", errors.New("path is empty")
	}
	if path.IsAbs(p) {
		return "", fmt.Errorf("path %q is absolute", p)
	}

	osPath := filepath.FromSlash(p)
	if filepath.IsAbs(osPath) || filepath.VolumeName(osPath) != "" {
		return "", fmt.Errorf("path %q is absolute", p)
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}

	full := filepath.Join(rootAbs, osPath)
	rel, err := filepath.Rel(rootAbs, full)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the output directory", p)
	}

	return full, nil
}
