// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/cli"

	"github.com/sourcegraph/conc/pool"
)

// genTask is a single resolved unit of work: one generator to run with its
// output directory, options, and the schemas it should generate from.
type genTask struct {
	name           string // avroc-gen-<name>
	executablePath string
	output         string
	options        []*avrocpb.Option
	schemas        []*avrocpb.Schema
}

// runGenerate realizes the avroc.json manifest: it resolves each declared
// generator against the PATH-discovered executables, parses the declared input
// IDL files, and runs the generators concurrently, writing their streamed
// output under each generator's output directory.
func runGenerate(ctx context.Context, cli cli.Context) int {
	path, ok := cli.Env.LookupEnv("PATH")
	if !ok {
		cli.Log.ErrorContext(ctx, "unable to lookup generators", slog.Any("error", "PATH environment variable not set"))
		return 1
	}

	generators, err := lookupGenerators(ctx, cli.Log, cli.OpenDir, filepath.SplitList(path)...)
	if err != nil {
		cli.Log.ErrorContext(ctx, "failed to lookup generators", slog.Any("error", err))
		return 1
	}

	manifest, err := loadManifest(cli, cli.WorkingDir)
	if err != nil {
		cli.Log.ErrorContext(ctx, "failed to load manifest", slog.Any("error", err))
		return 1
	}

	tasks, err := planGenerators(manifest, generators, cli.WorkingDir)
	if err != nil {
		cli.Log.ErrorContext(ctx, "failed to plan generation", slog.Any("error", err))
		return 1
	}

	// docs/plugin/SPEC.md's capability handshake, and it runs before the pool
	// rather than inside it: a plugin too old for this avroc's IR must fail the
	// run with nothing written, not after some other generator has already
	// produced output the user now has to work out whether to keep.
	if err := checkGenerators(ctx, cli.Log, tasks); err != nil {
		cli.Log.ErrorContext(ctx, "generator capability handshake failed", slog.Any("error", err))
		return 1
	}

	if err := generateAll(ctx, cli.Log, cli.WorkingDir, tasks); err != nil {
		cli.Log.ErrorContext(ctx, "failed to run generators", slog.Any("error", err))
		return 1
	}

	return 0
}

// generateAll runs every planned generator and merges what they produced into
// the project's output tree.
//
// It is in two stages, and the boundary between them is #118's. Every generator
// runs concurrently, each into a private scratch directory, and its output is
// resolved there; only once all of them have finished does anything move into
// the project tree. That is what makes two generators producing the same path an
// error rather than a race: the collision is decided from the full set of plans,
// so the run fails with the same report whichever generator finished first, and
// it fails before either file has been written where a person would find it.
//
// The price is that a generator's output is not visible until the slowest
// generator has finished, which is the trade docs/plugin/SPEC.md already makes
// under "Scheduling" — a plugin sees a single invocation and depends on nothing
// about the others.
//
// It also makes the run all or nothing: one generator failing, or colliding with
// another, now discards what every other generator produced rather than leaving
// it in the tree. That is deliberate and is the same reason the capability
// handshake runs before the pool — a half-generated tree is worse than an
// ungenerated one, because a person then has to work out which half is which.
//
// The last stage is #119's: what the previous run recorded having generated and
// this one did not produce is removed, and the record is rewritten. Both are
// relative to projectRoot — the directory the manifest was read from — because
// the record spans every generator's output directory and outlives the manifest
// entry that produced any of them.
func generateAll(ctx context.Context, log *slog.Logger, projectRoot string, tasks []genTask) (err error) {
	// Read before the first generator starts, so a record avroc cannot make sense
	// of fails the run with nothing generated — the same reason the capability
	// handshake runs before the pool. Read afterwards, the only choices left would
	// be to leave the stale files behind forever or to remove paths avroc cannot
	// vouch for.
	previous, err := loadOutputRecord(projectRoot)
	if err != nil {
		return err
	}

	outs := make([]*generatorOutput, len(tasks))

	genPool := pool.New().WithContext(ctx)
	for i, task := range tasks {
		genPool.Go(func(ctx context.Context) error {
			g := generator{
				log:            log,
				name:           task.name,
				executablePath: task.executablePath,
			}
			out, runErr := g.run(ctx, task.output, task.options, task.schemas...)
			if runErr != nil {
				return runErr
			}
			// Stored at this generator's own index rather than appended, so that
			// the order the plans are merged and reported in is the manifest's and
			// not the order the generators happened to finish in.
			outs[i] = out
			return nil
		})
	}
	waitErr := genPool.Wait()

	// Every scratch directory that outlived its invocation, removed however this
	// returns: emptied by a successful merge, or still holding the output of a run
	// that failed or collided elsewhere. Registered after the wait, so that no
	// generator can still be writing into one.
	defer func() {
		for _, out := range outs {
			if out == nil {
				continue
			}
			err = errors.Join(err, os.RemoveAll(out.scratch))
		}
	}()

	if waitErr != nil {
		return waitErr
	}

	if err := mergeOutputs(projectRoot, outs); err != nil {
		log.ErrorContext(ctx, "failed to merge generated output", slog.Any("error", err))
		return err
	}

	produced, err := producedFiles(projectRoot, outs)
	if err != nil {
		log.ErrorContext(ctx, "failed to record generated output", slog.Any("error", err))
		return err
	}

	// Pruned before the record is rewritten, so that a removal that fails leaves a
	// record still naming the file: the run fails loudly, and the next one tries
	// again. Rewriting first would forget the stale file, which is the bug this
	// stage exists to fix.
	if err := pruneStale(ctx, log, projectRoot, previous.Files, produced); err != nil {
		log.ErrorContext(ctx, "failed to remove stale generated output", slog.Any("error", err))
		return err
	}

	if err := writeOutputRecord(ctx, log, projectRoot, produced); err != nil {
		log.ErrorContext(ctx, "failed to record generated output", slog.Any("error", err))
		return err
	}

	for _, out := range outs {
		log.InfoContext(ctx, "generated output",
			slog.String("generator", out.generator),
			slog.String("out", out.output),
			slog.Any("output_files", relPaths(out.files)),
		)
	}
	return nil
}

