// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"context"
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

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
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
//
// Since #218 it has a neighbour: newDescriptorDir creates this invocation's
// descriptor directory in the same tree, on the same terms and for a different
// reason. The two are siblings and never nested, so a generator walking its own
// --out cannot reach the descriptor and nothing avroc merges, prunes or records
// can reach either.
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

// generatorOutput is what one generator produced, after a zero exit and before
// anything of it has moved: every file resolved to its destination, still in the
// scratch directory the generator wrote it into.
//
// It is the value a merge is decided from. A plan is cheap to hold and has
// touched nothing in the project tree, which is what lets avroc collect one per
// generator and check them against each other before the first rename (#118).
type generatorOutput struct {
	// generator is the executable name, avroc-gen-<name>, that a report names.
	generator string
	// output is the project output tree this generator's files merge into,
	// absolute. Two generators may or may not share one.
	output string
	// scratch is the private directory the generator wrote into. It still holds
	// every file in files, and removing it belongs to whoever holds this plan.
	scratch string
	files   []mergedFile
}

// mergeOutputs moves everything every generator left in its scratch directory
// into the project's output tree.
//
// It is called only for generators that exited zero, which is the whole of the
// success signal (docs/plugin/SPEC.md, "Exit codes and diagnostics"): a failed
// invocation's scratch directory is discarded instead, so nothing a failing
// generator left behind reaches the project.
//
// The merge is in phases, and the split is the point of it. Nothing is written
// into the project tree until every file of every generator has been resolved
// (planMerge) and checked against every other generator's (checkCollisions), so
// a path a generator should not have produced, a path two of them both produced,
// or a directory that cannot be created fails the run before a single file has
// been moved — nothing is adopted as output, and no existing file in the tree is
// replaced. The directory creation is the one thing those phases write, so a
// refused merge can leave an empty directory behind; what it cannot leave is a
// file. The last phase then does nothing but rename, which is atomic per file —
// so a run interrupted mid-merge leaves whole files where it got to and never a
// half-written one, and the window in which it can be interrupted at all is a
// sequence of metadata operations rather than a copy of every byte the
// generators produced.
//
// The plans are merged in the order they are given, which is the manifest's
// rather than the order the generators finished in. Removing a file an earlier
// run produced and this one did not is pruneStale's, and it runs after this
// returns: nothing is removed from the tree until everything this run produced
// is in it (#119).
func mergeOutputs(ctx context.Context, projectRoot string, outs []*generatorOutput) (err error) {
	ctx, span := startSpan(ctx, spanMerge)
	defer func() {
		endSpan(ctx, span, err)
	}()

	// Recorded on this span rather than on any generator's, because a collision
	// is a fact about the whole set of plans: the generators that claimed a path
	// each did nothing wrong on their own, and neither of their spans is where a
	// person would look for what the two of them did together. The events are
	// added in the order the refusal reports them, so the trace and the message a
	// user sees name the same paths in the same order.
	if collisions := findCollisions(outs); len(collisions) > 0 {
		for _, c := range collisions {
			span.AddEvent(eventCollision, trace.WithAttributes(
				attribute.String(attrPath, c.path),
				attribute.StringSlice(attrGenerators, c.generators),
			))
		}
		return collisionError(collisions)
	}
	if err := checkReservedPaths(projectRoot, outs); err != nil {
		return err
	}

	for _, out := range outs {
		for _, f := range out.files {
			if err := os.MkdirAll(filepath.Dir(f.dst), 0o755); err != nil {
				return fmt.Errorf("generator %q: failed to create the output directory for %q: %w", out.generator, f.rel, err)
			}
		}
	}

	for _, out := range outs {
		for _, f := range out.files {
			if err := moveIntoPlace(f.src, f.dst); err != nil {
				return fmt.Errorf("generator %q: failed to merge %q into the output directory: %w", out.generator, f.rel, err)
			}
		}
	}
	return nil
}

// collision is one destination path more than one generator produced: the path,
// and every generator that claimed it, sorted by name.
//
// It is a value rather than a formatted string because the same fact is said
// twice — once to the person whose run was refused and once on the merge span —
// and two renderings computed from one value cannot disagree about which
// generators collided or in what order.
type collision struct {
	path       string
	generators []string
}

