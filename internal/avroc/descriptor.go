// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/ir"

	"google.golang.org/protobuf/proto"
)

// descriptorFilename is the name avroc gives the descriptor inside the private
// directory it creates for one generator invocation.
//
// Fixed rather than randomised, because the directory already is: no two
// invocations share one, so there is nothing left for the file's name to
// disambiguate. docs/plugin/SPEC.md forbids a plugin deriving anything from
// either the name or the directory, so this constant is a convenience for the
// person reading a log line or saving the bytes, and no part of the contract.
//
// The .binpb suffix is protobuf's own for the binary wire encoding, which is
// what the file holds. The JSON rendering a person reads is a separate artifact
// — avroc inspect produces it on demand — and a plugin is never handed one; a
// plugin that accepted both would have to sniff which it had.
const descriptorFilename = "descriptor.binpb"

// descriptorFileMode is the mode avroc creates a descriptor with.
//
// docs/plugin/SPEC.md forbids a plugin writing to the descriptor, and a mode bit
// does not enforce that — the plugin usually runs as the user that created the
// file and can chmod it back. What it does is turn the accidental case, a plugin
// opening the path for writing, into an error where the mistake is rather than
// into a descriptor quietly different from the one avroc wrote.
const descriptorFileMode os.FileMode = 0o444

// newDescriptorDir creates the private directory one generator invocation's
// descriptor is written into, and which the path at --descriptor names.
//
// docs/plugin/SPEC.md's "Location and lifetime" is what this implements: a
// directory created for that one invocation and nothing else, never shared
// between two generators or two runs, so a descriptor on disk belongs to exactly
// one generator and one run and cannot have been overwritten by whichever
// invocation happened to finish last.
//
// It is created *inside* the generator's own output tree, beside the scratch
// directory --out names (newScratchDir), rather than under os.TempDir (#218).
// avroc reads no ambient temporary directory at all, which is what lets the
// published image be scratch plus the files docs/container/SPEC.md names — with
// no /tmp in it, and no --tmpfs on the `docker run` line an adopter copies. The
// leading dot keeps it out of a shell glob for the moment it exists, and the name
// says what left it behind if a hard kill ever stops the removal from running,
// exactly as the scratch directory's does.
//
// Nothing here is a promise about the path: docs/plugin/SPEC.md forbids a plugin
// deriving anything from the descriptor's name or its directory, so a plugin that
// notices this location has already broken that rule rather than found a feature.
func newDescriptorDir(output, generatorName string) (string, error) {
	dir, err := os.MkdirTemp(output, "."+generatorName+"-descriptor-")
	if err != nil {
		return "", fmt.Errorf("failed to create descriptor directory: %w", err)
	}
	return dir, nil
}

// newDescriptor builds the descriptor for a single generator invocation: the IR
// contract version, that generator's own options, and the resolved schemas it
// was asked to generate from.
//
// It is built once per invocation and then both written and sent, so the file on
// disk is the value the generator received rather than a second encoding of the
// same inputs that could drift from it.
func newDescriptor(options []*avrocpb.Option, schemas []*avrocpb.Schema) *avrocpb.GenerateRequest {
	return &avrocpb.GenerateRequest{
		// Every descriptor avroc emits carries the version of the contract it
		// was written against, so a generator built against an IR avroc has
		// since moved past can say so instead of misreading the schemas.
		Version: proto.Int32(ir.Version),
		Options: options,
		Schemas: schemas,
	}
}

// writeDescriptor encodes desc and writes it into dir, returning the path it
// wrote. dir is expected to be the private directory created for this one
// invocation, and the file is expected not to exist yet: O_EXCL turns a reused
// directory into an error here rather than into a descriptor that silently
// belongs to some earlier invocation.
//
// The file is written and closed before it is returned, which is what lets the
// caller start the generator against a descriptor that is complete by
// construction — no lock, no sentinel and no retry on the reading side.
func writeDescriptor(dir string, desc *avrocpb.GenerateRequest) (path string, err error) {
	b, err := ir.MarshalDescriptor(desc)
	if err != nil {
		return "", fmt.Errorf("failed to encode descriptor: %w", err)
	}

	path = filepath.Join(dir, descriptorFilename)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, descriptorFileMode)
	if err != nil {
		return "", fmt.Errorf("failed to create descriptor file: %w", err)
	}
	defer func() {
		closeErr := f.Close()
		if closeErr == nil {
			return
		}
		// A close that failed may have lost buffered bytes, so the path is not
		// handed back: a caller must not start a generator against a descriptor
		// avroc cannot say it finished writing.
		err = errors.Join(err, fmt.Errorf("failed to close descriptor file %q: %w", path, closeErr))
		path = ""
	}()

	if _, err := f.Write(b); err != nil {
		return "", fmt.Errorf("failed to write descriptor file %q: %w", path, err)
	}

	return path, nil
}
