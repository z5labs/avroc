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
	"net"
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

	paths := filepath.SplitList(path)
	generators, err := lookupGenerators(ctx, cli.Log, cli.OpenDir, paths...)
	if err != nil {
		cli.Log.ErrorContext(ctx, "failed to lookup generators", slog.Any("error", err))
		return 1
	}

	flags := flag.NewFlagSet("avroc", flag.ContinueOnError)
	flags.Usage = func() {}

	optFlags := make(map[string]*optionFlag)
	for name := range generators {
		generatorName := strings.TrimPrefix(name, "avroc-gen-")

		flags.String(generatorName+"_out", "", fmt.Sprintf("Output directory for the %q generator", generatorName))

		of := &optionFlag{}
		optFlags[generatorName] = of
		flags.Var(of, generatorName+"_opt", fmt.Sprintf("Options for the %q generator (key=value)", generatorName))
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
		if !strings.HasSuffix(f.Name, "_out") {
			return
		}

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

			var options []*avrocpb.Option
			if of, ok := optFlags[flagName]; ok {
				options = of.options()
			}

			g := generator{
				log:            cli.Log,
				env:            cli.Env,
				name:           generatorName,
				executablePath: executablePath,
			}

			return g.generate(ctx, output, options, schemas...)
		})
	})

	err = genPool.Wait()
	if err != nil {
		cli.Log.ErrorContext(ctx, "failed to run generators", slog.Any("error", err))
		return 1
	}

	return 0
}

func lookupGenerators(ctx context.Context, log *slog.Logger, openDir func(string) fs.FS, dirs ...string) (map[string]string, error) {
	generatorIndex := make(map[string]string)

	for _, dir := range dirs {
		fsys := openDir(dir)
		entries, err := fs.ReadDir(fsys, ".")
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if errors.Is(err, fs.ErrPermission) {
			log.WarnContext(ctx, "skipping path due to permission error", slog.String("path", dir), slog.Any("error", err))
			continue
		}
		if err != nil {
			return nil, err
		}

		for _, entry := range entries {
			name := entry.Name()
			info, infoErr := entry.Info()
			if infoErr != nil {
				continue
			}
			if !isGeneratorExecutable(name, info.Mode()) {
				continue
			}
			generatorIndex[generatorKey(name)] = filepath.Join(dir, name)
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

		_, err = fmt.Fprintf(w, "  -%s_opt key=value\n", name)
		if err != nil {
			return err
		}

		_, err = fmt.Fprintf(w, "        Options for the %q generator (can be specified multiple times)\n", name)
		if err != nil {
			return err
		}
	}

	return nil
}

// optionFlag implements flag.Value to support repeated key=value flag usage.
type optionFlag struct {
	values []string
}

func (f *optionFlag) String() string {
	return strings.Join(f.values, ",")
}

func (f *optionFlag) Set(value string) error {
	if !strings.Contains(value, "=") {
		return fmt.Errorf("option must be in key=value format: %q", value)
	}
	f.values = append(f.values, value)
	return nil
}

func (f *optionFlag) options() []*avrocpb.Option {
	opts := make([]*avrocpb.Option, len(f.values))
	for i, v := range f.values {
		// Cut is safe here; Set() already validated the "=" separator is present.
		key, val, _ := strings.Cut(v, "=")
		opts[i] = &avrocpb.Option{
			Name:  proto.String(key),
			Value: proto.String(val),
		}
	}
	return opts
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

func (g generator) generate(ctx context.Context, output string, options []*avrocpb.Option, schemas ...*avrocpb.Schema) (err error) {
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

	// Use the passthrough resolver with a custom dialer rather than "unix://".
	// gRPC's URL-based target parsing mishandles Windows absolute paths (the
	// drive-letter colon is read as host:port); passthrough hands the address
	// to the dialer verbatim so the socket path is never URL-parsed.
	cc, err := grpc.NewClient(
		"passthrough:///"+socketPath,
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", addr)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
	)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, cc.Close())
	}()

	// No overall RPC timeout: a large streamed generation can legitimately run
	// long. Cancellation flows from the signal-based parent context instead.
	client := avrocpb.NewGeneratorClient(cc)
	stream, err := client.Generate(ctx, &avrocpb.GenerateRequest{
		Options: options,
		Schemas: schemas,
	})
	if err != nil {
		g.log.ErrorContext(ctx, "failed to generate code", slog.String("generator", g.name), slog.Any("error", err))
		return err
	}

	outputFiles, err := g.writeStream(output, stream)
	if err != nil {
		g.log.ErrorContext(ctx, "failed to write generated output", slog.String("generator", g.name), slog.Any("error", err))
		return err
	}

	g.log.InfoContext(ctx, "generated output", slog.String("generator", g.name), slog.Any("output_files", outputFiles))
	return nil
}

// writeStream consumes the generator's server stream, writing each file's
// ordered chunks directly to disk under output rather than buffering whole
// files in memory. A path is validated to stay within output and its file is
// created on the first chunk, so an unsafe or buggy generator cannot force
// avroc to buffer arbitrary content before being rejected. A file is only
// considered written once a chunk with last=true terminates it; the stream
// ending with files still open, or any chunk arriving after a file's
// terminating chunk, is reported as an error. It returns the OS-native paths
// of the files written.
func (g generator) writeStream(output string, stream avrocpb.Generator_GenerateClient) ([]string, error) {
	open := make(map[string]*os.File)
	finalized := make(map[string]struct{})
	var written []string

	// On any early return, discard files that never received their terminating
	// chunk so partial/corrupt output is not left behind in the output tree.
	defer func() {
		for path, f := range open {
			name := f.Name()
			_ = f.Close()
			_ = os.Remove(name)
			delete(open, path)
		}
	}()

	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			if len(open) > 0 {
				return written, fmt.Errorf("generator %q closed the stream with %d unterminated file(s)", g.name, len(open))
			}
			return written, nil
		}
		if err != nil {
			return written, err
		}

		path := msg.GetPath()
		if _, done := finalized[path]; done {
			return written, fmt.Errorf("generator %q sent a chunk for already-completed file %q", g.name, path)
		}

		f, ok := open[path]
		if !ok {
			dst, err := safeOutputPath(output, path)
			if err != nil {
				return written, fmt.Errorf("generator %q returned unsafe path %q: %w", g.name, path, err)
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return written, fmt.Errorf("failed to create output directory for %q: %w", dst, err)
			}
			f, err = os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err != nil {
				return written, fmt.Errorf("failed to create file %q: %w", dst, err)
			}
			open[path] = f
		}

		if _, err := f.Write(msg.GetContent()); err != nil {
			return written, fmt.Errorf("failed to write file %q: %w", f.Name(), err)
		}

		if !msg.GetLast() {
			continue
		}

		name := f.Name()
		delete(open, path)
		finalized[path] = struct{}{}
		if err := f.Close(); err != nil {
			return written, fmt.Errorf("failed to close file %q: %w", name, err)
		}
		written = append(written, name)
	}
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
