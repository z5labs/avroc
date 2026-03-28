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
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/z5labs/avroc/internal/avrocpb"
	"github.com/z5labs/avroc/internal/cli"

	"github.com/sourcegraph/conc/pool"
	"github.com/z5labs/avro-go/idl"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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

	for name, _ := range generators {
		name := strings.TrimPrefix(name, "avroc-gen-") + "_out"

		flags.String(name, "", fmt.Sprintf("Output directory for the %q generator", name))
	}

	err = flags.Parse(cli.Args)
	if errors.Is(err, flag.ErrHelp) {
		err = printHelp(os.Stdout, slices.Collect(maps.Keys(generators))...)
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

	schemas := make([]*avrocpb.Schema, len(args))
	for i, arg := range args {
		f, err := parseIDL(arg)
		if err != nil {
			cli.Log.ErrorContext(ctx, "failed to parse IDL file", slog.String("file", arg), slog.Any("error", err))
			return 1
		}
		if f.Schema == nil {
			cli.Log.ErrorContext(ctx, "IDL file does not contain a schema", slog.String("file", arg))
			return 1
		}

		schema, err := mapToProtoSchema(f.Schema)
		if err != nil {
			cli.Log.ErrorContext(ctx, "failed to map IDL file to proto schema", slog.String("file", arg), slog.Any("error", err))
			return 1
		}

		schemas[i] = schema
	}

	pool := pool.New().WithContext(ctx)
	flags.Visit(func(f *flag.Flag) {
		pool.Go(func(ctx context.Context) error {
			flagName := strings.TrimSuffix(f.Name, "_out")
			generatorName := "avroc-gen-" + flagName

			executablePath, ok := generators[generatorName]
			if !ok {
				cli.Log.ErrorContext(ctx, "no generator found for flag", slog.String("flag", f.Name))
				return nil
			}

			g := generator{
				log:            cli.Log,
				name:           generatorName,
				executablePath: executablePath,
			}

			return g.generate(ctx, f.Value.String(), schemas...)
		})
	})

	err = pool.Wait()
	if err != nil {
		cli.Log.ErrorContext(ctx, "failed to run generators", slog.Any("error", err))
		return 1
	}

	return 0
}

func lookupGenerators(root fs.FS, dirs ...string) (map[string]string, error) {
	generatorIndex := make(map[string]string)

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

			generatorIndex[name] = path
			return filepath.SkipAll
		})
		if err != nil {
			return nil, err
		}
	}

	return generatorIndex, nil
}

func printHelp(w io.Writer, generators ...string) error {
	_, err := fmt.Fprintln(w, "Usage: avroc [options] <idl files>")
	if err != nil {
		return err
	}

	_, err = fmt.Fprintln(w, "Options:")
	if err != nil {
		return err
	}

	for _, gen := range generators {
		name := strings.TrimPrefix(gen, "avroc-gen-")

		_, err = fmt.Fprintf(w, "  -%s_out string\n", name)
		if err != nil {
			return err
		}

		_, err = fmt.Fprintf(w, "        Output directory for the %q generator\n", name)
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

type generator struct {
	log            *slog.Logger
	name           string
	executablePath string
}

func (g generator) generate(ctx context.Context, output string, schemas ...*avrocpb.Schema) error {

	socketFile, err := os.CreateTemp(g.name, "*.sock")
	if err != nil {
		g.log.ErrorContext(ctx, "failed to create temporary socket file", slog.String("generator", g.name), slog.Any("error", err))
		return err
	}
	defer func() {
		err = errors.Join(err, os.Remove(socketFile.Name()))
	}()

	cmd, err := startGenerator(ctx, g.executablePath, socketFile.Name())
	if err != nil {
		g.log.ErrorContext(ctx, "failed to start generator", slog.String("generator", g.name), slog.Any("error", err))
		return err
	}
	defer func() {
		err = errors.Join(err, cmd.Wait())
	}()
	defer func() {
		err = errors.Join(err, cmd.Cancel())
	}()

	cc, err := grpc.NewClient("unix://"+socketFile.Name(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, cc.Close())
	}()

	client := avrocpb.NewGeneratorClient(cc)
	resp, err := client.Generate(ctx, &avrocpb.GenerateRequest{
		OutputDirectory: &output,
		Schemas:         schemas,
	})
	if err != nil {
		g.log.ErrorContext(ctx, "failed to generate code", slog.String("generator", g.name), slog.Any("error", err))
		return err
	}

	g.log.InfoContext(ctx, "generated output", slog.String("generator", g.name), slog.Any("output_files", resp.GetOutputFiles()))
	return nil
}

func startGenerator(ctx context.Context, executable, socket string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, executable, socket)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Start()
	if err != nil {
		return nil, err
	}

	return cmd, nil
}

func mapToProtoSchema(schema *idl.Schema) (*avrocpb.Schema, error) {
	return new(avrocpb.Schema), nil
}
