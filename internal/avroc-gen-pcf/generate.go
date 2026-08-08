// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgenpcf

import (
	"fmt"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/ir"
	"github.com/z5labs/avroc/internal/plugin"
)

// Generate writes one canonical .avsc file per schema in the descriptor beneath
// the invocation's output directory.
//
// It is the whole of what this generator does: docs/plugin/SPEC.md makes the
// files left in that directory on a zero exit the output of the run, so there
// is nothing to hand back and nothing to report but an error.
//
// A schema it cannot generate stops the run there, with the files from the
// schemas before it already written. That is the contract's permitted partial
// failure rather than a gap: the exit is non-zero, and avroc discards the whole
// scratch directory instead of merging it, so no half a set of schemas reaches
// the user's tree.
func Generate(req *avrocpb.GenerateRequest, w plugin.FileWriter) error {
	// The version comes first, before a single schema is looked at. A descriptor
	// written against a contract this generator does not know is not one to read
	// the recognisable parts of — and a canonical form derived from a misread
	// schema fingerprints to something nothing else agrees with.
	if err := ir.CheckVersion(req.GetVersion()); err != nil {
		return err
	}

	for _, schema := range req.Schemas {
		filename, content, err := buildSchemaFile(schema)
		if err != nil {
			return fmt.Errorf("failed to generate schema: %w", err)
		}
		if err := w.WriteFile(filename, content); err != nil {
			return err
		}
	}

	return nil
}

// buildSchemaFile generates the Avro Parsing Canonical Form for a single schema,
// returning its relative filename and content.
//
// The content is ir.CanonicalJSON's bytes unchanged, and that is the point of
// this generator: those are the same bytes avroc-gen-go fingerprints, so the
// published schema and the embedded fingerprint cannot disagree. Nothing is
// appended — Avro's Parsing Canonical Form has no trailing newline, and one
// added here would change every fingerprint computed over the published file.
func buildSchemaFile(schema *avrocpb.Schema) (string, []byte, error) {
	data, err := ir.CanonicalJSON(schema)
	if err != nil {
		return "", nil, err
	}

	return ir.SnakeCase(ir.SchemaBaseName(schema)) + ".avsc", data, nil
}
