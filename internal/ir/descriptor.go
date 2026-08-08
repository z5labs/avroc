// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package ir

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/z5labs/avroc/avrocpb"

	"google.golang.org/protobuf/encoding/protojson"
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

// UnmarshalDescriptor decodes the protobuf binary wire encoding MarshalDescriptor
// produces back into a descriptor.
//
// It is the reading half of the same one place, so a consumer never spells the
// message type at its own call site and cannot decode a descriptor into
// something that merely resembles one.
//
// Unknown fields are kept rather than discarded, which is the ignoring half of
// docs/ir/SPEC.md's asymmetry done properly: a descriptor from a producer that
// carries a field this build has never heard of decodes, and the field survives
// a re-encode instead of being silently dropped by whatever reads it next. What
// is *not* checked here is the version or the closed sets — CheckVersion and
// Validate own those, in that order, and a caller that only wants to look at a
// descriptor is entitled to skip both.
//
// Emptiness is the one exception, and it is not a version check in disguise.
// Zero bytes are a valid encoding of an empty message, so proto.Unmarshal
// accepts them and hands back a descriptor carrying nothing — which is how a
// truncated write, an empty file or a path that was never a descriptor at all
// turns into a successful read and an empty rendering. No conforming producer
// emits one, because every descriptor carries at least a version, so refusing
// here puts the diagnostic where the mistake is instead of leaving somebody to
// wonder why their schemas vanished.
func UnmarshalDescriptor(b []byte) (*avrocpb.GenerateRequest, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("failed to decode descriptor: no bytes to decode")
	}

	var desc avrocpb.GenerateRequest
	if err := proto.Unmarshal(b, &desc); err != nil {
		return nil, fmt.Errorf("failed to decode descriptor: %w", err)
	}
	return &desc, nil
}

// descriptorJSONIndent is the indentation a rendered descriptor is written with.
// Two spaces, matching the manifest and lockfile avroc writes, so the three
// artifacts a person reads in one sitting are indented alike.
const descriptorJSONIndent = "  "

// MarshalDescriptorJSON renders a descriptor as the JSON a person reads:
// docs/ir/SPEC.md's inspection path, and the answer to "what was this generator
// actually handed".
//
// It is a rendering and never an interchange format. Only avroc writes it,
// nothing reads it back, and a plugin is handed the binary encoding — a plugin
// accepting both would have to sniff which it had (docs/plugin/SPEC.md, "The
// descriptor").
//
// Two choices make the output worth diffing:
//
// Field names are protobuf's own, so a name in the rendering is a name in
// proto/ and in docs/ir/SPEC.md — full_name, not fullName. Someone reading a
// descriptor is reading it beside the spec, and a lowerCamelCase rendering
// would make them translate every name back.
//
// The bytes are stable across runs *and across builds*, which the obvious
// implementation is not. protojson deliberately emits an unstable amount of
// whitespace — an extra space after a comma, or after a "key":, chosen by a
// hash of the running binary — precisely so that nobody depends on its exact
// output. That is stable within one avroc and different in the next, so a
// descriptor re-rendered after an upgrade would diff on every line with nothing
// in it that anybody changed. Re-indenting through encoding/json rewrites every
// byte of insignificant whitespace and leaves the token sequence alone, which
// pins the output to what protojson *said* rather than to how it spaced it.
//
// Field order is protobuf's field-number order and map entries are sorted by
// key, both of which protojson already guarantees; the re-indent preserves the
// order it was given.
func MarshalDescriptorJSON(desc *avrocpb.GenerateRequest) ([]byte, error) {
	b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(desc)
	if err != nil {
		return nil, fmt.Errorf("failed to render descriptor as JSON: %w", err)
	}
	return indentJSON(b)
}

// indentJSON reformats valid JSON into one canonical indented shape, dropping
// whatever insignificant whitespace it arrived with. It is the step that makes
// MarshalDescriptorJSON's output a function of the descriptor rather than of
// the binary that rendered it; see there for why protojson's own output is not.
func indentJSON(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, b, "", descriptorJSONIndent); err != nil {
		return nil, fmt.Errorf("failed to indent descriptor JSON: %w", err)
	}
	return buf.Bytes(), nil
}
