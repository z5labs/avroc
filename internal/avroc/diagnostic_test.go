// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/z5labs/avroc/internal/plugin"
)

// TestParseDiagnostic pins docs/plugin/SPEC.md's diagnostic format: the closed
// set of three severities, matched case-sensitively, a colon and a space, and a
// message that runs to the end of the line.
func TestParseDiagnostic(t *testing.T) {
	testCases := []struct {
		name         string
		line         string
		wantSeverity string
		wantMessage  string
		wantOK       bool
	}{
		{
			name:         "an error naming the field it is about",
			line:         `error: com.example.User.created_at: logical type "duration" is not supported`,
			wantSeverity: "error",
			wantMessage:  `com.example.User.created_at: logical type "duration" is not supported`,
			wantOK:       true,
		},
		{
			name:         "a warning",
			line:         "warning: com.example.User: field order is ignored",
			wantSeverity: "warning",
			wantMessage:  "com.example.User: field order is ignored",
			wantOK:       true,
		},
		{
			name:         "a note continuing a diagnostic",
			line:         "note: com.example.User.created_at: declared as fixed(12)",
			wantSeverity: "note",
			wantMessage:  "com.example.User.created_at: declared as fixed(12)",
			wantOK:       true,
		},
		{
			name:        "a severity is matched case-sensitively",
			line:        "Error: something went wrong",
			wantMessage: "",
		},
		{
			name: "a severity outside the closed set of three",
			line: "fatal: something went wrong",
		},
		{
			name: "a colon with no space after it",
			line: "error:something went wrong",
		},
		{
			name: "a severity with no message",
			line: "error: ",
		},
		{
			name: "a line opening with whitespace is not a diagnostic",
			line: "  error: indented by a stack trace",
		},
		{
			name: "a panic",
			line: "panic: runtime error: invalid memory address or nil pointer dereference",
		},
		{
			name: "a line with no colon at all",
			line: "goroutine 1 [running]:",
		},
		{
			name: "an empty line",
			line: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			severity, message, ok := parseDiagnostic(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("parseDiagnostic(%q) ok = %t, want %t", tc.line, ok, tc.wantOK)
			}
			if severity != tc.wantSeverity {
				t.Errorf("severity = %q, want %q", severity, tc.wantSeverity)
			}
			if message != tc.wantMessage {
				t.Errorf("message = %q, want %q", message, tc.wantMessage)
			}
		})
	}
}

// TestSeverityLevel is the mapping avroc records each severity at, and the
// closedness of the set on the other side of it.
func TestSeverityLevel(t *testing.T) {
	testCases := []struct {
		severity string
		want     slog.Level
		wantOK   bool
	}{
		{severity: "error", want: slog.LevelError, wantOK: true},
		{severity: "warning", want: slog.LevelWarn, wantOK: true},
		{severity: "note", want: slog.LevelInfo, wantOK: true},
		{severity: "fatal"},
		{severity: "ERROR"},
		{severity: ""},
	}

	for _, tc := range testCases {
		t.Run(tc.severity, func(t *testing.T) {
			level, ok := severityLevel(tc.severity)
			if ok != tc.wantOK {
				t.Fatalf("severityLevel(%q) ok = %t, want %t", tc.severity, ok, tc.wantOK)
			}
			if ok && level != tc.want {
				t.Errorf("level = %v, want %v", level, tc.want)
			}
		})
	}
}

