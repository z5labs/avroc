// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocpb

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// FileDescriptorSet returns the IR's self-description: the protobuf
// FileDescriptorSet that describes a descriptor, and so the thing that lets a
// consumer decode one without any generated code at all.
//
// It exists for the plugin author whose language has weak protobuf tooling, or
// none in the build. protobuf's one real disadvantage against a self-describing
// format is that a reader normally needs the schema compiled in ahead of time;
// a FileDescriptorSet closes that, because every runtime worth the name can
// build a message type out of one at run time and decode against it.
// docs/ir/SPEC.md's "protobuf is a schema language, not a service" is where that
// trade is argued; this function is the half of it that ships.
//
// # Why it is computed rather than committed
//
// The set is derived, on every call, from the descriptors compiled into this
// package by protoc-gen-go — the same descriptors avroc itself encodes a
// descriptor with. There is no .binpb checked into the repository and no protoc
// invocation anyone has to remember, so there is no second copy of the IR that
// could describe a version of it that no longer exists. Adding a field to
// proto/ and regenerating avrocpb/ changes what this returns in the same commit,
// because it is the same input read twice rather than two artifacts kept in
// step by hand.
//
// # What is in it, and what is deliberately not
//
// GenerateRequest is the only root. It is the descriptor — docs/ir/SPEC.md
// specifies exactly one message a plugin is handed — so the set is its file
// plus the transitive closure of that file's imports, and nothing else.
//
// Nothing else is reachable from that root, and since #124 nothing else is in
// proto/ either: generator.proto and generate_response.proto — the gRPC
// Generator service and the message that existed only as its streamed response —
// are deleted. They were excluded from this set before they were deleted, for
// the reason docs/ir/SPEC.md gives for the IR defining no service at all:
// publishing them here would have handed a plugin author a self-description of a
// service the specification tells them does not exist, and would then have broken
// them when it went. A generator needs to decode a descriptor, not to speak a
// protocol.
//
// # Order
//
// Files are emitted in dependency order: a file appears only after every file it
// imports. That is what `protoc --include_imports` produces and what a consumer
// that walks the set linearly — building each file's types as it goes, which is
// the shape of most dynamic protobuf APIs — needs in order to resolve a type
// reference the first time it meets one. Within that, the order is fixed by the
// import declarations themselves, so the output is a function of the protos and
// not of a map iteration.
func FileDescriptorSet() *descriptorpb.FileDescriptorSet {
	set := &descriptorpb.FileDescriptorSet{}
	appendFileWithImports(set, make(map[string]struct{}), descriptorFileDescriptor())
	return set
}

// MarshalFileDescriptorSet encodes FileDescriptorSet into the protobuf binary
// wire encoding, which is the form every dynamic protobuf runtime reads a
// FileDescriptorSet in and the form the published ir.binpb artifact holds.
//
// The bytes are deterministic, for the same reason MarshalDescriptor's are
// (internal/ir): the artifact is published against a release and copied into an
// image, and a set whose bytes moved between two builds of the same source would
// make every rebuild look like a change to the contract. Field order is
// protobuf's own, the file order is fixed by FileDescriptorSet above, and the
// deterministic option pins the one construct — a map field — whose encoding
// would otherwise follow Go's randomised map iteration.
func MarshalFileDescriptorSet() ([]byte, error) {
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(FileDescriptorSet())
	if err != nil {
		return nil, fmt.Errorf("failed to encode the IR FileDescriptorSet: %w", err)
	}
	return b, nil
}

// descriptorFileDescriptor returns the file GenerateRequest is defined in.
//
// It is read off the message rather than named as a string so that the root of
// the set is the descriptor type itself. A path like "generate_request.proto"
// written here would be a second place the IR's shape is recorded, and would go
// on compiling after the file it names had been renamed or its message moved.
func descriptorFileDescriptor() protoreflect.FileDescriptor {
	return new(GenerateRequest).ProtoReflect().Descriptor().ParentFile()
}

// appendFileWithImports appends fd's imports, transitively and depth first,
// before appending fd itself — which is what puts the result in the dependency
// order FileDescriptorSet documents.
//
// seen is keyed by path and marked on entry, so a file reached through two
// different import chains is emitted once, at its first (deepest) position.
// Marking on entry rather than on append also means a cyclic import graph
// terminates instead of recursing forever; protobuf forbids one, and relying on
// that to stay true is a worse bargain than one map write.
func appendFileWithImports(set *descriptorpb.FileDescriptorSet, seen map[string]struct{}, fd protoreflect.FileDescriptor) {
	if _, ok := seen[fd.Path()]; ok {
		return
	}
	seen[fd.Path()] = struct{}{}

	imports := fd.Imports()
	for i := range imports.Len() {
		appendFileWithImports(set, seen, imports.Get(i).FileDescriptor)
	}

	set.File = append(set.File, protodesc.ToFileDescriptorProto(fd))
}