// findCollisions reports every destination path two or more generators produced.
//
// avroc owns the project's output tree, so this is avroc's to detect: the
// generators cannot see each other's directories, they are told not to try
// (docs/plugin/SPEC.md, "What a plugin does not own"), and without the check the
// tree would hold whichever of the two was renamed last — a last-writer-wins
// whose winner is the order two concurrent processes happened to finish in, so
// the same unchanged inputs would produce different output on different runs.
//
// A collision is decided from the destination path rather than from the path
// relative to a generator's own output directory, because two generators need
// not share one: a generator writing "pkg/user.avsc" under the project root and
// one writing "user.avsc" under "pkg/" collide, and only the resolved
// destination says so. That is also why the report names the absolute path — it
// is the one name both generators' output is described by.
//
// The result is a function of the plans and of nothing else. Every claim is
// collected before anything is decided, the colliding paths are sorted, and the
// generators claiming each one are sorted by name, so two runs over unchanged
// inputs produce the identical report — and the identical events on the merge
// span — however the generators were ordered in the manifest and whichever of
// them finished first.
func findCollisions(outs []*generatorOutput) []collision {
	claimants := make(map[string][]string)
	var collided []string
	for _, out := range outs {
		for _, f := range out.files {
			claimants[f.dst] = append(claimants[f.dst], out.generator)
			// Recorded as the path stops being claimed by one generator alone, so
			// that a third claimant does not record it a second time.
			if len(claimants[f.dst]) == 2 {
				collided = append(collided, f.dst)
			}
		}
	}
	if len(collided) == 0 {
		return nil
	}

	slices.Sort(collided)
	collisions := make([]collision, 0, len(collided))
	for _, dst := range collided {
		names := slices.Clone(claimants[dst])
		slices.Sort(names)
		collisions = append(collisions, collision{path: dst, generators: names})
	}
	return collisions
}

// collisionError is what a run refused for a collision fails with: every
// colliding path, in the order findCollisions fixed, naming every generator that
// claimed it.
//
// The path it names is the absolute destination rather than either generator's
// relative one, because two generators need not share an output directory: one
// writing "pkg/user.avsc" under the project root and one writing "user.avsc"
// under "pkg/" collide, and only the resolved destination says so.
func collisionError(collisions []collision) error {
	reports := make([]string, 0, len(collisions))
	for _, c := range collisions {
		reports = append(reports, fmt.Sprintf("%q is produced by generators %s", c.path, quotedList(c.generators)))
	}
	return fmt.Errorf("refusing to merge: %s", strings.Join(reports, "; "))
}

// checkReservedPaths refuses a run in which a generator produces avroc's own
// record of what the run generated.
//
// avroc.gen.json at the project root is avroc's file (#119), and it is rewritten
// after every successful merge. A generator producing it would have its output
// silently overwritten moments later, and the record avroc then read on the next
// run would be a file two writers disagree about — so the collision is refused
// for the same reason two generators claiming one path is, and in the same phase,
// before anything has been written where a person would find it.
//
// It is checked here rather than in planMerge because a plan is built against one
// generator's output directory, and the reserved path is a property of the
// project root: a generator whose --out is a subdirectory cannot reach it at all,
// and one writing into the project root itself can.
func checkReservedPaths(projectRoot string, outs []*generatorOutput) error {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return err
	}
	reserved := filepath.Join(root, outputRecordFilename)

	for _, out := range outs {
		for _, f := range out.files {
			if f.dst == reserved {
				return fmt.Errorf("refusing to merge: generator %q produces %q, which is %q — avroc's own record of what the run generated", out.generator, f.rel, reserved)
			}
		}
	}
	return nil
}

// quotedList renders names as a quoted list: "a" and "b" for two, "a", "b" and
// "c" for more.
func quotedList(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	switch len(quoted) {
	case 0:
		return ""
	case 1:
		return quoted[0]
	default:
		return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
	}
}

// relPaths names every file a plan holds, relative to the output tree and
// slash-separated: what a successful merge of it moved, in the order it moved
// them.
func relPaths(files []mergedFile) []string {
	rel := make([]string, 0, len(files))
	for _, f := range files {
		rel = append(rel, f.rel)
	}
	return rel
}

// planMerge resolves every file in a scratch directory to its destination in the
// output tree, refusing anything a generator is not allowed to have produced.
//
// It writes nothing: a plan can be built for every generator and then thrown
// away — which is what a collision between two of them does (#118) — with the
// project tree exactly as the run found it.
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
