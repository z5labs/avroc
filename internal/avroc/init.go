// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/z5labs/avroc/internal/cli"
)

// runInit scaffolds a starter avroc.json under the working directory. It never
// clobbers an existing manifest: if one is present it reports that and exits 0.
func runInit(ctx context.Context, cli cli.Context) int {
	if _, err := fs.Stat(cli.OpenDir(cli.WorkingDir), manifestFilename); err == nil {
		fmt.Fprintf(os.Stdout, "%s already exists; not overwriting\n", manifestFilename)
		return 0
	} else if !errors.Is(err, fs.ErrNotExist) {
		cli.Log.ErrorContext(ctx, "failed to check for existing manifest", slog.Any("error", err))
		return 1
	}

	data, err := marshalManifest(scaffoldManifest())
	if err != nil {
		cli.Log.ErrorContext(ctx, "failed to render manifest", slog.Any("error", err))
		return 1
	}

	dst := filepath.Join(cli.WorkingDir, manifestFilename)
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		cli.Log.ErrorContext(ctx, "failed to write manifest", slog.String("path", dst), slog.Any("error", err))
		return 1
	}

	fmt.Fprintf(os.Stdout, "created %s\n", manifestFilename)
	return 0
}

// scaffoldManifest is the starter manifest avroc init writes: a single Go
// generator over one example input, ready for the user to edit.
func scaffoldManifest() *Manifest {
	return &Manifest{
		Inputs: []string{"schema.avdl"},
		Generators: []GeneratorConfig{
			{
				Name:    "go",
				Source:  "ghcr.io/z5labs/avroc-gen-go",
				Version: "v0.1.0",
				Out:     "gen",
				Options: map[string]string{"package_name": "models"},
			},
		},
	}
}
