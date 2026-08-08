// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// Package plugin is the generator's half of docs/plugin/SPEC.md: the argument
// vector avroc invokes an executable with, the descriptor that vector names,
// and the directory every generated file is written beneath.
//
// avroc's half — discovering the executable and building the vector — is
// internal/avroc's. The two are deliberately separate packages either side of a
// process boundary: a third-party generator implements this contract without
// importing anything here, and the only thing that binds the two is the vector
// itself.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/cli"

	"google.golang.org/protobuf/proto"
)

// Invocation is one parsed generator argument vector.
type Invocation struct {
	// Descriptor is the path the descriptor was handed at, or "-" for standard
	// input. Nothing may be derived from the path itself: docs/plugin/SPEC.md
	// makes it a temporary location avroc chose and not a place to hang meaning.
	Descriptor string

	// Out is the directory every generated file is written beneath.
	Out string

	// Options are the --opt pairs, in the order they appeared on the command
	// line. avroc emits them in the order the manifest fixed, so a generator
	// reading them in order reads them in the manifest's.
	Options []*avrocpb.Option
}

// ParseArgs parses the argument vector docs/plugin/SPEC.md specifies:
//
//	avroc-gen-<name> --descriptor <path> --out <dir> [--opt k=v ...]
//
// Only the separated form is accepted — each option followed by its value as
// the next argument — because that is the one form avroc emits and the one a
// shell-script plugin can handle in a three-line case statement. The joined
// --descriptor=<path> spelling is a thing a plugin MAY accept and avroc never
// produces, so accepting it here would be surface with nothing behind it.
//
// "--" ends the options. The contract defines no operands and avroc passes
// none, so anything after it is an error rather than something to ignore.
func ParseArgs(args []string) (*Invocation, error) {
	inv := &Invocation{}
	var sawDescriptor, sawOut bool

	for i := 0; i < len(args); i++ {
		arg := args[i]

		if arg == "--" {
			if rest := args[i+1:]; len(rest) > 0 {
				return nil, fmt.Errorf("unexpected operand %q: the generator contract defines none", rest[0])
			}
			break
		}

		switch arg {
		case "--descriptor", "--out", "--opt":
		default:
			if strings.HasPrefix(arg, "-") {
				return nil, fmt.Errorf("unknown option %q", arg)
			}
			return nil, fmt.Errorf("unexpected operand %q: the generator contract defines none", arg)
		}

		if i+1 >= len(args) {
			return nil, fmt.Errorf("%s requires a value", arg)
		}
		value := args[i+1]
		i++

		switch arg {
		case "--descriptor":
			if sawDescriptor {
				return nil, errors.New("--descriptor given more than once")
			}
			sawDescriptor, inv.Descriptor = true, value
		case "--out":
			if sawOut {
				return nil, errors.New("--out given more than once")
			}
			sawOut, inv.Out = true, value
		case "--opt":
			opt, err := parseOption(value)
			if err != nil {
				return nil, err
			}
			inv.Options = append(inv.Options, opt)
		}
	}

	if !sawDescriptor {
		return nil, errors.New("--descriptor is required")
	}
	if !sawOut {
		return nil, errors.New("--out is required")
	}
	return inv, nil
}

// parseOption splits one --opt value at its first "=". Everything before it is
// the key and everything after it is the value, so a value may be empty or
// carry further "=" characters; a key may do neither.
func parseOption(s string) (*avrocpb.Option, error) {
	key, value, ok := strings.Cut(s, "=")
	if !ok {
		return nil, fmt.Errorf("option %q is not of the form k=v", s)
	}
	if key == "" {
		return nil, fmt.Errorf("option %q has an empty key", s)
	}
	return &avrocpb.Option{
		Name:  proto.String(key),
		Value: proto.String(value),
	}, nil
}

// Usage is the one-line usage every generator in this repository prints, with
// name the executable's own.
func Usage(name string) string {
	return fmt.Sprintf("Usage: %s --descriptor <path> --out <dir> [--opt k=v ...]\n", name)
}

