// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/ir"
)

// pluginInfoFlag is the whole of the capability-negotiation argument vector
// avroc invokes a generator with, and docs/plugin/SPEC.md accepts no other
// argument alongside it.
//
// It is written out here rather than taken from internal/plugin on purpose.
// That package is the generator's half of the contract and this is avroc's; the
// two sit either side of a process boundary, and a third-party generator
// implements the flag without importing anything from this repository. What
// keeps them honest is a test, not a shared constant — see
// TestTheDeclarationThisRepositorysGeneratorsWriteIsOneAvrocAccepts.
const pluginInfoFlag = "--plugin-info"

// pluginInfo is docs/plugin/SPEC.md's capability declaration as avroc reads it.
//
// Every member is a pointer so that "the plugin did not write this" is
// distinguishable from "the plugin wrote the zero value". Failing on a missing
// required member is most of what the handshake is for, and a declaration with
// no ir_version and one declaring ir_version 0 are different mistakes that would
// otherwise arrive here looking identical.
//
// options is a pointer to a slice for the same reason, and it is not pedantry:
// present-and-empty says the plugin accepts no options at all, while absent says
// avroc should pass whatever the manifest declared through and let the plugin
// decide. Those are opposite instructions, and a plain []string cannot tell them
// apart.
type pluginInfo struct {
	Name      *string   `json:"name"`
	Version   *string   `json:"version"`
	IRVersion *int32    `json:"ir_version"`
	Options   *[]string `json:"options"`
}

// checkGenerators runs docs/plugin/SPEC.md's capability handshake against every
// generator this run resolved, before any generation begins.
//
// Early is the entire point. The failures it catches — a plugin too old for this
// avroc's IR, a name on PATH that is not the generator the manifest asked for, an
// option the plugin has never heard of — are all failures that would otherwise
// surface as a confusing complaint about a schema, after files had already been
// written by whichever generator happened to run first.
//
// "Every generator it resolved" is every generator the manifest named and avroc
// found on PATH, not every avroc-gen-* executable discovery happened to see.
// avroc runs the generators it was asked to run and nothing else, and a
// handshake with an executable no task names would be avroc executing a file for
// no reason at all.
//
// Sequential, in task order, so that a manifest with two broken generators
// reports the same one every time rather than whichever process lost the race.
func checkGenerators(ctx context.Context, log *slog.Logger, tasks []genTask) error {
	for _, task := range tasks {
		info, err := queryPluginInfo(ctx, task.name, task.executablePath)
		if err != nil {
			return err
		}
		if err := checkPluginInfo(task.name, task.options, info); err != nil {
			return err
		}

		log.DebugContext(ctx, "generator declared its capabilities",
			slog.String("generator", task.name),
			slog.String("version", *info.Version),
			slog.Int("ir_version", int(*info.IRVersion)),
		)
	}
	return nil
}

// queryPluginInfo runs one generator's handshake and returns what it declared.
//
// A generator that does not implement the flag is handled here rather than
// crashing the run: whatever it did instead — exited non-zero, printed a usage
// line, wrote nothing — becomes a failure naming the generator and quoting what
// it wrote, which is the difference between "this plugin is too old" and a
// stack trace attributed to nobody.
func queryPluginInfo(ctx context.Context, name, executablePath string) (*pluginInfo, error) {
	cmd := exec.CommandContext(ctx, executablePath, pluginInfoFlag)
	// No descriptor is read during a handshake, so there is nothing standard
	// input could carry; a plugin blocking on it would otherwise hang the run
	// before any generation had been attempted.
	cmd.Stdin = nil

	// Both streams are captured rather than inherited. Standard output is the
	// declaration, and standard error is what a plugin that did not understand
	// the flag wrote instead — which is exactly the text the failure below has to
	// quote, and useless once it has scrolled past on avroc's own stderr.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = generatorWaitDelay

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("generator %q failed the %s handshake: %w%s",
			name, pluginInfoFlag, err, quoteReceived(stderr.Bytes(), stdout.Bytes()))
	}

	info, err := parsePluginInfo(stdout.Bytes())
	if err != nil {
		return nil, fmt.Errorf("generator %q wrote an unusable %s declaration: %w%s",
			name, pluginInfoFlag, err, quoteReceived(stdout.Bytes(), stderr.Bytes()))
	}
	return info, nil
}

