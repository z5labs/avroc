// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/z5labs/avro-go/idl"
	"github.com/z5labs/avroc/internal/cli"
)

func Main(ctx context.Context, cli cli.Context) int {
	path, ok := cli.Env.LookupEnv("PATH")
	if !ok {
		cli.Log.ErrorContext(
			ctx,
			"unable to lookup generators",
			slog.Any("error", "PATH environment variable not set"),
		)
		return 1
	}

	paths := strings.Split(path, ":")
	generators, err := lookupGenerators(cli.Fs, paths...)
	if err != nil {
		cli.Log.ErrorContext(ctx, "failed to lookup generators", slog.Any("error", err))
		return 1
	}

	flags := flag.NewFlagSet("avroc", flag.ContinueOnError)
	flags.Usage = func() {}

	for _, gen := range generators {
		flags.String(gen.name+"_out", "", fmt.Sprintf("Output directory for the %q generator", gen.name))
	}

	err = flags.Parse(cli.Args)
	if errors.Is(err, flag.ErrHelp) {
		err = printHelp(os.Stdout, generators...)
		if err != nil {
			cli.Log.ErrorContext(ctx, "failed to print help", slog.Any("error", err))
			return 1
		}
		return 0
	}
	if err != nil {
		cli.Log.ErrorContext(ctx, "failed to parse flags", slog.Any("error", err))
		return 1
	}

	args := flags.Args()
	if len(args) == 0 {
		cli.Log.ErrorContext(ctx, "no IDL files provided")
		return 1
	}

	files := make([]*idl.File, len(args))
	for i, arg := range args {
		f, err := parseIDL(arg)
		if err != nil {
			cli.Log.ErrorContext(ctx, "failed to parse IDL file", slog.String("file", arg), slog.Any("error", err))
			return 1
		}

		files[i] = f
	}
	cli.Log.InfoContext(ctx, "parsed idl files", slog.Int("num_of_files", len(files)))

	return 0
}

type generator struct {
	name         string
	absolutePath string
}

func lookupGenerators(root fs.FS, dirs ...string) ([]generator, error) {
	generatorIndex := make(map[generator]struct{})

	for _, dir := range dirs {
		dir = strings.TrimPrefix(dir, "/")

		err := fs.WalkDir(root, dir, func(path string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}

			name := d.Name()
			if d.IsDir() || !strings.HasPrefix(name, "avroc-gen-") {
				return nil
			}

			gen := generator{
				name:         strings.TrimPrefix(name, "avroc-gen-"),
				absolutePath: path,
			}
			generatorIndex[gen] = struct{}{}
			return filepath.SkipAll
		})
		if err != nil {
			return nil, err
		}
	}

	generators := make([]generator, 0, len(generatorIndex))
	for gen := range maps.All(generatorIndex) {
		generators = append(generators, gen)
	}

	return generators, nil
}

func printHelp(w io.Writer, generators ...generator) error {
	_, err := fmt.Fprintln(w, "Usage: avroc [options] <idl files>")
	if err != nil {
		return err
	}

	_, err = fmt.Fprintln(w, "Options:")
	if err != nil {
		return err
	}

	for _, gen := range generators {
		_, err = fmt.Fprintf(w, "  -%s_out string\n", gen.name)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "        Output directory for the %q generator\n", gen.name)
		if err != nil {
			return err
		}
	}

	return nil
}

func parseIDL(path string) (_ *idl.File, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, f.Close())
	}()

	return idl.Parse(f)
}
