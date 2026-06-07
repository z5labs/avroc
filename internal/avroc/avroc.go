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
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/z5labs/avroc/internal/avrocpb"
	"github.com/z5labs/avroc/internal/cli"

	"github.com/z5labs/avro-go/idl"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

// Main dispatches to an avroc subcommand. Generators are declared in an
// avroc.json manifest (see avroc init) and run with avroc generate.
func Main(ctx context.Context, cli cli.Context) int {
	if len(cli.Args) == 0 {
		printUsage(os.Stderr)
		return 1
	}

	switch cmd := cli.Args[0]; cmd {
	case "init":
		return runInit(ctx, cli)
	case "generate":
		return runGenerate(ctx, cli)
	case "help", "-h", "-help", "--help":
		printUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		printUsage(os.Stderr)
		return 1
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: avroc <command>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  init       Scaffold an avroc.json manifest")
	fmt.Fprintln(w, "  generate   Run the generators declared in avroc.json")
	fmt.Fprintln(w, "  help       Show this help")
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
