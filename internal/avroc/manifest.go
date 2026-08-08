// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/cli"

	"google.golang.org/protobuf/proto"
)

// manifestFilename is the fixed name of the declarative project manifest avroc
// init scaffolds and avroc generate reads.
const manifestFilename = "avroc.json"

// Manifest is the declarative description of which generators a project uses
// and how they are configured. It is the checked-in, reviewable record that
// avroc generate realizes.
type Manifest struct {
	// Inputs are IDL files shared by every generator. Each generator's
	// effective input set is these plus its own Inputs.
	Inputs     []string          `json:"inputs,omitempty"`
	Generators []GeneratorConfig `json:"generators"`
}

// GeneratorConfig describes a single generator entry in the manifest.
type GeneratorConfig struct {
	// Name is the logical generator name, i.e. the <name> in avroc-gen-<name>.
	//
	// It is the whole of how a generator is identified: avroc resolves
	// avroc-gen-<name> on PATH and runs the first match. There is no source and
	// no version here, because avroc neither fetches a generator nor pins one
	// (#125); see docs/plugin/SPEC.md, "Plugin distribution, and
	// reproducibility".
	Name string `json:"name"`
	// Out is the output directory for this generator.
	Out string `json:"out"`
	// Options is the declarative form of the -<name>_opt key=value flags.
	Options map[string]string `json:"options,omitempty"`
	// Inputs are IDL files specific to this generator, merged with the
	// top-level Manifest.Inputs.
	Inputs []string `json:"inputs,omitempty"`
}

// options converts the manifest option map into the protobuf Option slice the
// Generator service expects. Entries are sorted by key so the request (and the
// resulting generation) is deterministic.
func (g GeneratorConfig) options() []*avrocpb.Option {
	keys := slices.Sorted(maps.Keys(g.Options))
	opts := make([]*avrocpb.Option, 0, len(keys))
	for _, k := range keys {
		opts = append(opts, &avrocpb.Option{
			Name:  proto.String(k),
			Value: proto.String(g.Options[k]),
		})
	}
	return opts
}

// loadManifest reads and validates the manifest located at manifestFilename
// under workingDir. Unknown JSON fields are rejected so typos surface as errors
// rather than being silently ignored.
func loadManifest(cli cli.Context, workingDir string) (*Manifest, error) {
	data, err := fs.ReadFile(cli.OpenDir(workingDir), manifestFilename)
	if err != nil {
		return nil, err
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var m Manifest
	if err := dec.Decode(&m); err != nil {
		if name, ok := unknownFieldName(err); ok {
			if guidance, removed := removedManifestField(name); removed {
				return nil, fmt.Errorf("%s declares %q, which avroc no longer accepts: %s", manifestFilename, name, guidance)
			}
		}
		return nil, fmt.Errorf("failed to parse %s: %w", manifestFilename, err)
	}

	// Decode stops after the first JSON value; reject any trailing content so a
	// malformed or accidentally concatenated manifest surfaces as an error
	// instead of being silently ignored.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("unexpected trailing data after JSON object in %s", manifestFilename)
	}

	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// unknownFieldName pulls the field name out of encoding/json's unknown-field
// error, which DisallowUnknownFields produces as: json: unknown field "source".
//
// The package gives that error no type of its own, so the text is the only
// handle there is. It reports false for anything else, and every caller keeps
// the original error for that case, so a format change here degrades to the
// plain parse failure rather than swallowing it.
func unknownFieldName(err error) (string, bool) {
	const prefix = `json: unknown field "`

	msg := err.Error()
	i := strings.Index(msg, prefix)
	if i < 0 {
		return "", false
	}
	rest := msg[i+len(prefix):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

// removedManifestField reports what an adopter should do about a manifest field
// avroc used to accept and no longer does.
//
// DisallowUnknownFields already rejects these, so the manifest fails either way.
// What it does not do is tell the two cases apart: "unknown field" reads as a
// typo, and these were documented fields removed on purpose with nothing put in
// their place (#125). Naming the removal and the replacement is the difference
// between an adopter fixing their manifest and an adopter looking for the
// spelling that works.
func removedManifestField(name string) (string, bool) {
	switch name {
	case "source":
		return `avroc no longer fetches generators, so there is no image to name. Delete the field and put avroc-gen-<name> on PATH, or build an image FROM the avroc base image; see docs/plugin/SPEC.md, "Plugin distribution, and reproducibility"`, true
	case "version":
		return `avroc no longer pins generator versions, so there is no tag to name and no avroc.lock to write. Delete the field; the generator found on PATH is the one that runs, and an image pinned by digest is the reproducible path. See docs/plugin/SPEC.md, "Plugin distribution, and reproducibility"`, true
	default:
		return "", false
	}
}

func (m *Manifest) validate() error {
	if len(m.Generators) == 0 {
		return fmt.Errorf("%s declares no generators", manifestFilename)
	}
	for _, in := range m.Inputs {
		if !filepath.IsLocal(in) {
			return fmt.Errorf("input %q must be a relative path within the project (no absolute paths or %q traversal)", in, "..")
		}
	}
	for i, g := range m.Generators {
		if g.Name == "" {
			return fmt.Errorf("generator at index %d is missing a name", i)
		}
		if g.Out == "" {
			return fmt.Errorf("generator %q is missing an output directory (out)", g.Name)
		}
		if !filepath.IsLocal(g.Out) {
			return fmt.Errorf("generator %q output %q must be a relative path within the project (no absolute paths or %q traversal)", g.Name, g.Out, "..")
		}
		for _, in := range g.Inputs {
			if !filepath.IsLocal(in) {
				return fmt.Errorf("generator %q input %q must be a relative path within the project (no absolute paths or %q traversal)", g.Name, in, "..")
			}
		}
	}
	return nil
}

// marshalManifest renders a manifest as the canonical JSON avroc init writes:
// two-space indentation and a trailing newline.
func marshalManifest(m *Manifest) ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
