// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package plugin

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// Severity is one of docs/plugin/SPEC.md's three diagnostic severities. The set
// is closed, and the spellings are the ones avroc matches, case-sensitively.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
	SeverityNote    = "note"
)

// severityFor maps a log level onto the severity a generator writes it as.
//
// Everything below warning is a note, which is what the contract leaves for a
// line that explains rather than complains. Nothing maps to no severity at all:
// a record a generator chose to emit is one avroc should record, and dropping it
// here would make the level threshold two decisions instead of one.
func severityFor(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return SeverityError
	case level >= slog.LevelWarn:
		return SeverityWarning
	default:
		return SeverityNote
	}
}

// DiagnosticHandler writes a generator's structured log to standard error in the
// diagnostic format docs/plugin/SPEC.md specifies:
//
//	<severity>: <message>
//
// It is the producing half of a format avroc parses back into its own structured
// log, and it exists so that a generator in this repository is held to the
// contract a third-party generator is held to: a diagnostic is a line, not a
// slog dump, and a user reading avroc's output sees the generator's error at
// error level rather than a verbatim line avroc could not classify.
//
// A record whose message or attributes contain a newline becomes one severity
// line followed by note: lines, which is the continuation the contract defines —
// a message MUST NOT contain a newline, and a diagnostic needing more than one
// line is written as one error: or warning: line and then notes.
type DiagnosticHandler struct {
	// mu guards w, and is shared with every handler derived from this one by
	// WithAttrs or WithGroup: they write to the same file descriptor, and a
	// diagnostic interleaved with another is a line neither avroc nor a person
	// can read.
	mu *sync.Mutex
	w  io.Writer

	level  slog.Leveler
	attrs  []slog.Attr
	groups []string
}

// NewDiagnosticHandler returns a handler writing diagnostics to w. A nil level
// means slog's own default, which records everything from info upwards: debug
// output is the generator author's, and a user running avroc has not asked for
// it.
func NewDiagnosticHandler(w io.Writer, level slog.Leveler) *DiagnosticHandler {
	if level == nil {
		level = slog.LevelInfo
	}
	return &DiagnosticHandler{
		mu:    &sync.Mutex{},
		w:     w,
		level: level,
	}
}

func (h *DiagnosticHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *DiagnosticHandler) Handle(_ context.Context, r slog.Record) error {
	var msg strings.Builder
	msg.WriteString(r.Message)

	// The handler's own attributes were qualified by the groups open when they
	// were added, so they carry no groups now; the record's are qualified here
	// by the groups open at the call.
	for _, a := range h.attrs {
		writeAttr(&msg, nil, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&msg, h.groups, a)
		return true
	})

	severity := severityFor(r.Level)

	var out strings.Builder
	for i, line := range strings.Split(msg.String(), "\n") {
		// The first line carries the record's severity and the rest continue it.
		// A record split this way is one diagnostic, which is why the notes are
		// written in the same call as the line they belong to.
		if i > 0 {
			severity = SeverityNote
		}
		out.WriteString(severity)
		out.WriteString(": ")
		out.WriteString(line)
		out.WriteString("\n")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	_, err := io.WriteString(h.w, out.String())
	return err
}

func (h *DiagnosticHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	derived := h.clone()
	for _, a := range attrs {
		derived.attrs = append(derived.attrs, qualify(h.groups, a))
	}
	return derived
}

func (h *DiagnosticHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	derived := h.clone()
	derived.groups = append(derived.groups, name)
	return derived
}

func (h *DiagnosticHandler) clone() *DiagnosticHandler {
	return &DiagnosticHandler{
		mu:     h.mu,
		w:      h.w,
		level:  h.level,
		attrs:  append([]slog.Attr(nil), h.attrs...),
		groups: append([]string(nil), h.groups...),
	}
}

// qualify prefixes an attribute's key with the groups open when it was added, so
// that a handler holding it has already resolved where it came from.
func qualify(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 {
		return a
	}
	return slog.Attr{Key: strings.Join(groups, ".") + "." + a.Key, Value: a.Value}
}

// writeAttr appends one attribute to a diagnostic's message as " key=value",
// flattening a group into one attribute per member.
//
// Empty attributes are skipped, as slog requires of a handler. The rendering is
// deliberately plain: the message is what a person reads in avroc's log, and a
// generator with something structured to say has the message itself to say it
// in.
func writeAttr(msg *strings.Builder, groups []string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}

	if a.Value.Kind() == slog.KindGroup {
		members := a.Value.Group()
		if len(members) == 0 {
			return
		}
		// A group attribute's own key opens a group for its members, and an
		// empty key adds no level at all.
		if a.Key != "" {
			groups = append(append([]string(nil), groups...), a.Key)
		}
		for _, member := range members {
			writeAttr(msg, groups, member)
		}
		return
	}

	a = qualify(groups, a)
	fmt.Fprintf(msg, " %s=%s", a.Key, a.Value.String())
}
