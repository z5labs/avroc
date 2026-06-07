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
	"slices"

	"github.com/z5labs/avroc/internal/avrocpb"
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
	Name string `json:"name"`
	// Source is the OCI image reference (e.g. ghcr.io/z5labs/avroc-gen-go). It
	// is recorded here and consumed by the acquisition/execution work (#69/#70);
	// avroc generate today resolves generators from PATH.
	Source string `json:"source,omitempty"`
	// Version is the OCI image tag. Recorded for #69/#70.
	Version string `json:"version,omitempty"`
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

func (m *Manifest) validate() error {
	if len(m.Generators) == 0 {
		return fmt.Errorf("%s declares no generators", manifestFilename)
	}
	for i, g := range m.Generators {
		if g.Name == "" {
			return fmt.Errorf("generator at index %d is missing a name", i)
		}
		if g.Out == "" {
			return fmt.Errorf("generator %q is missing an output directory (out)", g.Name)
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
