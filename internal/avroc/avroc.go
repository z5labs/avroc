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
	"time"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/cli"

	"github.com/z5labs/avro-go/idl"
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
  get        Resolve and pull generator images, pinning them in avroc.lock
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

// generatorWaitDelay bounds how long avroc waits after a generator's process
// has been killed before giving up on the streams it inherited.
//
// Cancellation kills the child, which cannot be caught or ignored, so this is
// not what makes a cancelled run terminate. What it covers is a grandchild that
// outlived its parent still holding avroc's stderr open: without it Wait blocks
// on a descriptor nobody is going to close, and a cancelled run hangs on a
// process avroc never started.
const generatorWaitDelay = 5 * time.Second

// generate runs one generator over one descriptor, as docs/plugin/SPEC.md's
// Invocation specifies: fork and exec avroc-gen-<name> with the descriptor and
// the output directory as absolute paths, and wait for it to exit.
//
// There is no socket, no client and no stream. The generator writes its own
// files beneath output and says what went wrong on stderr; a zero exit is the
// whole of the success signal.
//
// Anything else fails the run, and nothing the generator left behind is adopted
// as output. Discarding a private scratch directory rather than merging it is
// that same rule once #117 gives a generator one; today --out is the project's
// own directory and there is no merge step to withhold.
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

	// Both paths go across as absolute ones, so that two runs of the same
	// generator from different working directories are the same invocation.
	descriptorPath, err = filepath.Abs(descriptorPath)
	if err != nil {
		g.log.ErrorContext(ctx, "failed to resolve descriptor path", slog.String("generator", g.name), slog.Any("error", err))
		return err
	}
	outputDir, err := filepath.Abs(output)
	if err != nil {
		g.log.ErrorContext(ctx, "failed to resolve output directory", slog.String("generator", g.name), slog.Any("error", err))
		return err
	}

	// A plugin may assume the output directory exists and is writable, so avroc
	// creates it rather than making every generator do it. Making it *empty* and
	// private, and merging what lands in it into the project tree, is #117's.
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		g.log.ErrorContext(ctx, "failed to create output directory", slog.String("generator", g.name), slog.Any("error", err))
		return err
	}

	args := generatorArgs(descriptorPath, outputDir, options)
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
	// Standard input is unused: avroc always passes the descriptor as a path,
	// never through the "-" form the contract also allows.
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	// Standard error is the diagnostic channel, so it is read rather than
	// inherited: every line lands in avroc's structured log, attributed to the
	// generator that wrote it. An io.Writer rather than a StderrPipe on purpose
	// — exec copies into it from a goroutine that WaitDelay bounds, where a pipe
	// read of avroc's own would hang on a grandchild holding the descriptor open
	// and never reach the Wait that gives up on it.
	stderr := newStderrDiagnostics(ctx, g.log, g.name)
	cmd.Stderr = stderr
	cmd.WaitDelay = generatorWaitDelay

	runErr := cmd.Run()
	// After the wait, so that the copy is finished and the last line — which a
	// generator killed mid-write may have left without its newline — is recorded
	// rather than being the one thing avroc swallows.
	stderr.flush()
	if runErr != nil {
		return g.reportFailure(ctx, runErr)
	}

	g.log.InfoContext(ctx, "generated output", slog.String("generator", g.name), slog.String("out", outputDir))
	return nil
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
func (g generator) reportFailure(ctx context.Context, runErr error) error {
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
		g.log.ErrorContext(ctx, "generator terminated by signal",
			slog.String("generator", g.name),
			slog.String("signal", signal.String()),
			slog.Int("signal_number", int(signal)),
		)
		return fmt.Errorf("generator %q was terminated by signal %s (%d): %w", g.name, signal, int(signal), runErr)
	}

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