// TestStderrDiagnostics is what avroc does with a generator's standard error:
// every line recorded, a diagnostic at the level its severity names, anything
// else verbatim, and nothing held back or discarded.
func TestStderrDiagnostics(t *testing.T) {
	t.Run("a diagnostic is recorded at the level its severity names", func(t *testing.T) {
		log, records := recordingLogger()
		w := newStderrDiagnostics(context.Background(), log, testGeneratorName)

		writeAll(t, w, "error: com.example.User: no\nwarning: com.example.User: hmm\nnote: because\n")

		got := records()
		if len(got) != 3 {
			t.Fatalf("recorded %d lines, want 3: %v", len(got), got)
		}

		wantLevels := []slog.Level{slog.LevelError, slog.LevelWarn, slog.LevelInfo}
		wantSeverities := []string{"error", "warning", "note"}
		wantMessages := []string{"com.example.User: no", "com.example.User: hmm", "because"}
		for i, r := range got {
			if r.level != wantLevels[i] {
				t.Errorf("line %d level = %v, want %v", i, r.level, wantLevels[i])
			}
			if r.message != wantMessages[i] {
				t.Errorf("line %d message = %q, want %q", i, r.message, wantMessages[i])
			}
			if r.attrs["severity"] != wantSeverities[i] {
				t.Errorf("line %d severity attribute = %q, want %q", i, r.attrs["severity"], wantSeverities[i])
			}
			// Attributed to the generator that wrote it, which is the only thing
			// that tells two generators' output apart in one log.
			if r.attrs["generator"] != testGeneratorName {
				t.Errorf("line %d generator attribute = %q, want %q", i, r.attrs["generator"], testGeneratorName)
			}
		}
	})

	t.Run("an unparseable line is surfaced verbatim", func(t *testing.T) {
		log, records := recordingLogger()
		w := newStderrDiagnostics(context.Background(), log, testGeneratorName)

		const panicLine = "panic: runtime error: index out of range [3] with length 2"
		writeAll(t, w, panicLine+"\ngoroutine 1 [running]:\n")

		got := records()
		if len(got) != 2 {
			t.Fatalf("recorded %d lines, want 2: %v", len(got), got)
		}
		if got[0].message != panicLine {
			t.Errorf("message = %q, want the line unchanged %q", got[0].message, panicLine)
		}
		// Warning, because a line that is not a diagnostic is most often what a
		// user most needs to see, and info is the level a handler is ordinarily
		// configured at.
		if got[0].level != slog.LevelWarn {
			t.Errorf("level = %v, want %v", got[0].level, slog.LevelWarn)
		}
		if _, ok := got[0].attrs["severity"]; ok {
			t.Error("a line that is not a diagnostic was given a severity")
		}
		if got[0].attrs["generator"] != testGeneratorName {
			t.Errorf("generator attribute = %q, want %q", got[0].attrs["generator"], testGeneratorName)
		}
	})

	t.Run("a line split across writes is one line", func(t *testing.T) {
		log, records := recordingLogger()
		w := newStderrDiagnostics(context.Background(), log, testGeneratorName)

		for _, chunk := range []string{"err", "or: com.exa", "mple.User: no\nnote: ", "and here is why\n"} {
			writeAll(t, w, chunk)
		}

		got := records()
		if len(got) != 2 {
			t.Fatalf("recorded %d lines, want 2: %v", len(got), got)
		}
		if got[0].message != "com.example.User: no" || got[0].level != slog.LevelError {
			t.Errorf("first record = %v, want the reassembled error", got[0])
		}
		if got[1].message != "and here is why" || got[1].level != slog.LevelInfo {
			t.Errorf("second record = %v, want the note", got[1])
		}
	})

	t.Run("a last line without its newline is not swallowed", func(t *testing.T) {
		log, records := recordingLogger()
		w := newStderrDiagnostics(context.Background(), log, testGeneratorName)

		writeAll(t, w, "error: killed before the newline")
		if got := records(); len(got) != 0 {
			t.Fatalf("recorded %d lines before the flush, want 0: %v", len(got), got)
		}

		w.flush()

		got := records()
		if len(got) != 1 {
			t.Fatalf("recorded %d lines, want 1: %v", len(got), got)
		}
		if got[0].message != "killed before the newline" {
			t.Errorf("message = %q, want the unterminated line", got[0].message)
		}

		// Flushing again records nothing: the buffer is empty and a repeated
		// line would be a diagnostic the generator never wrote.
		w.flush()
		if got := records(); len(got) != 1 {
			t.Errorf("recorded %d lines after a second flush, want 1", len(got))
		}
	})

	t.Run("an empty line records nothing", func(t *testing.T) {
		log, records := recordingLogger()
		w := newStderrDiagnostics(context.Background(), log, testGeneratorName)

		writeAll(t, w, "\n\n")
		w.flush()

		if got := records(); len(got) != 0 {
			t.Errorf("recorded %d lines for blank input, want 0: %v", len(got), got)
		}
	})

	t.Run("a line that never ends is recorded in pieces", func(t *testing.T) {
		log, records := recordingLogger()
		w := newStderrDiagnostics(context.Background(), log, testGeneratorName)

		writeAll(t, w, strings.Repeat("x", maxDiagnosticLine+10))

		got := records()
		if len(got) != 1 {
			t.Fatalf("recorded %d lines, want the buffer to have been given up on once", len(got))
		}
		if len(got[0].message) != maxDiagnosticLine {
			t.Errorf("recorded %d bytes, want the %d-byte bound", len(got[0].message), maxDiagnosticLine)
		}

		w.flush()
		if got := records(); len(got) != 2 || len(got[1].message) != 10 {
			t.Errorf("the remainder of an unterminated line was lost: %v", got)
		}
	})

	t.Run("Write reports every byte consumed", func(t *testing.T) {
		log, _ := recordingLogger()
		w := newStderrDiagnostics(context.Background(), log, testGeneratorName)

		const p = "error: one\npartial"
		n, err := w.Write([]byte(p))
		if err != nil {
			t.Fatalf("Write returned an error, which would fail the generator's own write: %v", err)
		}
		if n != len(p) {
			t.Errorf("Write returned %d, want %d", n, len(p))
		}
	})
}

