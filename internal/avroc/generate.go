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
	"runtime"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/cli"

	"github.com/sourcegraph/conc/pool"
	"github.com/z5labs/avro-go/idl"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
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
//
// It is also where the run's root span starts and ends, which is the one place
// the tracer is handed in rather than taken from the context: every phase below
// inherits its provider from this span (see startSpan). The exit status and the
// span's status say the same thing about the same run — a run that returns 1
// leaves a span marked with the error that produced it, and a run somebody
// interrupted leaves one marked cancelled.
func runGenerate(ctx context.Context, cli cli.Context, tracer trace.Tracer) int {
	ctx, span := tracer.Start(ctx, spanRun)

	var err error
	defer func() {
		endSpan(ctx, span, err)
	}()

	if err = generate(ctx, cli); err != nil {
		return 1
	}
	return 0
}

// generate is the run itself, phase by phase, each of them reported as it fails
// and returned to runGenerate to become the exit status and the root span's.
func generate(ctx context.Context, cli cli.Context) error {
	path, ok := cli.Env.LookupEnv("PATH")
	if !ok {
		err := errors.New("PATH environment variable not set")
		cli.Log.ErrorContext(ctx, "unable to lookup generators", slog.Any("error", err))
		return err
	}

	generators, err := lookupGenerators(ctx, cli.Log, cli.OpenDir, filepath.SplitList(path)...)
	if err != nil {
		cli.Log.ErrorContext(ctx, "failed to lookup generators", slog.Any("error", err))
		return err
	}

	// The span is started and ended around the call rather than inside
	// loadManifest, which takes no context and does one filesystem read: a phase
	// that cannot be cancelled has no business growing a context parameter for a
	// span's sake.
	_, manifestSpan := startSpan(ctx, spanManifestLoad)
	manifest, err := loadManifest(cli, cli.WorkingDir)
	endSpan(ctx, manifestSpan, err)
	if err != nil {
		cli.Log.ErrorContext(ctx, "failed to load manifest", slog.Any("error", err))
		return err
	}

	tasks, err := planGenerators(ctx, manifest, generators, cli.WorkingDir)
	if err != nil {
		cli.Log.ErrorContext(ctx, "failed to plan generation", slog.Any("error", err))
		return err
	}

	// docs/plugin/SPEC.md's capability handshake, and it runs before the pool
	// rather than inside it: a plugin too old for this avroc's IR must fail the
	// run with nothing written, not after some other generator has already
	// produced output the user now has to work out whether to keep.
	if err := checkGenerators(ctx, cli.Log, tasks); err != nil {
		cli.Log.ErrorContext(ctx, "generator capability handshake failed", slog.Any("error", err))
		return err
	}

	if err := generateAll(ctx, cli.Log, cli.WorkingDir, tasks); err != nil {
		cli.Log.ErrorContext(ctx, "failed to run generators", slog.Any("error", err))
		return err
	}

	return nil
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

	// Bounded, and the bound is the machine's rather than the manifest's — see
	// maxConcurrentGenerators. What it changes is which generator runs when, and
	// nothing else: every task submitted here runs to completion whatever the
	// bound, and each stores its result at its own index, so the order the plans
	// are merged and reported in is still the manifest's.
	genPool := pool.New().WithMaxGoroutines(maxConcurrentGenerators()).WithContext(ctx)
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

	if err := mergeOutputs(ctx, projectRoot, outs); err != nil {
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

// maxConcurrentGenerators is how many generators generateAll runs at once.
//
// Unbounded, the fan-out is whatever avroc.json declares: planGenerators emits
// one task per generators[] entry, and every one of them is a fork, an exec and
// in the worst case a compiler-sized working set. That makes the peak load a
// property of a file a user wrote rather than of the machine avroc is running
// on, and a manifest with thirty generators on a two-core runner is not a
// configuration avroc should discover by being killed on it.
//
// GOMAXPROCS(0) rather than NumCPU(), because avroc ships as a container image
// (docs/container/SPEC.md). Since Go 1.25 the runtime derives GOMAXPROCS from
// the cgroup CPU limit whenever that is lower than the logical CPU count, so it
// is the figure that reflects a quota and the one an operator can override from
// the environment. NumCPU reports the host's cores and would over-subscribe
// precisely the constrained runner this bound exists to protect.
//
// Nothing about the contract moves. docs/plugin/SPEC.md's "Also out of scope"
// already reserves this decision — how many generators avroc runs at once is
// avroc's, and a plugin MUST NOT depend on being run before, after or alongside
// another — so this keeps a promise already made deliberately rather than by
// accident. Determinism is untouched for a different reason: that rule
// constrains generated bytes, and scheduling produces none.
func maxConcurrentGenerators() int {
	// Clamped because a bound of zero would run no generator at all, and conc
	// panics on one. Whatever the runtime reports, one generator at a time is
	// still a run.
	return max(1, runtime.GOMAXPROCS(0))
}

// planGenerators resolves every manifest generator into a runnable genTask. It
// is derived entirely from the manifest, the discovered generators, and the
// working directory — no generator subprocesses are spawned — so it can be
// unit-tested in isolation. It does read and parse the declared input IDL files
// from disk; each input is parsed at most once even when shared across
// generators — and so is traced at most once, because the cache is what decides
// whether there was any work to observe.
func planGenerators(ctx context.Context, m *Manifest, generators map[string]string, workingDir string) ([]genTask, error) {
	schemaCache := make(map[string]*avrocpb.Schema)

	tasks := make([]genTask, 0, len(m.Generators))
	for _, g := range m.Generators {
		execName := "avroc-gen-" + g.Name
		executablePath, ok := generators[execName]
		if !ok {
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
				loaded, err := loadSchema(ctx, in, resolved)
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
//
// The two halves carry a span each, because they are the two phases the story
// names: reading an IDL file and resolving the IR it describes are different
// work over the same input, and a project that is slow in one is not slow in the
// other. Both name the input as the manifest wrote it rather than as it resolved
// on this machine, so a trace reads like avroc.json.
func loadSchema(ctx context.Context, input, path string) (*avrocpb.Schema, error) {
	schema, err := parseInput(ctx, input, path)
	if err != nil {
		return nil, err
	}
	return resolveInput(ctx, input, schema)
}

// parseInput is loadSchema's first phase: the IDL file read off disk and parsed.
func parseInput(ctx context.Context, input, path string) (_ *idl.Schema, err error) {
	_, span := startSpan(ctx, spanIDLParse, trace.WithAttributes(attribute.String(attrInput, input)))
	defer func() {
		endSpan(ctx, span, err)
	}()

	f, err := parseIDL(path)
	if err != nil {
		return nil, fmt.Errorf("failed to parse IDL file: %w", err)
	}
	if f.Schema == nil {
		return nil, errors.New("IDL file does not contain a schema")
	}
	return f.Schema, nil
}

// resolveInput is loadSchema's second phase: the parsed schema validated and
// resolved into the IR a generator is handed.
//
// Validation is inside this span rather than beside it. It is the first half of
// resolving — the questions resolve.go would otherwise have to ask again — and a
// third span for it would be a boundary in the trace that is not a boundary in
// the code.
func resolveInput(ctx context.Context, input string, schema *idl.Schema) (_ *avrocpb.Schema, err error) {
	_, span := startSpan(ctx, spanIRResolve, trace.WithAttributes(attribute.String(attrInput, input)))
	defer func() {
		endSpan(ctx, span, err)
	}()

	if err := validateSchema(schema); err != nil {
		return nil, fmt.Errorf("schema validation failed: %w", err)
	}
	return resolveSchema(schema)
}
