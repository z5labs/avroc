// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package plugin

import (
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// TestDiagnosticHandler is the format docs/plugin/SPEC.md specifies, from the
// producing side: one line per record, opening with one of the three
// severities, and a message that never carries a newline.
func TestDiagnosticHandler(t *testing.T) {
	testCases := []struct {
		name string
		log  func(*slog.Logger)
		want []string
	}{
		{
			name: "an error",
			log: func(l *slog.Logger) {
				l.Error("com.example.User.created_at: logical type is not supported")
			},
			want: []string{"error: com.example.User.created_at: logical type is not supported"},
		},
		{
			name: "a warning",
			log:  func(l *slog.Logger) { l.Warn("com.example.User: field order is ignored") },
			want: []string{"warning: com.example.User: field order is ignored"},
		},
		{
			name: "info is a note",
			log:  func(l *slog.Logger) { l.Info("wrote 3 files") },
			want: []string{"note: wrote 3 files"},
		},
		{
			name: "attributes are appended to the message",
			log: func(l *slog.Logger) {
				l.Error("failed to generate",
					slog.String("generator", "avroc-gen-go"),
					slog.Any("error", errors.New("package_name option is required")),
				)
			},
			want: []string{"error: failed to generate generator=avroc-gen-go error=package_name option is required"},
		},
		{
			name: "a multi-line message continues as notes",
			log:  func(l *slog.Logger) { l.Error("first\nsecond\nthird") },
			want: []string{
				"error: first",
				"note: second",
				"note: third",
			},
		},
		{
			name: "an attribute value with a newline continues as a note",
			log: func(l *slog.Logger) {
				l.Error("failed to generate", slog.Any("error", errors.New("no such type:\n\twanted int")))
			},
			want: []string{
				"error: failed to generate error=no such type:",
				"note: \twanted int",
			},
		},
		{
			name: "handler attributes precede the record's",
			log: func(l *slog.Logger) {
				l.With(slog.String("generator", "avroc-gen-pcf")).Warn("slow", slog.Int("schemas", 400))
			},
			want: []string{"warning: slow generator=avroc-gen-pcf schemas=400"},
		},
		{
			name: "a group qualifies the keys beneath it",
			log: func(l *slog.Logger) {
				l.WithGroup("descriptor").Error("unreadable", slog.Int("version", 2))
			},
			want: []string{"error: unreadable descriptor.version=2"},
		},
		{
			name: "a group attribute is flattened into its members",
			log: func(l *slog.Logger) {
				l.Error("unreadable", slog.Group("descriptor", slog.Int("version", 2), slog.String("path", "/tmp/d")))
			},
			want: []string{"error: unreadable descriptor.version=2 descriptor.path=/tmp/d"},
		},
		{
			name: "an empty attribute is skipped",
			log:  func(l *slog.Logger) { l.Error("nope", slog.Attr{}) },
			want: []string{"error: nope"},
		},
		{
			name: "debug is below the default level",
			log:  func(l *slog.Logger) { l.Debug("generated output") },
			want: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var buf strings.Builder
			tc.log(slog.New(NewDiagnosticHandler(&buf, nil)))

			var got []string
			if out := buf.String(); out != "" {
				if !strings.HasSuffix(out, "\n") {
					t.Errorf("output %q does not end in a newline; a diagnostic is a whole line", out)
				}
				got = strings.Split(strings.TrimSuffix(out, "\n"), "\n")
			}

			if len(got) != len(tc.want) {
				t.Fatalf("wrote %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("line %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestDiagnosticHandlerLevel is the threshold a generator can lower when its own
// author wants the notes it writes at debug.
func TestDiagnosticHandlerLevel(t *testing.T) {
	var buf strings.Builder
	log := slog.New(NewDiagnosticHandler(&buf, slog.LevelDebug))

	log.Debug("descriptor read", slog.Int("schemas", 2))

	if want := "note: descriptor read schemas=2\n"; buf.String() != want {
		t.Errorf("wrote %q, want %q", buf.String(), want)
	}
}

// TestDiagnosticHandlerConcurrentWrites is why the mutex is shared with every
// derived handler: two goroutines logging through handlers that differ only in
// their attributes still write to one file descriptor, and a diagnostic
// interleaved with another is a line neither avroc nor a person can read.
func TestDiagnosticHandlerConcurrentWrites(t *testing.T) {
	var buf syncBuffer
	log := slog.New(NewDiagnosticHandler(&buf, nil))

	const goroutines, perGoroutine = 8, 50

	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perGoroutine {
				log.With(slog.Int("worker", i)).Error("com.example.User: no")
			}
		}()
	}
	wg.Wait()

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != goroutines*perGoroutine {
		t.Fatalf("wrote %d lines, want %d", len(lines), goroutines*perGoroutine)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "error: com.example.User: no worker=") {
			t.Fatalf("line %q is not a whole diagnostic", line)
		}
	}
}

// syncBuffer is an io.Writer whose writes may overlap, so that a handler that
// did not serialise them would produce interleaved lines rather than a data
// race the test could only sometimes catch.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