// planGenerators resolves every manifest generator into a runnable genTask. It
// is derived entirely from the manifest, the discovered generators, and the
// working directory — no generator subprocesses are spawned — so it can be
// unit-tested in isolation. It does read and parse the declared input IDL files
// from disk; each input is parsed at most once even when shared across
// generators.
func planGenerators(m *Manifest, generators map[string]string, workingDir string) ([]genTask, error) {
	schemaCache := make(map[string]*avrocpb.Schema)

	tasks := make([]genTask, 0, len(m.Generators))
	for _, g := range m.Generators {
		execName := "avroc-gen-" + g.Name
		executablePath, ok := generators[execName]
		if !ok {
			if g.Source != "" {
				return nil, fmt.Errorf("generator %q references OCI image %q but OCI execution is not yet supported (see #70); install %s on PATH to use it now", g.Name, g.Source, execName)
			}
			return nil, fmt.Errorf("no generator %q found on PATH", execName)
		}

		inputs := dedupInputs(m.Inputs, g.Inputs)
		if len(inputs) == 0 {
			return nil, fmt.Errorf("generator %q has no input IDL files", g.Name)
		}

		schemas := make([]*avrocpb.Schema, len(inputs))
		for i, in := range inputs {
			resolved := filepath.Join(workingDir, in)
			schema, ok := schemaCache[resolved]
			if !ok {
				loaded, err := loadSchema(resolved)
				if err != nil {
					return nil, fmt.Errorf("generator %q input %q: %w", g.Name, in, err)
				}
				schemaCache[resolved] = loaded
				schema = loaded
			}
			schemas[i] = schema
		}

		tasks = append(tasks, genTask{
			name:           execName,
			executablePath: executablePath,
			output:         filepath.Join(workingDir, g.Out),
			options:        g.options(),
			schemas:        schemas,
		})
	}

	return tasks, nil
}

// dedupInputs concatenates the shared and per-generator inputs, preserving the
// first occurrence order and dropping duplicates.
func dedupInputs(shared, own []string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, in := range append(append([]string{}, shared...), own...) {
		if _, ok := seen[in]; ok {
			continue
		}
		seen[in] = struct{}{}
		out = append(out, in)
	}
	return out
}

// loadSchema parses, validates, and resolves a single IDL file into the
// protobuf schema sent to a generator.
func loadSchema(path string) (*avrocpb.Schema, error) {
	f, err := parseIDL(path)
	if err != nil {
		return nil, fmt.Errorf("failed to parse IDL file: %w", err)
	}
	if f.Schema == nil {
		return nil, fmt.Errorf("IDL file does not contain a schema")
	}
	if err := validateSchema(f.Schema); err != nil {
		return nil, fmt.Errorf("schema validation failed: %w", err)
	}
	return resolveSchema(f.Schema)
}
