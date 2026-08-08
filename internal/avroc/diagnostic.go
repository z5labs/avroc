// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
)

// severityLevel maps one of docs/plugin/SPEC.md's three severities onto the
// level avroc records it at, reporting whether the string is a severity at all.
//
// The set is closed and matched case-sensitively, exactly as the contract writes
// it: a generator emitting "Error:" has not written a diagnostic, and guessing
// that it meant to would make the set open in practice while the spec calls it
// closed.
func severityLevel(severity string) (slog.Level, bool) {
	switch severity {
	case "error":
		return slog.LevelError, true
	case "warning":
		return slog.LevelWarn, true
	case "note":
		return slog.LevelInfo, true
	default:
		return 0, false
	}
}

// diagnosticSeparator is what stands between a severity and its message.
//
// A colon and a single space, because that is the form docs/plugin/SPEC.md
// writes and the one a generator copying the example produces. Accepting a bare
// colon as well would make "error:no such type" a diagnostic whose message
// begins mid-word, and accepting arbitrary spacing would make two generators
// that agree on the format disagree on the message.
const diagnosticSeparator = ": "

// parseDiagnostic splits one line of a generator's standard error into the
// severity and message docs/plugin/SPEC.md specifies:
//
//	<severity>: <message>
//
// It reports false for anything else, which is never an error: an unrecognised
// line is surfaced verbatim rather than discarded, so the only thing riding on
// this decision is whether the line carries a level avroc can believe.
//
// The severity occupies the start of the line — a line opening with whitespace
// is not a diagnostic — and the message runs to the end of it. A message may
// contain further colons, which is what the leading "com.example.User.field:"
// the contract asks for is made of, so the split is at the first separator and
// not the last.
func parseDiagnostic(line string) (severity, message string, ok bool) {
	severity, message, found := strings.Cut(line, diagnosticSeparator)
	if !found {
		return "", "", false
	}
	if _, known := severityLevel(severity); !known {
		return "", "", false
	}
	// A severity with nothing after it says nothing. Reporting it as a
	// diagnostic would produce an empty log record where the line was; failing
	// the match surfaces the line as it was written, which loses less.
	if message == "" {
		return "", "", false
	}
	return severity, message, true
}

// maxDiagnosticLine bounds how much of an unterminated line avroc buffers before
// it gives up waiting for the newline and records what it has.
//
// A generator that writes without ever terminating a line is misbehaving, but
// avroc holding its output in memory until it stops is avroc's bug rather than
// the generator's: the contract says nothing about how much a generator may
// write. The bound turns that into a long line recorded in pieces, which is
// still every byte the generator wrote, in order.
const maxDiagnosticLine = 1 << 20 // 1 MiB

// stderrDiagnostics is the io.Writer avroc hands a generator's standard error.
// It splits the stream into lines and records each one in avroc's structured
// log, attributed to the generator: a line of docs/plugin/SPEC.md's diagnostic
// form at the level its severity names, carrying a "severity" attribute, and
// anything else verbatim at warning level without one.
//
// Warning is the level for a line that is not a diagnostic because the line is
// most often a panic, a stack trace, or a library writing to standard error on
// its own account — what a user needs to see when a generator fails in a way its
// author did not anticipate. Info is the level an ordinary handler is configured
// at, so a handler set one notch above would drop exactly that.
//
// Lines are recorded as they arrive rather than held until the process exits,
// which is what makes a generator that hangs after complaining still tell the
// user what it complained about.
type stderrDiagnostics struct {
	// ctx is the invocation's context, held because io.Writer has no parameter
	// to pass one through and this writer's lifetime is exactly that of the
	// generator whose stream it is reading.
	ctx       context.Context
	log       *slog.Logger
	generator string

	// mu guards line. exec's copy goroutine is the only ordinary writer, but
	// flush runs on the goroutine that waited on the process, and a WaitDelay
	// that expires lets Wait return while that copy is still in progress.
	mu   sync.Mutex
	line []byte
}

func newStderrDiagnostics(ctx context.Context, log *slog.Logger, generator string) *stderrDiagnostics {
	return &stderrDiagnostics{
		ctx:       ctx,
		log:       log,
		generator: generator,
	}
}

// Write consumes whatever part of the stream it is given, which may be any
// number of whole lines, part of one, or several of each. It never reports an
// error: refusing bytes here would make the generator's own writes to standard
// error fail, and a diagnostic avroc could not record is still one the generator
// was entitled to write.
func (w *stderrDiagnostics) Write(p []byte) (int, error) {
	n := len(p)

	w.mu.Lock()
	defer w.mu.Unlock()

	for len(p) > 0 {
		// Never more than the bound in one go, so that a generator writing a
		// megabyte in a single call is held to the same limit as one writing it
		// a byte at a time.
		chunk := p
		if room := maxDiagnosticLine - len(w.line); len(chunk) > room {
			chunk = chunk[:room]
		}

		if i := bytes.IndexByte(chunk, '\n'); i >= 0 {
			w.line = append(w.line, chunk[:i]...)
			w.emitLocked()
			p = p[i+1:]
			continue
		}

		w.line = append(w.line, chunk...)
		p = p[len(chunk):]
		if len(w.line) >= maxDiagnosticLine {
			w.emitLocked()
		}
	}

	return n, nil
}

// flush records a final line that arrived without a trailing newline. It is
// called once the process has been waited on, so that a generator whose last
// diagnostic lacked its newline is not the one line avroc swallows.
func (w *stderrDiagnostics) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.emitLocked()
}

// emitLocked records the buffered line and resets the buffer. It must be called
// with mu held, and does nothing when there is nothing buffered — an empty line
// on standard error carries no information and a log record made of one is
// noise.
func (w *stderrDiagnostics) emitLocked() {
	if len(w.line) == 0 {
		return
	}
	line := string(w.line)
	w.line = w.line[:0]

	if severity, message, ok := parseDiagnostic(line); ok {
		level, _ := severityLevel(severity)
		w.log.LogAttrs(w.ctx, level, message,
			slog.String("generator", w.generator),
			slog.String("severity", severity),
		)
		return
	}

	w.log.LogAttrs(w.ctx, slog.LevelWarn, line,
		slog.String("generator", w.generator),
	)
}
