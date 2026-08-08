// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"fmt"
	"go/format"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/ir"
	"github.com/z5labs/avroc/internal/plugin"
)

// Generate writes one Go source file per schema in the descriptor beneath the
// invocation's output directory.
//
// It is the whole of what this generator does: docs/plugin/SPEC.md makes the
// files left in that directory on a zero exit the output of the run, so there
// is nothing to hand back and nothing to report but an error.
//
// A schema it cannot generate stops the run there, with the files from the
// schemas before it already written. That is the contract's permitted partial
// failure rather than a gap: the exit is non-zero, and avroc discards the whole
// scratch directory instead of merging it, so no half a package reaches the
// user's tree.
func Generate(req *avrocpb.GenerateRequest, w plugin.FileWriter) error {
	// The version comes first, before the options and before a single schema is
	// looked at. A descriptor written against a contract this generator does not
	// know is not one to read the recognisable parts of, and complaining about a
	// type is no use to a user whose real problem is a generator that is too old.
	if err := ir.CheckVersion(req.GetVersion()); err != nil {
		return err
	}

	var packageName string
	var encoding string
	for _, opt := range req.GetOptions() {
		switch opt.GetName() {
		case "package_name":
			packageName = opt.GetValue()
		case "encoding":
			encoding = opt.GetValue()
		}
	}
	if packageName == "" {
		return fmt.Errorf("package_name option is required")
	}

	singleObject := encoding == "single_object"
	if encoding != "" && !singleObject {
		return fmt.Errorf("unsupported encoding option: %q (supported: single_object)", encoding)
	}

	for _, schema := range req.Schemas {
		filename, content, err := buildSchemaFile(packageName, schema, singleObject)
		if err != nil {
			return fmt.Errorf("failed to generate schema: %w", err)
		}
		if err := w.WriteFile(filename, content); err != nil {
			return err
		}
	}

	return nil
}

// buildSchemaFile generates the Go source for a single schema, returning its
// relative filename and formatted content.
func buildSchemaFile(packageName string, schema *avrocpb.Schema, singleObject bool) (string, []byte, error) {
	// Refuse a descriptor this generator cannot represent before emitting any
	// of it. The code builders below cannot report a problem — they append to a
	// buffer — so a reference whose kind is unrecognised, or which claims to
	// name a primitive and does not, would otherwise be silently generated as a
	// call to a Go type that does not exist.
	if err := ir.Validate(schema); err != nil {
		return "", nil, err
	}

	// Compute fingerprint before code generation if single-object encoding is requested.
	var fp [8]byte
	if singleObject {
		var err error
		fp, err = schemaFingerprint(schema)
		if err != nil {
			return "", nil, err
		}
	}

	// Generate the Go source code
	code := generateFileCode(packageName, schema, singleObject, fp)

	// Determine the filename from the schema
	filename := ir.SnakeCase(ir.SchemaBaseName(schema)) + ".go"

	// Format the code using go/format
	formatted, err := format.Source([]byte(code))
	if err != nil {
		return "", nil, fmt.Errorf("failed to format generated code for %s: %w", filename, err)
	}

	return filename, formatted, nil
}
