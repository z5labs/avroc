// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package plugin

import (
	"encoding/json"
	"io"
	"runtime/debug"

	"github.com/z5labs/avroc/internal/ir"
)

// PluginInfoFlag is the whole of the capability-negotiation argument vector:
//
//	avroc-gen-<name> --plugin-info
//
// docs/plugin/SPEC.md accepts no other argument alongside it, so a vector
// carrying one is an error rather than something to ignore. avroc's half — the
// flag it passes and the declaration it reads back — is internal/avroc's.
const PluginInfoFlag = "--plugin-info"

// Info is the capability declaration docs/plugin/SPEC.md's "Capability
// negotiation" specifies: what a plugin supports, said before it is handed any
// work, so that a mismatch fails early and by name instead of halfway through a
// generation that was never going to succeed.
//
// It is JSON while the descriptor is protobuf, and the split is deliberate.
// This is the one message a plugin has to produce before it has demonstrated
// any tooling at all, and a shell script emits it with a single printf;
// requiring protobuf here would mean a plugin needed a protobuf runtime in
// order to say that it does not have one.
type Info struct {
	// Name is the generator's <name> — the suffix of the avroc-gen-<name>
	// executable avroc resolved, not the whole filename. avroc fails the run
	// when the two disagree, because a mismatch means the file on PATH is not
	// the generator the manifest asked for and its output would be attributed to
	// the wrong plugin.
	Name string `json:"name"`

	// Version is the plugin's own version, in whatever scheme its author uses.
	// avroc never interprets it, compares it or decides anything from it: it
	// exists to be reported in a log and quoted in a bug report.
	Version string `json:"version"`

	// IRVersion is the highest IR version this plugin understands, in the sense
	// of docs/ir/SPEC.md's version field. avroc compares it against the version
	// of the descriptor it is about to write and fails the run before generating
	// anything when the descriptor's is higher.
	//
	// It does not relieve the plugin of the check ir.CheckVersion performs at
	// generation time: a plugin is entitled to be run by something other than
	// this version of avroc.
	IRVersion int32 `json:"ir_version"`

	// Options are the --opt keys the plugin accepts.
	//
	// Present and empty says the plugin accepts none, which lets avroc reject a
	// misplaced manifest option before anything runs. Absent says avroc should
	// pass the manifest's options through and let the plugin decide, which is
	// what the contract already requires of it. Those are opposite instructions,
	// so the member is always written by the generators here — every one of them
	// knows its own vocabulary, and a generator that accepts no options has to
	// be able to say so rather than fall into the other case by writing nothing.
	Options []string `json:"options"`
}

// NewInfo is the declaration for a generator built from this repository: its
// own name and option vocabulary, this build's version, and the IR version
// internal/ir pins.
//
// name is the generator's <name> and not the executable's filename — "go", not
// "avroc-gen-go". The two are connected by nothing but that suffix, in either
// direction, so deriving the filename here (see Executable) is what keeps the
// declaration and the executable that wrote it from being able to disagree.
func NewInfo(name string, options ...string) Info {
	return Info{
		Name:      name,
		Version:   pluginVersion(),
		IRVersion: ir.Version,
		Options:   normalizeOptions(options),
	}
}

// Executable is the filename avroc resolves this generator's name to.
func (i Info) Executable() string {
	return "avroc-gen-" + i.Name
}

// normalizeOptions turns a generator that declared no options into an empty
// declared vocabulary rather than an absent one.
//
// The distinction survives all the way to avroc's reader, and a nil slice would
// marshal to JSON null — which is neither of the two things the member can say.
func normalizeOptions(options []string) []string {
	if options == nil {
		return []string{}
	}
	return options
}

// pluginVersion is the version a generator built from this repository declares.
//
// It is read out of the build rather than written down as a constant here,
// because a constant is a second place to remember on release day and goes
// stale silently — and a version quoted in a bug report is worth nothing once it
// has. It is fixed for a given executable, which is the whole of what
// docs/plugin/SPEC.md asks of it: the declaration must be identical across
// invocations of the same executable.
func pluginVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi.Main.Version == "" {
		return "(devel)"
	}
	return bi.Main.Version
}

// WriteInfo writes the declaration to w as a single JSON object and nothing
// else.
//
// docs/plugin/SPEC.md gives standard output to this one message during the
// whole of a plugin's life, which is what makes the declaration parseable
// without a mode flag and a generation invocation safe to put in a pipeline.
func WriteInfo(w io.Writer, info Info) error {
	info.Options = normalizeOptions(info.Options)

	// Indented because a person runs --plugin-info by hand more often than a
	// program does, and the field order is the struct's either way — the
	// declaration has to be byte-identical across invocations, so nothing here
	// may range over a map.
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}

	_, err = w.Write(append(b, '\n'))
	return err
}