// parsePluginInfo decodes the declaration a generator wrote to standard output.
//
// Unknown members are ignored rather than rejected, because docs/plugin/SPEC.md
// requires it: a plugin declaring more than this version of avroc reads is not
// thereby broken. That is the opposite of loadManifest, which does reject
// unknown fields, and the two differ because their authors do. A manifest is a
// file a person wrote and an unknown field in it is a typo; a declaration is a
// message a program wrote and a member avroc does not know is a newer plugin.
func parsePluginInfo(stdout []byte) (*pluginInfo, error) {
	dec := json.NewDecoder(bytes.NewReader(stdout))

	var info pluginInfo
	if err := dec.Decode(&info); err != nil {
		return nil, fmt.Errorf("it is not a JSON object: %w", err)
	}
	// Decode stops after the first JSON value. Standard output carries the
	// declaration and nothing else, so anything following it means the plugin
	// wrote something there it should not have — most likely a generator that
	// treated stdout as a log.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("it is followed by trailing data, and standard output carries the declaration and nothing else")
	}

	var missing []string
	if info.Name == nil {
		missing = append(missing, "name")
	}
	if info.Version == nil {
		missing = append(missing, "version")
	}
	if info.IRVersion == nil {
		missing = append(missing, "ir_version")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("it is missing the required member(s) %s", strings.Join(missing, ", "))
	}

	if *info.Name == "" {
		return nil, errors.New(`its "name" is empty, and a generator name never is`)
	}
	return &info, nil
}

// checkPluginInfo holds a declaration against the generator avroc resolved, the
// descriptor avroc is about to write, and the options the manifest declared.
//
// Each failure names the generator, because a run with several of them reports
// one line and the user has to know which plugin to go and look at.
func checkPluginInfo(execName string, options []*avrocpb.Option, info *pluginInfo) error {
	if declared := "avroc-gen-" + *info.Name; declared != execName {
		return fmt.Errorf("generator %q declares the name %q, which is %q: the file on PATH is not the generator the manifest asked for, and generating with it would attribute its output to the wrong plugin",
			execName, *info.Name, declared)
	}

	// The descriptor's version is the one this avroc stamps on every descriptor
	// it emits, so it is known here without building one. A plugin that
	// understands a *higher* version than avroc writes is fine: it is newer than
	// this avroc, and reading an older descriptor is what a monotonic version
	// makes possible.
	if ir.Version > *info.IRVersion {
		return fmt.Errorf("generator %q understands IR version %d, but this avroc writes descriptors at IR version %d: the generator is too old for it",
			execName, *info.IRVersion, ir.Version)
	}

	// An absent options member is a plugin declining to declare a vocabulary, and
	// docs/plugin/SPEC.md leaves the decision to the plugin in that case. Present
	// and empty is a plugin saying it accepts none, which is a declaration and not
	// a silence.
	if info.Options == nil {
		return nil
	}
	for _, opt := range options {
		if slices.Contains(*info.Options, opt.GetName()) {
			continue
		}
		return fmt.Errorf("generator %q does not accept the option %q the manifest declares; it accepts %v",
			execName, opt.GetName(), *info.Options)
	}
	return nil
}

// maxQuotedBytes bounds how much of a generator's output a failure report
// quotes.
//
// A plugin that does not implement the handshake usually prints a usage line,
// but one that ignored the flag and generated anyway can print a great deal, and
// a diagnostic is worth nothing once it has scrolled the real one out of view.
const maxQuotedBytes = 512

// quoteReceived renders the first of streams that carried anything, for a
// failure whose whole job is to say what avroc received instead of a
// declaration.
//
// The streams are given most-likely-first by the caller, because which one holds
// the explanation depends on how the plugin failed: a plugin that rejected the
// flag wrote to standard error, and one that wrote something unparseable wrote
// it to standard output.
func quoteReceived(streams ...[]byte) string {
	for _, s := range streams {
		text := strings.TrimSpace(string(s))
		if text == "" {
			continue
		}
		if len(text) > maxQuotedBytes {
			// ToValidUTF8 because the cut is at a byte offset and may land in the
			// middle of a rune, which would otherwise be quoted as an escape that
			// looks like the plugin's own output rather than avroc's truncation.
			text = strings.ToValidUTF8(text[:maxQuotedBytes], "") + "..."
		}
		return "; it wrote " + strconv.Quote(text)
	}
	return "; it wrote nothing"
}
