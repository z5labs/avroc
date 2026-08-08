// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"context"
	"fmt"
	"log/slog"
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

	genPool := pool.New().WithContext(ctx)
	for _, task := range tasks {
		genPool.Go(func(ctx context.Context) error {
			g := generator{
				log:            cli.Log,
				name:           task.name,
				executablePath: task.executablePath,
			}
			return g.generate(ctx, task.output, task.options, task.schemas...)
		})
	}

	if err := genPool.Wait(); err != nil {
		cli.Log.ErrorContext(ctx, "failed to run generators", slog.Any("error", err))
		return 1
	}

	return 0
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
