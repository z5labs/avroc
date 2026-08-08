// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"fmt"
	"go/format"

	"github.com/z5labs/avroc/internal/avrocpb"
	"github.com/z5labs/avroc/internal/ir"
)

type generatorService struct {
	avrocpb.UnimplementedGeneratorServer
}

// Generate implements the Generator gRPC service method, streaming each
// generated file back to avroc, which performs the filesystem writes.
func (s *generatorService) Generate(req *avrocpb.GenerateRequest, stream avrocpb.Generator_GenerateServer) error {
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
		if err := sendFile(stream, filename, content); err != nil {
			return err
		}
	}

	return nil
}

// maxChunkSize bounds each streamed GenerateResponse so messages stay well
// under gRPC's 4MB default MaxRecvMsgSize.
const maxChunkSize = 1 << 20 // 1 MiB

// sendFile streams content to avroc as one or more chunks sharing path, with
// last set on the final chunk. Empty content emits a single terminating chunk.
func sendFile(stream avrocpb.Generator_GenerateServer, path string, content []byte) error {
	for {
		n := min(len(content), maxChunkSize)
		chunk := content[:n]
		content = content[n:]
		last := len(content) == 0

		err := stream.Send(&avrocpb.GenerateResponse{
			Path:    &path,
			Content: chunk,
			Last:    &last,
		})
		if err != nil {
			return err
		}
		if last {
			return nil
		}
	}
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
