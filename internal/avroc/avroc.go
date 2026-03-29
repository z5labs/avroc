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
	"slices"
	"strings"
	"time"

	"github.com/z5labs/avroc/internal/avrocpb"
	"github.com/z5labs/avroc/internal/cli"

	"github.com/sourcegraph/conc/pool"
	"github.com/z5labs/avro-go/idl"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
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

	for name := range generators {
		generatorName := strings.TrimPrefix(name, "avroc-gen-")
		flagName := generatorName + "_out"

		flags.String(flagName, "", fmt.Sprintf("Output directory for the %q generator", generatorName))
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

		if err := validateSchema(f.Schema); err != nil {
			cli.Log.ErrorContext(ctx, "schema validation failed", slog.String("file", arg), slog.Any("error", err))
			return 1
		}

		schema, err := mapToProtoSchema(f.Schema)
		if err != nil {
			cli.Log.ErrorContext(ctx, "failed to map IDL file to proto schema", slog.String("file", arg), slog.Any("error", err))
			return 1
		}

		schemas[i] = schema
	}

	genPool := pool.New().WithContext(ctx)
	flags.Visit(func(f *flag.Flag) {
		genPool.Go(func(ctx context.Context) error {
			flagName := strings.TrimSuffix(f.Name, "_out")
			generatorName := "avroc-gen-" + flagName

			executablePath, ok := generators[generatorName]
			if !ok {
				cli.Log.ErrorContext(ctx, "no generator found for flag", slog.String("flag", f.Name))
				return nil
			}

			output := f.Value.String()
			if output == "" {
				return fmt.Errorf("empty output directory for generator %q", generatorName)
			}

			g := generator{
				log:            cli.Log,
				env:            cli.Env,
				name:           generatorName,
				executablePath: executablePath,
			}

			return g.generate(ctx, output, schemas...)
		})
	})

	err = genPool.Wait()
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
			if !strings.HasPrefix(name, "avroc-gen-") {
				return nil
			}

			info, infoErr := d.Info()
			if infoErr != nil {
				return nil
			}

			mode := info.Mode()
			if !mode.IsRegular() || mode&0o111 == 0 {
				return nil
			}

			generatorIndex[name] = "/" + path
			return nil
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
	env            cli.Environment
	name           string
	executablePath string
}

