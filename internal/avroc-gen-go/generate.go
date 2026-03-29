// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"context"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"

	"github.com/z5labs/avroc/internal/avrocpb"
)

type generatorService struct {
	avrocpb.UnimplementedGeneratorServer
}

// Generate implements the Generator gRPC service method.
func (s *generatorService) Generate(ctx context.Context, req *avrocpb.GenerateRequest) (*avrocpb.GenerateResponse, error) {
	outputDir := req.GetOutputDirectory()
	if outputDir == "" {
		return nil, fmt.Errorf("output directory is required")
	}

	// Ensure output directory exists
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	var outputFiles []string

	for _, schema := range req.Schemas {
		filename, err := generateSchemaFile(outputDir, schema)
		if err != nil {
			return nil, fmt.Errorf("failed to generate schema: %w", err)
		}
		outputFiles = append(outputFiles, filename)
	}

	return &avrocpb.GenerateResponse{
		OutputFiles: outputFiles,
	}, nil
}

// generateSchemaFile generates a Go file for a single schema.
func generateSchemaFile(outputDir string, schema *avrocpb.Schema) (string, error) {
	// Generate the Go source code
	code := generateFileCode(schema)

	// Format the code using go/format
	formatted, err := format.Source([]byte(code))
	if err != nil {
		// If formatting fails, write the unformatted code for debugging
		formatted = []byte(code)
	}

	// Determine the filename from the schema
	filename := schemaFilename(schema)
	filepath := filepath.Join(outputDir, filename)

	// Write the file
	if err := os.WriteFile(filepath, formatted, 0o644); err != nil {
		return "", fmt.Errorf("failed to write file %s: %w", filepath, err)
	}

	return filepath, nil
}

// schemaFilename determines the output filename for a schema.
func schemaFilename(schema *avrocpb.Schema) string {
	// Try to get a name from the primary type
	if schema.Type != nil {
		if name := typeName(schema.Type); name != "" {
			return toSnakeCase(name) + ".go"
		}
	}

	// Try to get a name from the first type
	if len(schema.Types) > 0 {
		if name := typeName(schema.Types[0]); name != "" {
			return toSnakeCase(name) + ".go"
		}
	}

	// Fall back to namespace-based name
	if ns := schema.GetNamespace(); ns != "" {
		parts := strings.Split(ns, ".")
		return toSnakeCase(parts[len(parts)-1]) + ".go"
	}

	return "schema.go"
}

// typeName extracts the name from a type.
func typeName(t *avrocpb.Type) string {
	if t == nil {
		return ""
	}

	switch v := t.Type.(type) {
	case *avrocpb.Type_Record:
		return v.Record.GetName()
	case *avrocpb.Type_EnumType:
		return v.EnumType.GetName()
	case *avrocpb.Type_Fixed:
		return v.Fixed.GetName()
	default:
		return ""
	}
}

// toSnakeCase converts a string to snake_case.
func toSnakeCase(s string) string {
	if s == "" {
		return ""
	}

	// Handle fully-qualified names
	if idx := strings.LastIndex(s, "."); idx != -1 {
		s = s[idx+1:]
	}

	var result strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			result.WriteByte('_')
		}
		result.WriteRune(r)
	}

	return strings.ToLower(result.String())
}
