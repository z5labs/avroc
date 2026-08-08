// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgenpcf

import (
	"fmt"

	"github.com/z5labs/avroc/internal/avrocpb"
	"github.com/z5labs/avroc/internal/ir"
)

type generatorService struct {
	avrocpb.UnimplementedGeneratorServer
}

// Generate implements the Generator gRPC service method, streaming each
// generated file back to avroc, which performs the filesystem writes.
func (s *generatorService) Generate(req *avrocpb.GenerateRequest, stream avrocpb.Generator_GenerateServer) error {
	for _, schema := range req.Schemas {
		filename, content, err := buildSchemaFile(schema)
		if err != nil {
			return fmt.Errorf("failed to generate schema: %w", err)
		}
		if err := sendFile(stream, filename, content); err != nil {
			return err
		}
	}

	return nil
}

// buildSchemaFile generates the Avro Parsing Canonical Form for a single schema,
// returning its relative filename and content.
func buildSchemaFile(schema *avrocpb.Schema) (string, []byte, error) {
	data, err := ir.CanonicalJSON(schema)
	if err != nil {
		return "", nil, err
	}

	return ir.SnakeCase(ir.SchemaBaseName(schema)) + ".avsc", data, nil
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