// TestGeneratorGenerateDiagnostics is the parsing on the real fork/exec path:
// what a generator writes to standard error reaches avroc's structured log,
// classified, attributed, and with the lines it could not classify intact.
func TestGeneratorGenerateDiagnostics(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	log, records := recordingLogger()
	g := testGenerator(t, writeShellGenerator(t, `
echo 'warning: com.example.User.id: field order is ignored' >&2
echo 'note: com.example.User.id: declared as int' >&2
echo 'Traceback (most recent call last):' >&2
printf 'error: com.example.User: unterminated' >&2
`))
	g.log = log

	projectRoot, outputDir := newProject(t)
	if err := generateOne(ctx, g, projectRoot, outputDir, nil, testSchema("User")); err != nil {
		t.Fatalf("a generator that exited zero after complaining failed the run: %v", err)
	}

	byMessage := make(map[string]recordedLine)
	for _, r := range records() {
		byMessage[r.message] = r
	}

	testCases := []struct {
		message      string
		wantLevel    slog.Level
		wantSeverity string
	}{
		{
			message:      "com.example.User.id: field order is ignored",
			wantLevel:    slog.LevelWarn,
			wantSeverity: "warning",
		},
		{
			message:      "com.example.User.id: declared as int",
			wantLevel:    slog.LevelInfo,
			wantSeverity: "note",
		},
		{
			// Not a diagnostic, and surfaced rather than swallowed.
			message:   "Traceback (most recent call last):",
			wantLevel: slog.LevelWarn,
		},
		{
			// The generator exited without terminating its last line.
			message:      "com.example.User: unterminated",
			wantLevel:    slog.LevelError,
			wantSeverity: "error",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.message, func(t *testing.T) {
			got, ok := byMessage[tc.message]
			if !ok {
				t.Fatalf("nothing in the log carries %q", tc.message)
			}
			if got.level != tc.wantLevel {
				t.Errorf("level = %v, want %v", got.level, tc.wantLevel)
			}
			if got.attrs["severity"] != tc.wantSeverity {
				t.Errorf("severity attribute = %q, want %q", got.attrs["severity"], tc.wantSeverity)
			}
			if got.attrs["generator"] != testGeneratorName {
				t.Errorf("generator attribute = %q, want %q", got.attrs["generator"], testGeneratorName)
			}
		})
	}
}

// TestGeneratorGenerateSignal is docs/plugin/SPEC.md's other failure: a
// generator killed by a signal is reported as killed by that signal, naming it,
// and distinguishably from one that exited non-zero.
func TestGeneratorGenerateSignal(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	log, records := recordingLogger()
	g := testGenerator(t, writeShellGenerator(t, "kill -TERM $$\n"))
	g.log = log

	projectRoot, outputDir := newProject(t)
	err := generateOne(ctx, g, projectRoot, outputDir, nil, testSchema("User"))
	if err == nil {
		t.Fatal("generate reported success for a generator killed by a signal")
	}
	if !strings.Contains(err.Error(), "terminated by signal") {
		t.Errorf("error %q does not say the generator was terminated by a signal", err)
	}
	if !strings.Contains(err.Error(), "terminated") {
		t.Errorf("error %q does not name the signal", err)
	}
	if !strings.Contains(err.Error(), testGeneratorName) {
		t.Errorf("error %q does not name the generator", err)
	}

	var reported bool
	for _, r := range records() {
		if r.message != "generator terminated by signal" {
			continue
		}
		reported = true
		if r.attrs["signal"] != "terminated" {
			t.Errorf("signal attribute = %q, want %q", r.attrs["signal"], "terminated")
		}
		if r.attrs["generator"] != testGeneratorName {
			t.Errorf("generator attribute = %q, want %q", r.attrs["generator"], testGeneratorName)
		}
		if _, ok := r.attrs["exit_code"]; ok {
			t.Error("a generator killed by a signal was reported with an exit code")
		}
	}
	if !reported {
		t.Errorf("nothing in the log reports the signal: %v", records())
	}
}

