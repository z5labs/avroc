// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"context"
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
//
// It is also the generator here with enough work in it to have parts, so it is
// the one whose phases are spans (#197): the descriptor check, the options, then
// a span per schema and a span per file. The context carries the invocation's
// span and nothing here has to know whether anything is listening — see
// plugin.StartSchemaGenerate and the phases beside it.
func Generate(ctx context.Context, req *avrocpb.GenerateRequest, w plugin.FileWriter) error {
	// The version comes first, before the options and before a single schema is
	// looked at. A descriptor written against a contract this generator does not
	// know is not one to read the recognisable parts of, and complaining about a
	// type is no use to a user whose real problem is a generator that is too old.
	_, validateSpan := plugin.StartDescriptorValidate(ctx)
	err := ir.CheckVersion(req.GetVersion())
	plugin.EndPhase(validateSpan, err)
	if err != nil {
		return err
	}

	opts, err := readOptions(ctx, req)
	if err != nil {
		return err
	}

	// Every identifier the package is about to declare, checked across the
	// whole descriptor before the loop below writes its first file: a
	// collision is a package that does not compile, and the loop cannot take
	// back the files it wrote before reaching one. checkIdentifiers is the
	// long form.
	if err := checkIdentifiers(req.GetSchemas(), opts); err != nil {
		return err
	}

	// Indexed once for the descriptor rather than per schema: a schema's
	// reference can name a fixed, or any other named type, defined in full by
	// an earlier one.
	fixed := collectFixedTypes(req.GetSchemas())
	named := collectNamedTypes(req.GetSchemas())

	for _, schema := range req.GetSchemas() {
		// Once, and before the span: it is the name the file is built from as
		// well as the name the span carries, so computing it here is what keeps
		// an untraced generation doing exactly the work it did before.
		base := ir.SchemaBaseName(schema)

		schemaCtx, schemaSpan := plugin.StartSchemaGenerate(ctx, base)

		filename, content, err := buildSchemaFile(schemaCtx, opts, base, schema, fixed, named)
		if err != nil {
			plugin.EndPhase(schemaSpan, err)
			return fmt.Errorf("failed to generate schema: %w", err)
		}

		// The write is inside the schema's span rather than beside it, so that
		// the span for a schema covers everything done for that schema whichever
		// generator opened it — the rendering is still separately readable as the
		// difference between the two. It carries its own failure and so does the
		// schema's: a write that failed is a schema this generator did not
		// produce, and a reader who has collapsed the child should still see it.
		_, writeSpan := plugin.StartFileWrite(schemaCtx, filename)
		err = w.WriteFile(filename, content)
		plugin.EndPhase(writeSpan, err)

		plugin.EndPhase(schemaSpan, err)
		if err != nil {
			return err
		}
	}

	return nil
}

// genOptions is the --opt vocabulary, read.
type genOptions struct {
	packageName  string
	singleObject bool
	// compare is compare=true: a Compare<Record>Avro for every record (#250).
	compare bool
	// views is views=true: a <Record>View for every record (#251).
	views bool
}

// readOptions reads the --opt vocabulary this generator declares, and refuses
// what it cannot honour.
//
// The single-object root check belongs here rather than in the loop below
// because it is the rest of accepting that option: encoding=single_object over a
// descriptor this generator cannot give that encoding to is an option it is
// refusing, and refusing it before the loop is what keeps a package the option
// had no effect on from being half written. checkSingleObjectRoots' own comment
// is the long form. compare=true's and views=true's scope checks are here for the
// same reason.
func readOptions(ctx context.Context, req *avrocpb.GenerateRequest) (opts genOptions, err error) {
	_, span := plugin.StartOptionsParse(ctx)
	defer func() { plugin.EndPhase(span, err) }()

	var encoding string
	for _, opt := range req.GetOptions() {
		switch opt.GetName() {
		case "package_name":
			opts.packageName = opt.GetValue()
		case "encoding":
			encoding = opt.GetValue()
		case "compare":
			opts.compare, err = parseBoolOption(opt.GetName(), opt.GetValue())
			if err != nil {
				return genOptions{}, err
			}
		case "views":
			opts.views, err = parseBoolOption(opt.GetName(), opt.GetValue())
			if err != nil {
				return genOptions{}, err
			}
		}
	}
	if opts.packageName == "" {
		return genOptions{}, fmt.Errorf("package_name option is required")
	}

	opts.singleObject = encoding == "single_object"
	if encoding != "" && !opts.singleObject {
		return genOptions{}, fmt.Errorf("unsupported encoding option: %q (supported: single_object)", encoding)
	}

	if opts.singleObject {
		if err := checkSingleObjectRoots(req.GetSchemas()); err != nil {
			return genOptions{}, err
		}
	}
	if opts.compare {
		if err := checkCompareScope(req.GetSchemas()); err != nil {
			return genOptions{}, err
		}
	}
	if opts.views {
		if err := checkViewScope(req.GetSchemas()); err != nil {
			return genOptions{}, err
		}
	}
	return opts, nil
}

// parseBoolOption reads an option whose value is exactly true or false. Any
// other spelling — True, 1, yes, the empty string — is refused rather than
// read as one of the two, because an option a person misspelled is one they
// believe is set.
func parseBoolOption(name, value string) (bool, error) {
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, fmt.Errorf("unsupported %s option: %q (supported: true, false)", name, value)
}

// buildSchemaFile generates the Go source for a single schema, returning its
// relative filename and formatted content.
//
// base is the schema's base name, computed by the caller because the span it
// opened is named by it too; deriving it again here would be work an untraced
// generation had no reason to do. fixed is the descriptor's fixed types, for
// the same reason: it is a property of every schema at once.
func buildSchemaFile(ctx context.Context, opts genOptions, base string, schema *avrocpb.Schema, fixed fixedTypes, named namedTypes) (string, []byte, error) {
	// Refuse a descriptor this generator cannot represent before emitting any
	// of it. The code builders below cannot report a problem — they append to a
	// buffer — so a reference whose kind is unrecognised, or which claims to
	// name a primitive and does not, would otherwise be silently generated as a
	// call to a Go type that does not exist.
	if err := ir.Validate(schema); err != nil {
		return "", nil, err
	}

	// Compute fingerprint before code generation if single-object encoding is
	// requested. It gets a span of its own — the one place this generator
	// computes over the IR rather than walking it, and the only per-schema cost
	// here that does not scale with the size of the file being written.
	var fp [8]byte
	if opts.singleObject {
		_, span := plugin.StartFingerprint(ctx)
		var err error
		fp, err = schemaFingerprint(schema)
		plugin.EndPhase(span, err)
		if err != nil {
			return "", nil, err
		}
	}

	// Generate the Go source code
	code, err := generateFileCode(opts, schema, fixed, named, fp)
	if err != nil {
		return "", nil, err
	}

	// Determine the filename from the schema
	filename := ir.SnakeCase(base) + ".go"

	// Format the code using go/format
	formatted, err := format.Source([]byte(code))
	if err != nil {
		return "", nil, fmt.Errorf("failed to format generated code for %s: %w", filename, err)
	}

	return filename, formatted, nil
}
