// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package ir

import (
	"github.com/z5labs/avroc/avrocpb"

	"google.golang.org/protobuf/proto"
)

// MarshalDescriptor encodes a descriptor into the protobuf binary wire encoding
// docs/plugin/SPEC.md requires of the bytes at --descriptor.
//
// It is the one place those bytes are produced, because the encoding carries a
// requirement the call site cannot see: two runs over unchanged inputs MUST
// produce byte-identical descriptors. Generated output is a thing a project
// commits and reviews, and a descriptor that differed run to run would make
// every regeneration a diff, with nothing in it that anybody changed.
//
// Deterministic marshalling is what buys that. protobuf-go emits fields in
// field-number order, so a message built the same way twice already encodes the
// same way twice; the option additionally fixes the order of any map field the
// IR grows later, which is the one construct whose encoding would otherwise
// follow Go's randomised map iteration — and would break intermittently rather
// than every time, which is worse.
//
// Determinism upstream of the encoding is the producer's: the repeated fields
// carry an order, and avroc fixes it before it gets here. Manifest options sort
// by key (see GeneratorConfig.options), and schemas follow the manifest's input
// order.
func MarshalDescriptor(desc *avrocpb.GenerateRequest) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(desc)
}