// TestDiagnosticAgreement holds the two halves of the format to each other: what
// internal/plugin writes for a generator is what avroc parses back, at the level
// the generator meant.
//
// The two are separate packages either side of a process boundary and neither
// imports the other, so nothing but a test like this notices when one of them
// moves. A drift here is a generator whose errors reach the user as unclassified
// lines — visible, which is why it would survive a long time.
func TestDiagnosticAgreement(t *testing.T) {
	testCases := []struct {
		name         string
		log          func(*slog.Logger)
		wantSeverity string
		wantLevel    slog.Level
		wantMessage  string
	}{
		{
			name:         "an error a generator refused work with",
			log:          func(l *slog.Logger) { l.Error("com.example.User: package_name option is required") },
			wantSeverity: "error",
			wantLevel:    slog.LevelError,
			wantMessage:  "com.example.User: package_name option is required",
		},
		{
			name:         "a warning",
			log:          func(l *slog.Logger) { l.Warn("com.example.User: field order is ignored") },
			wantSeverity: "warning",
			wantLevel:    slog.LevelWarn,
			wantMessage:  "com.example.User: field order is ignored",
		},
		{
			name:         "a note carrying attributes",
			log:          func(l *slog.Logger) { l.Info("descriptor read", slog.Int("schemas", 2)) },
			wantSeverity: "note",
			wantLevel:    slog.LevelInfo,
			wantMessage:  "descriptor read schemas=2",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var written strings.Builder
			tc.log(slog.New(plugin.NewDiagnosticHandler(&written, nil)))

			log, records := recordingLogger()
			w := newStderrDiagnostics(context.Background(), log, testGeneratorName)
			writeAll(t, w, written.String())
			w.flush()

			got := records()
			if len(got) != 1 {
				t.Fatalf("%q parsed into %d records, want 1: %v", written.String(), len(got), got)
			}
			if got[0].attrs["severity"] != tc.wantSeverity {
				t.Errorf("severity = %q, want %q", got[0].attrs["severity"], tc.wantSeverity)
			}
			if got[0].level != tc.wantLevel {
				t.Errorf("level = %v, want %v", got[0].level, tc.wantLevel)
			}
			if got[0].message != tc.wantMessage {
				t.Errorf("message = %q, want %q", got[0].message, tc.wantMessage)
			}
		})
	}
}

// recordedLine is one slog record, reduced to what these tests assert on.
type recordedLine struct {
	level   slog.Level
	message string
	attrs   map[string]string
}

func (r recordedLine) String() string {
	return fmt.Sprintf("%v %q %v", r.level, r.message, r.attrs)
}

// recordingLogger returns a logger that keeps every record, and the accessor
// that reads them back. The accessor copies, so a caller ranging over it cannot
// race the generator's stderr goroutine still writing into the handler.
func recordingLogger() (*slog.Logger, func() []recordedLine) {
	h := &recordingHandler{}
	return slog.New(h), h.records
}

type recordingHandler struct {
	mu    sync.Mutex
	lines []recordedLine
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	line := recordedLine{
		level:   r.Level,
		message: r.Message,
		attrs:   make(map[string]string),
	}
	r.Attrs(func(a slog.Attr) bool {
		line.attrs[a.Key] = a.Value.String()
		return true
	})

	h.mu.Lock()
	defer h.mu.Unlock()
	h.lines = append(h.lines, line)
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingHandler) records() []recordedLine {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]recordedLine(nil), h.lines...)
}

func writeAll(t *testing.T, w *stderrDiagnostics, s string) {
	t.Helper()

	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
}
