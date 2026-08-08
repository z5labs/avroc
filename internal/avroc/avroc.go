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

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/cli"

	"github.com/z5labs/avro-go/idl"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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
		if code, ok := rejectExtraArgs(cmd, cli.Args[1:]); !ok {
			return code
		}
		return runInit(ctx, cli)
	case "get":
		// get owns its own flag parsing (-upgrade), so it is not routed through
		// rejectExtraArgs.
		return runGet(ctx, cli)
	case "generate":
		if code, ok := rejectExtraArgs(cmd, cli.Args[1:]); !ok {
			return code
		}
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

// rejectExtraArgs enforces that init and generate take no positional
// arguments. The manifest is the sole source of inputs/generators, so a stray
// argument — a typo or legacy flag-style usage like "avroc generate
// schema.avdl" — should fail loudly instead of being silently ignored.
func rejectExtraArgs(cmd string, extra []string) (int, bool) {
	if len(extra) == 0 {
		return 0, true
	}
	fmt.Fprintf(os.Stderr, "%s takes no arguments, got %v\n\n", cmd, extra)
	printUsage(os.Stderr)
	return 1, false
}

func printUsage(w io.Writer) {
	const usage = `Usage: avroc <command>

Commands:
  init       Scaffold an avroc.json manifest
  get        Resolve and pull generator images, pinning them in avroc.lock
  generate   Run the generators declared in avroc.json
  help       Show this help
`
	_, _ = io.WriteString(w, usage)
}

// isGeneratorExecutable reports whether a directory entry is a generator
// plugin: a regular file named avroc-gen-<name> carrying an execute bit.
//
// avroc targets POSIX hosts (see docs/plugin/SPEC.md), so the mode bits are the
// whole test. There is no extension to strip and no second rule for a second
// operating system — the entry's name is the generator's name.
func isGeneratorExecutable(name string, mode fs.FileMode) bool {
	return strings.HasPrefix(name, "avroc-gen-") && mode.IsRegular() && mode&0o111 != 0
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
			generatorIndex[name] = filepath.Join(dir, name)
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
	desc := newDescriptor(options, schemas)

	// One descriptor file per generator invocation, in a directory created for
	// that invocation and nothing else, removed once the generator has exited.
	// docs/plugin/SPEC.md's "Location and lifetime" is normative for all three,
	// and this defer is registered first so that it unwinds last — after the
	// generator process has been waited on, never while it may still be reading.
	descriptorDir, err := os.MkdirTemp("", g.name+"-descriptor-*")
	if err != nil {
		g.log.ErrorContext(ctx, "failed to create descriptor directory", slog.String("generator", g.name), slog.Any("error", err))
		return err
	}
	defer func() {
		err = errors.Join(err, os.RemoveAll(descriptorDir))
	}()

	descriptorPath, err := writeDescriptor(descriptorDir, desc)
	if err != nil {
		g.log.ErrorContext(ctx, "failed to write descriptor", slog.String("generator", g.name), slog.Any("error", err))
		return err
	}
	// The path is logged and not yet passed: the argument vector that hands it
	// to the generator is #114's, and until then the same descriptor value
	// travels over the gRPC service #124 removes. Logging it is what makes the
	// file findable while it exists.
	g.log.DebugContext(ctx, "wrote descriptor", slog.String("generator", g.name), slog.String("descriptor", descriptorPath))

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

	// Use the passthrough resolver with a custom dialer rather than "unix://":
	// passthrough hands the address to the dialer verbatim, so a socket path
	// is never run through gRPC's URL-based target parsing. The whole gRPC
	// path here goes away with the story that deletes the Generator service.
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
	stream, err := client.Generate(ctx, desc)
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
