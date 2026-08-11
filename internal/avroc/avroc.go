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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/cli"
	"github.com/z5labs/avroc/internal/telemetry"

	"github.com/z5labs/avro-go/idl"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Main dispatches to an avroc subcommand. Generators are declared in an
// avroc.json manifest (see avroc init) and run with avroc generate.
//
// Tracing is started and shut down here rather than in cmd/avroc, and the
// reason is os.Exit: main calls it the moment this function returns, and
// os.Exit does not run deferred functions — the defer cancel() beside it has
// never run either. A defer up there would be dead code, and a batch span
// processor holding the run's spans would drop every one of them. Down here it
// is on every path out of the command, the failing ones included, because every
// path out is a return.
//
// The tracer the provider hands out goes to generate and to nothing else (#192).
// A run is the thing worth a trace: it forks processes, walks a project tree and
// takes seconds. init writes one file it was asked for and inspect renders one it
// was handed, and neither is a question anybody asks a span about — so they are
// not merely untraced by omission, they are never given the means.
func Main(ctx context.Context, cli cli.Context) int {
	// A configuration this build cannot honour is a warning and an untraced run,
	// never a failed one: see internal/telemetry. The provider is usable either
	// way, so there is nothing to check before deferring the flush.
	tracing, err := telemetry.Start(ctx, cli)
	if err != nil {
		cli.Log.WarnContext(ctx, "tracing is disabled", slog.Any("error", err))
	}
	defer func() {
		if err := tracing.Shutdown(ctx); err != nil {
			cli.Log.ErrorContext(ctx, "failed to flush traces", slog.Any("error", err))
		}
	}()

	// The run's own parent, when it has one (#199). avroc is routinely started by
	// something that is already tracing — a CI job, or the Dagger exec that sets
	// TRACEPARENT on every container it runs — and #193's carrier is symmetric:
	// the pair avroc writes for a generator is the pair avroc reads for itself,
	// through the same [telemetry.Extract] every generator here goes through.
	//
	// Without it a run produces something that looks entirely correct in
	// isolation — one connected tree with every generator under its invocation —
	// and is an orphan beside the pipeline that ran it. That is the failure
	// `dagger call trace-propagation` exists to catch, and it is the one this
	// call fixes.
	//
	// **Gated on tracing being on, and that is #193's decision rather than a new
	// one.** An untraced avroc exports no span, so there is nothing of avroc's
	// for a generator to be a child of; extracting anyway would leave
	// generatorEnv handing the inherited context straight on, parenting a
	// generator's spans to a trace avroc contributed nothing to. Off therefore
	// stays actively unset, exactly as that story wrote it. It is the same
	// condition [telemetry.Start] already uses to decide whether to touch
	// OpenTelemetry's globals at all: on is when this process joins the world's
	// tracing, and off is when it is not in it.
	if tracing.Enabled() {
		ctx = telemetry.Extract(ctx, cli.Env)
	}

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
	case "generate":
		if code, ok := rejectExtraArgs(cmd, cli.Args[1:]); !ok {
			return code
		}
		return runGenerate(ctx, cli, tracing.Tracer(tracerScope))
	case "inspect":
		// inspect names the descriptor it renders, so it owns its own argument
		// handling rather than being routed through rejectExtraArgs.
		return runInspect(ctx, cli)
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
  generate   Run the generators declared in avroc.json
  inspect    Render a descriptor file as JSON (- reads standard input)
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

// lookupGenerators indexes every generator plugin reachable from dirs, which
// are the PATH entries in the order PATH wrote them.
//
// The earliest match wins, exactly as it does for a shell resolving a command
// name (docs/plugin/SPEC.md, Discovery). That rule is what makes a generator
// under development shadow an installed one by prepending a directory, which is
// how an author tests a change; the opposite rule makes that gesture silently do
// nothing, and the difference is invisible in the generated output.
//
// An empty PATH element is a directory named "", which no host resolves to
// anything — the working directory is searched only when PATH writes it out.
// avroc runs generators as a side effect of a build command, and one picked up
// from whatever directory a user happened to be standing in is an execution
// surface nobody chose.
func lookupGenerators(ctx context.Context, log *slog.Logger, openDir func(string) fs.FS, dirs ...string) (map[string]string, error) {
	generatorIndex := make(map[string]string)

	for _, dir := range dirs {
		if dir == "" {
			continue
		}

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
			if _, found := generatorIndex[name]; found {
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
	name           string
	executablePath string
}

// run runs one generator over one descriptor, as docs/plugin/SPEC.md's
// Invocation specifies: fork and exec avroc-gen-<name> with the descriptor and
// an output directory as absolute paths, and wait for it to exit.
//
// There is no socket, no client and no stream. The generator writes its own
// files beneath the directory it was given and says what went wrong on stderr; a
// zero exit is the whole of the success signal.
//
// The directory it is given is not the project's. It is a private, empty scratch
// directory that this invocation owns, and only a zero exit gets what is in it
// merged into the project's output tree. Anything else fails the run and the
// scratch directory is discarded, so nothing a failing generator left behind is
// adopted as output — which is what makes a partially written failure the
// contract explicitly permits harmless.
//
// What it returns is a plan and not a merge: on a zero exit every file the
// generator produced is resolved to where it is going, and all of it is still in
// the scratch directory, which is then the caller's to merge and to remove. The
// merge waits for every generator because two of them producing the same path is
// avroc's to detect and has to be detected before either file reaches the
// project tree (#118). On any failure the directory is removed here, with
// whatever the generator left in it, and no plan is returned.
func (g generator) run(ctx context.Context, output string, options []*avrocpb.Option, schemas ...*avrocpb.Schema) (out *generatorOutput, err error) {
	// One span per invocation, and the anchor the generator's own spans hang off:
	// generatorEnv below puts this span's context in the child's environment
	// (#193), so a span the generator opens is a child of this invocation rather
	// than of the run as a whole.
	//
	// Registered before every other defer here so that it unwinds after all of
	// them, and therefore records the error the whole invocation returned rather
	// than the one the process exited with.
	ctx, span := startSpan(ctx, spanGeneratorRun, generatorAttr(g.name))
	defer func() {
		endSpan(ctx, span, err)
	}()

	desc := newDescriptor(options, schemas)

	// One descriptor file per generator invocation, in a directory created for
	// that invocation and nothing else, removed once the generator has exited.
	// docs/plugin/SPEC.md's "Location and lifetime" is normative for all three,
	// and this defer is registered first so that it unwinds last — after the
	// generator process has been waited on, never while it may still be reading.
	descriptorDir, err := os.MkdirTemp("", g.name+"-descriptor-*")
	if err != nil {
		g.log.ErrorContext(ctx, "failed to create descriptor directory", slog.String("generator", g.name), slog.Any("error", err))
		return nil, err
	}
	defer func() {
		err = errors.Join(err, os.RemoveAll(descriptorDir))
	}()

	descriptorPath, err := writeDescriptor(descriptorDir, desc)
	if err != nil {
		g.log.ErrorContext(ctx, "failed to write descriptor", slog.String("generator", g.name), slog.Any("error", err))
		return nil, err
	}

	// Both paths go across as absolute ones, so that two runs of the same
	// generator from different working directories are the same invocation.
	descriptorPath, err = filepath.Abs(descriptorPath)
	if err != nil {
		g.log.ErrorContext(ctx, "failed to resolve descriptor path", slog.String("generator", g.name), slog.Any("error", err))
		return nil, err
	}
	outputDir, err := filepath.Abs(output)
	if err != nil {
		g.log.ErrorContext(ctx, "failed to resolve output directory", slog.String("generator", g.name), slog.Any("error", err))
		return nil, err
	}

	// The project's own output tree, which avroc owns and creates: a generator
	// never learns where it is, and it has to exist before a scratch directory
	// can be made inside it.
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		g.log.ErrorContext(ctx, "failed to create output directory", slog.String("generator", g.name), slog.Any("error", err))
		return nil, err
	}

	// The directory --out names: private to this invocation, empty, and the only
	// place this generator can write.
	scratchDir, err := newScratchDir(outputDir, g.name)
	if err != nil {
		g.log.ErrorContext(ctx, "failed to create scratch directory", slog.String("generator", g.name), slog.Any("error", err))
		return nil, err
	}
	// Removed here whenever this invocation produces nothing to merge: on a
	// failure with the partial output the contract lets a generator leave behind
	// still in it, and on cancellation. A plan returned hands the directory to the
	// caller instead, because the files stay in it until every generator's output
	// has been resolved. Registered after the descriptor's removal so that it
	// unwinds before it, and both after the process has been waited on rather
	// than while it may still be writing.
	defer func() {
		if out != nil {
			return
		}
		err = errors.Join(err, os.RemoveAll(scratchDir))
	}()

	args := generatorArgs(descriptorPath, scratchDir, options)
	g.log.DebugContext(ctx, "running generator",
		slog.String("generator", g.name),
		slog.String("executable", g.executablePath),
		slog.Any("args", args),
	)

	// CommandContext kills the child when the signal-based parent context is
	// done, and Run waits on it either way — so the process is reaped on
	// success, on failure and on cancellation, and no generator outlives the
	// avroc that started it.
	cmd := exec.CommandContext(ctx, g.executablePath, args...)
	// avroc's own environment, carrying this invocation's span context instead of
	// whatever trace context it inherited (#193). Setting Env at all is the thing
	// to be careful about — a nil Env means the child gets os.Environ(), so
	// generatorEnv starts from exactly that and removes exactly two variables.
	cmd.Env = generatorEnv(ctx, os.Environ())
	// Standard input is unused: avroc always passes the descriptor as a path,
	// never through the "-" form the contract also allows.
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	// Standard error is inherited rather than read. avroc analyses the exit
	// status and nothing else (#190), so the generator's stderr is its own to
	// write and avroc's only job is to get out of the way: os.Stderr is an
	// *os.File, which exec hands to the child as a file descriptor instead of
	// spawning a goroutine to copy it, so the bytes reach avroc's standard error
	// unaltered, in order, and as they are written rather than when the process
	// exits.
	//
	// Two consequences are accepted deliberately. A multi-kilobyte write — a
	// stack trace — from one of several concurrent generators can interleave
	// with another's, because only writes below PIPE_BUF are atomic. And nothing
	// says which generator a line came from until generators emit log records
	// carrying their own resource; a prefix added here would be the parsed
	// attribution this removed.
	//
	// Passing the descriptor through also retires the WaitDelay this used to
	// need: with no copy goroutine there is no pipe for a grandchild to hold
	// open, and the kill CommandContext sends on cancellation cannot be caught.
	cmd.Stderr = os.Stderr

	runErr := cmd.Run()
	if runErr != nil {
		return nil, g.reportFailure(ctx, runErr)
	}

	// Only now, and only because the exit was zero: everything the generator left
	// in its scratch directory is the output of the run. Resolving it is as far as
	// this goes — nothing of it moves until every generator has got here.
	files, err := planMerge(scratchDir, outputDir)
	if err != nil {
		g.log.ErrorContext(ctx, "failed to resolve generated output",
			slog.String("generator", g.name),
			slog.Any("error", err),
		)
		return nil, fmt.Errorf("generator %q: %w", g.name, err)
	}

	return &generatorOutput{
		generator: g.name,
		output:    outputDir,
		scratch:   scratchDir,
		files:     files,
	}, nil
}

// reportFailure logs and describes an invocation that did not succeed, telling
// docs/plugin/SPEC.md's three cases apart.
//
// A generator terminated by a signal is named as terminated by that signal, and
// distinguishably from one that exited non-zero, because the two need different
// responses: a non-zero exit is a bug in the generator, while a signal is
// usually the run being cancelled or the machine running out of memory. A report
// that flattened them would send the user looking in the wrong place. The third
// case is a generator that never ran at all — a file that lost its execute bit
// between discovery and invocation — which is neither.
//
// No meaning is attached to a particular non-zero value beyond failure. The
// small integers are already spoken for by parties this contract does not
// control, so the code is reported and nothing is concluded from it.
//
// The three cases are also the three shapes the invocation's span takes, because
// the classification has already been made here and a second reading of the same
// error somewhere else would be a fourth description of the same distinction: a
// signal carries signal and signal_number, a non-zero exit carries exit_code,
// and a generator that never ran carries neither, there having been no process
// to produce either. What this does not do is set the span's status — run's
// deferred endSpan sets it once, from the error this returns, so a generator
// killed because the run was cancelled is still marked cancelled.
func (g generator) reportFailure(ctx context.Context, runErr error) error {
	span := trace.SpanFromContext(ctx)

	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		g.log.ErrorContext(ctx, "failed to run generator",
			slog.String("generator", g.name),
			slog.Any("error", runErr),
		)
		return fmt.Errorf("generator %q failed to run: %w", g.name, runErr)
	}

	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		signal := status.Signal()
		span.SetAttributes(
			attribute.String(attrSignal, signal.String()),
			attribute.Int(attrSignalNumber, int(signal)),
		)
		g.log.ErrorContext(ctx, "generator terminated by signal",
			slog.String("generator", g.name),
			slog.String("signal", signal.String()),
			slog.Int("signal_number", int(signal)),
		)
		return fmt.Errorf("generator %q was terminated by signal %s (%d): %w", g.name, signal, int(signal), runErr)
	}

	span.SetAttributes(attribute.Int(attrExitCode, exitErr.ExitCode()))
	g.log.ErrorContext(ctx, "generator exited non-zero",
		slog.String("generator", g.name),
		slog.Int("exit_code", exitErr.ExitCode()),
	)
	return fmt.Errorf("generator %q exited with status %d: %w", g.name, exitErr.ExitCode(), runErr)
}

// generatorArgs builds the argument vector docs/plugin/SPEC.md specifies:
//
//	avroc-gen-<name> --descriptor <path> --out <dir> [--opt k=v ...]
//
// and nothing else. In particular nothing is taken from the environment: the
// AVROC_GENERATOR_ARGS variable that used to prepend arbitrary words here is
// gone, because it made the vector a function of the ambient environment, which
// is the one input a manifest does not record and a reviewer cannot see.
//
// The options are emitted in the order they arrive, which the manifest fixes,
// so the vector is a function of the manifest rather than of a map iteration.
func generatorArgs(descriptorPath, outputDir string, options []*avrocpb.Option) []string {
	args := make([]string, 0, 4+2*len(options))
	args = append(args, "--descriptor", descriptorPath, "--out", outputDir)
	for _, opt := range options {
		args = append(args, "--opt", opt.GetName()+"="+opt.GetValue())
	}
	return args
}