// ReadDescriptor reads and decodes the descriptor this invocation names,
// reading stdin instead when the path is "-".
//
// The options on the returned descriptor are the ones from the command line,
// not the ones the encoded descriptor happens to carry. docs/plugin/SPEC.md is
// normative for how a generator is configured — "everything that configures its
// output arrives as --opt" — and avroc emits the same pairs in both places, so
// the two never disagree in practice. Preferring the vector is what keeps a
// descriptor saved from a failing run re-runnable by hand with the options
// changed.
func (inv *Invocation) ReadDescriptor(stdin io.Reader) (*avrocpb.GenerateRequest, error) {
	var b []byte
	var err error
	if inv.Descriptor == "-" {
		b, err = io.ReadAll(stdin)
	} else {
		b, err = os.ReadFile(inv.Descriptor)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read descriptor: %w", err)
	}

	var req avrocpb.GenerateRequest
	if err := proto.Unmarshal(b, &req); err != nil {
		return nil, fmt.Errorf("failed to decode descriptor: %w", err)
	}

	req.Options = inv.Options
	return &req, nil
}

// OutputPath resolves a generated file's relative path against the output
// directory, refusing anything that would land outside it.
//
// docs/plugin/SPEC.md puts the requirement on the plugin — every file beneath
// --out, not through "..", not through an absolute path — and avroc enforcing
// it as well is #117's. Checking here means a generator in this repository
// cannot break the rule even while avroc is not yet watching.
//
// The path a generator emits is a slash-separated relative path, whatever the
// host's separator, so it is converted before it is joined.
func OutputPath(out, p string) (string, error) {
	if p == "" {
		return "", errors.New("path is empty")
	}
	if path.IsAbs(p) {
		return "", fmt.Errorf("path %q is absolute", p)
	}

	osPath := filepath.FromSlash(p)
	if filepath.IsAbs(osPath) {
		return "", fmt.Errorf("path %q is absolute", p)
	}

	root, err := filepath.Abs(out)
	if err != nil {
		return "", err
	}

	dst := filepath.Join(root, osPath)
	rel, err := filepath.Rel(root, dst)
	if err != nil {
		return "", err
	}
	// "." is the output directory itself, which is not a file a generator can
	// write — "", "." and "./" all reach it, and each would otherwise fail
	// later as a confusing "is a directory" from the open.
	if rel == "." {
		return "", fmt.Errorf("path %q names the output directory, not a file in it", p)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the output directory", p)
	}
	return dst, nil
}

// GenerateFunc is the generation entry point every generator in this
// repository exposes: a descriptor in, files out through the stream.
//
// The streaming shape is the one the generators here still implement, and it is
// transitional — #121, #122 and #123 replace it with a plain write into --out,
// and #124 removes the service the type comes from. Nothing in the contract
// this package implements requires it: avroc no longer speaks to a generator
// over a socket, and the chunks never leave the process.
type GenerateFunc func(*avrocpb.GenerateRequest, avrocpb.Generator_GenerateServer) error

// Main runs one generation from an argument vector and returns the process
// exit status: parse the vector, read the descriptor, run generate, and write
// what it produced beneath --out.
//
// Diagnostics go to the logger, which every generator's main wires to standard
// error. Standard output carries nothing during a generation invocation, which
// is what keeps it free for the --plugin-info declaration (#116) and makes an
// invocation safe to put in a pipeline.
func Main(ctx context.Context, c cli.Context, name string, generate GenerateFunc) int {
	inv, err := ParseArgs(c.Args)
	if err != nil {
		c.Log.ErrorContext(ctx, "invalid arguments", slog.String("generator", name), slog.Any("error", err))
		_, _ = io.WriteString(os.Stderr, Usage(name))
		return 1
	}

	req, err := inv.ReadDescriptor(os.Stdin)
	if err != nil {
		c.Log.ErrorContext(ctx, "failed to read descriptor", slog.String("generator", name), slog.Any("error", err))
		return 1
	}

	sink := &fileStream{ctx: ctx, inv: inv}
	// A file whose terminating chunk never arrived is discarded however this
	// returns, so a failed generation leaves no half-written source behind.
	defer sink.discard()

	if err := generate(req, sink); err != nil {
		c.Log.ErrorContext(ctx, "failed to generate", slog.String("generator", name), slog.Any("error", err))
		return 1
	}
	if err := sink.finish(); err != nil {
		c.Log.ErrorContext(ctx, "failed to write generated output", slog.String("generator", name), slog.Any("error", err))
		return 1
	}

	c.Log.DebugContext(ctx, "generated output", slog.String("generator", name), slog.Any("output_files", sink.written))
	return 0
}
