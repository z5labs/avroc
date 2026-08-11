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
//
// It is also where an invocation joins avroc's trace, because [Main] is the one
// place every generator in this repository runs through (#196). That is an
// implementation of docs/plugin/SPEC.md's "Trace context" and not an addition
// to it: the contract says a plugin MAY read the pair avroc sets, and one that
// does not behaves exactly as it did before they existed. See trace.go.
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
	"slices"
	"strings"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/cli"
	"github.com/z5labs/avroc/internal/telemetry"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
)

// Invocation is one parsed generator argument vector.
type Invocation struct {
	// Descriptor is the path the descriptor was handed at, or "-" for standard
	// input. Nothing may be derived from the path itself: docs/plugin/SPEC.md
	// makes it a temporary location avroc chose and not a place to hang meaning.
	Descriptor string

	// Out is the directory every generated file is written beneath. It is a
	// private, empty scratch directory avroc made for this invocation alone —
	// not the project's output tree, which avroc merges into itself — so
	// nothing may be derived from the path and no file written on a previous
	// run is in it.
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

// Usage is the usage every generator in this repository prints, with name the
// executable's own. Both vectors the contract defines are listed: a generation,
// and the capability handshake that precedes one.
func Usage(name string) string {
	return fmt.Sprintf("Usage: %s --descriptor <path> --out <dir> [--opt k=v ...]\n       %s %s\n", name, name, PluginInfoFlag)
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
// --out, not through "..", not through an absolute path — and avroc enforces it
// as well, at merge time, rather than trusting it. Checking here too is not
// redundant: it fails at the write, naming the path and the generator's own call
// site, where the merge can only report that a file it found should not exist.
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

// GenerateFunc is the generation entry point a generator in this repository
// exposes: a descriptor in, files out through the writer.
//
// It is the shape docs/plugin/SPEC.md describes — a generator writes whole
// files beneath --out and exits — with the directory behind an interface so
// that Main owns the path checking and the bookkeeping and a generator owns
// only its bytes.
type GenerateFunc func(*avrocpb.GenerateRequest, FileWriter) error

// Main runs one invocation from an argument vector and returns the process exit
// status.
//
// There are two vectors the contract defines. --plugin-info writes info to
// standard output and exits zero, having read no descriptor and written no
// file. Anything else is a generation: parse the vector, read the descriptor,
// and run generate against a writer rooted at --out.
//
// Diagnostics go to the logger, which every generator's main wires to standard
// error. Standard output carries nothing during a generation invocation, which
// is what keeps it free for the declaration and makes an invocation safe to put
// in a pipeline.
//
// This is also where an invocation joins avroc's trace (#196), and it is one
// implementation rather than three because every generator here runs through it.
func Main(ctx context.Context, c cli.Context, info Info, generate GenerateFunc) int {
	return run(ctx, c, info, generate, os.Stdout)
}

// run is Main with the declaration's destination passed in, so that a test can
// read what a generator would have written to standard output without replacing
// the process's own.
//
// It is also the whole of the invocation's tracing, and the reason it is here
// rather than in a generator's main is os.Exit: every cmd/avroc-gen-* calls it
// the moment this returns, and os.Exit runs no deferred function — the
// defer cancel() beside it has never run either. A flush deferred up there would
// be dead code and the batch processor would drop every span it was holding, so
// the flush is deferred *inside this function*, which every path out of an
// invocation returns through. It is the discipline #191 needed for avroc itself,
// applied to the three executables avroc forks.
//
// The order of the two defers is load bearing. Deferred functions run last in
// first out, so the flush is registered first and runs last: whatever ends the
// span — the ordinary return below, or a panic unwinding through it — the span is
// finished before the provider is asked to export it.
func run(ctx context.Context, c cli.Context, info Info, generate GenerateFunc, stdout io.Writer) int {
	name := info.Executable()

	// A configuration this build cannot honour is a warning and an untraced
	// invocation, never a failed one: see internal/telemetry, and note that a
	// generator refusing to generate over a telemetry variable would be refusing
	// over something docs/plugin/SPEC.md says it may ignore entirely.
	tracing, err := telemetry.Start(ctx, c,
		telemetry.WithDefaultServiceName(name),
		telemetry.WithFlushBudget(FlushBudget),
	)
	if err != nil {
		c.Log.WarnContext(ctx, "tracing is disabled", slog.String("generator", name), slog.Any("error", err))
	}
	defer func() {
		if err := tracing.Shutdown(ctx); err != nil {
			c.Log.ErrorContext(ctx, "failed to flush traces", slog.String("generator", name), slog.Any("error", err))
		}
	}()

	// Checked before the vector is parsed, and by membership rather than by
	// position: --plugin-info accepts no other argument, so a vector carrying one
	// is a mistake to report rather than a generation to attempt with a flag
	// ParseArgs would call unknown. It is read here rather than inside invoke
	// because the two vectors are two spans, and which one this is has to be
	// known before either is opened.
	declaring := slices.Contains(c.Args, PluginInfoFlag)

	spanName, attrs := spanGenerate, []attribute.KeyValue{attribute.String(attrGenerator, name)}
	if declaring {
		// A handshake is handed no descriptor, so the only IR version there is to
		// carry is the one being declared. A generation carries the descriptor's,
		// set once it has been read.
		spanName = spanPluginInfo
		attrs = append(attrs, attribute.Int64(attrIRVersion, int64(info.IRVersion)))
	}

	// The parent is whatever avroc wrote into the environment, and nothing when
	// it wrote nothing — which is the ordinary case, not a fallback:
	// docs/plugin/SPEC.md's "Trace context" says a plugin run by a shell, by an
	// older avroc or by an avroc that is not tracing sees exactly this.
	ctx, span := tracing.Tracer(tracerScope).Start(
		telemetry.Extract(ctx, c.Env),
		spanName,
		trace.WithAttributes(attrs...),
	)

	code := invoke(ctx, c, info, generate, stdout, declaring)
	endSpan(span, code)
	return code
}

// invoke runs the invocation itself and returns the process's exit status. It is
// run without the telemetry, so that the span above is opened and ended in one
// place and the two vectors below read as they did before there was one.
func invoke(ctx context.Context, c cli.Context, info Info, generate GenerateFunc, stdout io.Writer, declaring bool) int {
	name := info.Executable()

	if declaring {
		if len(c.Args) != 1 {
			c.Log.ErrorContext(ctx, "invalid arguments",
				slog.String("generator", name),
				slog.Any("error", fmt.Errorf("%s takes no other arguments, got %v", PluginInfoFlag, c.Args)),
			)
			_, _ = io.WriteString(os.Stderr, Usage(name))
			return 1
		}
		if err := WriteInfo(stdout, info); err != nil {
			c.Log.ErrorContext(ctx, "failed to write the capability declaration",
				slog.String("generator", name),
				slog.Any("error", err),
			)
			return 1
		}
		return 0
	}

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
	// The version of the descriptor this invocation was *handed*, which is the
	// one worth having on the span: a generator that refuses one it does not
	// understand refuses it in Generate, and the span is then the only record of
	// what it was refusing. Taken from the span in the context rather than from a
	// parameter, for the reason internal/avroc gives — the provider travels in the
	// context, so nothing below run grew an argument to thread through untouched.
	trace.SpanFromContext(ctx).SetAttributes(attribute.Int64(attrIRVersion, int64(req.GetVersion())))

	out := NewOutputDir(inv.Out)
	if err := generate(req, out); err != nil {
		c.Log.ErrorContext(ctx, "failed to generate", slog.String("generator", name), slog.Any("error", err))
		return 1
	}
	// Checked even though generate returned nothing: a generator that ignored
	// a failed write would otherwise exit zero, and a zero exit is the whole of
	// the success signal — avroc would adopt the directory as this run's output
	// with the file missing from it.
	if err := out.Err(); err != nil {
		c.Log.ErrorContext(ctx, "failed to write generated output", slog.String("generator", name), slog.Any("error", err))
		return 1
	}

	c.Log.DebugContext(ctx, "generated output", slog.String("generator", name), slog.Any("output_files", out.Written()))
	return 0
}