func (g generator) generate(ctx context.Context, output string, schemas ...*avrocpb.Schema) (err error) {
	socketFile, err := os.CreateTemp("", g.name+"-*.sock")
	if err != nil {
		g.log.ErrorContext(ctx, "failed to create temporary socket file", slog.String("generator", g.name), slog.Any("error", err))
		return err
	}
	socketPath := socketFile.Name()
	err = errors.Join(socketFile.Close(), os.Remove(socketPath))
	if err != nil {
		g.log.ErrorContext(ctx, "failed to prepare socket path", slog.String("generator", g.name), slog.Any("error", err))
		return err
	}
	defer func() {
		err = errors.Join(err, os.Remove(socketPath))
	}()

	cmd, err := g.startGenerator(ctx, socketPath)
	if err != nil {
		g.log.ErrorContext(ctx, "failed to start generator", slog.String("generator", g.name), slog.Any("error", err))
		return err
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	cc, err := grpc.NewClient(
		"unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
	)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, cc.Close())
	}()

	rpcCtx, rpcCancel := context.WithTimeout(ctx, 30*time.Second)
	defer rpcCancel()

	client := avrocpb.NewGeneratorClient(cc)
	resp, err := client.Generate(rpcCtx, &avrocpb.GenerateRequest{
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

func (g generator) startGenerator(ctx context.Context, socket string) (*exec.Cmd, error) {
	args := []string{socket}
	if extra, ok := g.env.LookupEnv("AVROC_GENERATOR_ARGS"); ok {
		args = append(strings.Fields(extra), args...)
	}

	cmd := exec.CommandContext(ctx, g.executablePath, args...)
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
	t, err := mapToProtoType(schema.Type)
	if err != nil {
		return nil, err
	}

	types := make([]*avrocpb.Type, len(schema.Types))
	for i, st := range schema.Types {
		types[i], err = mapToProtoType(st)
		if err != nil {
			return nil, err
		}
	}

	return &avrocpb.Schema{
		Namespace: proto.String(schema.Namespace),
		Type:      t,
		Types:     types,
	}, nil
}

func mapToProtoType(t idl.Type) (*avrocpb.Type, error) {
	switch v := t.(type) {
	case *idl.Record:
		fields := make([]*avrocpb.Field, len(v.Fields))
		for i, f := range v.Fields {
			var err error
			fields[i], err = mapToProtoField(f)
			if err != nil {
				return nil, err
			}
		}
		return &avrocpb.Type{
			Type: &avrocpb.Type_Record{
				Record: &avrocpb.Record{
					Name:      proto.String(v.Name),
					Namespace: proto.String(v.Namespace),
					Aliases:   v.Aliases,
					Fields:    fields,
				},
			},
		}, nil
	case *idl.Enum:
		values := make([]*avrocpb.Ident, len(v.Values))
		for i, val := range v.Values {
			values[i] = mapToProtoIdent(val)
		}
		return &avrocpb.Type{
			Type: &avrocpb.Type_EnumType{
				EnumType: &avrocpb.Enum{
					Name:      proto.String(v.Name),
					Namespace: proto.String(v.Namespace),
					Aliases:   v.Aliases,
					Values:    values,
					Default:   mapToProtoIdent(v.Default),
				},
			},
		}, nil
	case *idl.Array:
		items, err := mapToProtoType(v.Items)
		if err != nil {
			return nil, err
		}
		return &avrocpb.Type{
			Type: &avrocpb.Type_Array{
				Array: &avrocpb.Array{
					Items: items,
				},
			},
		}, nil
	case *idl.Map:
		return &avrocpb.Type{
			Type: &avrocpb.Type_MapType{
				MapType: &avrocpb.Map{
					Values: mapToProtoIdent(v.Values),
				},
			},
		}, nil
	case *idl.Union:
		types := make([]*avrocpb.Type, len(v.Types))
		for i, ut := range v.Types {
			var err error
			types[i], err = mapToProtoType(ut)
			if err != nil {
				return nil, err
			}
		}
		return &avrocpb.Type{
			Type: &avrocpb.Type_Union{
				Union: &avrocpb.Union{
					Types: types,
				},
			},
		}, nil
	case *idl.Fixed:
		size := int32(v.Size)
		return &avrocpb.Type{
			Type: &avrocpb.Type_Fixed{
				Fixed: &avrocpb.Fixed{
					Name:      proto.String(v.Name),
					Namespace: proto.String(v.Namespace),
					Aliases:   v.Aliases,
					Size:      &size,
				},
			},
		}, nil
	case *idl.Ident:
		return &avrocpb.Type{
			Type: &avrocpb.Type_Ident{
				Ident: mapToProtoIdent(v),
			},
		}, nil
	case idl.Ident:
		return &avrocpb.Type{
			Type: &avrocpb.Type_Ident{
				Ident: mapToProtoIdent(&v),
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported IDL type: %T", t)
	}
}

func mapToProtoField(f *idl.Field) (*avrocpb.Field, error) {
	t, err := mapToProtoType(f.Type)
	if err != nil {
		return nil, err
	}

	so := avrocpb.SortOrder(f.SortOrder)
	return &avrocpb.Field{
		Name:      proto.String(f.Name),
		Aliases:   f.Aliases,
		Type:      t,
		SortOrder: &so,
	}, nil
}

func mapToProtoIdent(id *idl.Ident) *avrocpb.Ident {
	if id == nil {
		return nil
	}
	return &avrocpb.Ident{
		Value: proto.String(id.Value),
	}
}
