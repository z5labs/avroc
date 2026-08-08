// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

// Command ir-descriptor-set writes the IR's protobuf FileDescriptorSet — the
// published ir.binpb — to a file.
//
// It is the pipeline's hands and nothing more. Everything about what the set
// contains, what order it is in and why its bytes are stable is
// avrocpb.FileDescriptorSet's; this program exists so that a Dagger function has
// something to run, and so that the artifact is produced by compiling this
// repository rather than by somebody remembering to run protoc and commit the
// output. There is no committed .binpb for that reason: the set is a function of
// proto/, and the only way to keep a second copy of it honest is not to have
// one.
//
// It lives under internal/ deliberately. It is not part of avroc's published
// surface, nothing outside this repository has a reason to run it, and putting
// it in cmd/ would make a build tool look like a fifth shipped binary.
//
//	go run ./internal/tools/ir-descriptor-set -o ir.binpb
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/z5labs/avroc/avrocpb"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "ir-descriptor-set: %v\n", err)
		os.Exit(1)
	}
}

// run is separated from main so that the exit path is the only thing main owns.
func run(args []string) error {
	flags := flag.NewFlagSet("ir-descriptor-set", flag.ContinueOnError)
	out := flags.String("o", "", "path to write the FileDescriptorSet to (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("-o is required: name the file to write")
	}
	if rest := flags.Args(); len(rest) > 0 {
		return fmt.Errorf("unexpected arguments %v", rest)
	}

	b, err := avrocpb.MarshalFileDescriptorSet()
	if err != nil {
		return err
	}

	// The parent directory is created rather than required, because the caller
	// is a pipeline naming an output path in a fresh container filesystem —
	// /out/ir.binpb has no /out until something makes one, and failing there
	// would be this program reporting on the shape of its caller's environment
	// instead of doing its job.
	if dir := filepath.Dir(*out); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create %s: %w", dir, err)
		}
	}

	if err := os.WriteFile(*out, b, 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", *out, err)
	}
	return nil
}
