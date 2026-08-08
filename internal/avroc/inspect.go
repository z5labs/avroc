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
	"log/slog"
	"os"

	"github.com/z5labs/avroc/internal/cli"
	"github.com/z5labs/avroc/internal/ir"
)

// stdinPath is the argument that means "read the descriptor from standard
// input" rather than from a file of that name. The convention is the usual one,
// and it is what lets a descriptor be piped straight out of wherever it was
// saved without landing on disk first.
const stdinPath = "-"

// runInspect renders a descriptor file as JSON on stdout: docs/ir/SPEC.md's
// inspection path, and the first thing to reach for when a generator produces
// output nobody expected. The descriptor is what the generator was handed, and
// until there was a way to read one the question "what did it actually get"
// had no answer at all.
//
// It takes exactly one argument, the descriptor to read, or "-" for stdin.
func runInspect(ctx context.Context, c cli.Context) int {
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() {
		_, _ = io.WriteString(os.Stderr, "Usage: avroc inspect <descriptor>\n\nRenders a descriptor file as JSON. Use - to read standard input.\n")
	}
	if err := flags.Parse(c.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}

	args := flags.Args()
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "inspect takes exactly one descriptor path, got %d\n\n", len(args))
		flags.Usage()
		return 1
	}

	return inspectDescriptor(ctx, c, args[0], os.Stdin, os.Stdout)
}

// inspectDescriptor is the testable core of avroc inspect: stdin and stdout are
// parameters so the rendering can be exercised without touching the process's
// own streams.
//
// The version is deliberately not checked. Rendering is looking, not consuming,
// and a descriptor from a contract this build does not know is exactly the one
// somebody needs to look at — refusing it would withhold the rendering in the
// case that motivates the subcommand. The version is the first field of the
// output, so what a check would have said is on screen anyway. The closed sets
// go unchecked for the same reason; ir.Validate is for a generator about to act
// on a descriptor, not for a person reading one.
func inspectDescriptor(ctx context.Context, c cli.Context, path string, stdin io.Reader, stdout io.Writer) int {
	b, err := readDescriptorBytes(path, stdin)
	if err != nil {
		c.Log.ErrorContext(ctx, "failed to read descriptor", slog.String("path", path), slog.Any("error", err))
		return 1
	}

	desc, err := ir.UnmarshalDescriptor(b)
	if err != nil {
		c.Log.ErrorContext(ctx, "failed to decode descriptor", slog.String("path", path), slog.Any("error", err))
		return 1
	}

	out, err := ir.MarshalDescriptorJSON(desc)
	if err != nil {
		c.Log.ErrorContext(ctx, "failed to render descriptor", slog.String("path", path), slog.Any("error", err))
		return 1
	}

	// One trailing newline, so the rendering is a text file: it ends where a
	// line ends, a shell prompt starts on its own line, and diffing two of them
	// does not report a missing newline at end of file.
	if _, err := fmt.Fprintf(stdout, "%s\n", out); err != nil {
		c.Log.ErrorContext(ctx, "failed to write descriptor JSON", slog.Any("error", err))
		return 1
	}

	return 0
}

func readDescriptorBytes(path string, stdin io.Reader) ([]byte, error) {
	if path == stdinPath {
		return io.ReadAll(stdin)
	}
	return os.ReadFile(path)
}
