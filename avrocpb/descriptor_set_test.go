// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocpb_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/z5labs/avroc/avrocpb"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// serviceOnlyProtos are the files in proto/ that the published set leaves out,
// with the reason each one is out. They are the gRPC Generator service and the
// message that exists only as its streamed response; docs/ir/SPEC.md says the IR
// MUST NOT define a service, and #124 removes both files outright.
//
// The list is here rather than in the implementation on purpose. The
// implementation names one root and follows imports, so it has no exclusion list
// to get wrong; this is the test's own statement of which files that root is
// expected to miss, and it is what turns "a new .proto nobody wired into the
// descriptor" into a failure rather than a silently unpublished file.
func serviceOnlyProtos() []string {
	return []string{"generate_response.proto", "generator.proto"}
}

// TestFileDescriptorSetCoversEveryProtoTheDescriptorReaches is the staleness
// gate. The set is computed from the descriptors compiled into avrocpb, so it
// cannot drift from them; what it can do is quietly stop covering proto/, which
// is what happens when a new IR message lands in a file nothing in the
// descriptor's import graph reaches. A consumer decoding dynamically would then
// meet a field whose type the published set does not describe.
//
// So the assertion is against the directory rather than against another copy of
// the same descriptors: every .proto on disk is either in the set or in
// serviceOnlyProtos, and nothing is in the set that is not on disk.
func TestFileDescriptorSetCoversEveryProtoTheDescriptorReaches(t *testing.T) {
	onDisk, err := filepath.Glob(filepath.Join(moduleRoot(t), "proto", "*.proto"))
	if err != nil {
		t.Fatalf("glob proto/: %v", err)
	}
	if len(onDisk) == 0 {
		t.Fatal("no .proto files found: the check would pass vacuously")
	}

	var want []string
	for _, path := range onDisk {
		name := filepath.Base(path)
		if slices.Contains(serviceOnlyProtos(), name) {
			continue
		}
		want = append(want, name)
	}
	slices.Sort(want)

	var got []string
	for _, file := range avrocpb.FileDescriptorSet().GetFile() {
		got = append(got, file.GetName())
	}
	slices.Sort(got)

	if !slices.Equal(got, want) {
		t.Fatalf("published FileDescriptorSet does not cover proto/:\n got: %v\nwant: %v\n\nA new .proto reachable from GenerateRequest is published automatically; one that is not reachable is either a mistake or belongs beside the service files in serviceOnlyProtos.", got, want)
	}
}

// TestFileDescriptorSetIsInDependencyOrder asserts the ordering the doc comment
// promises: no file appears before a file it imports. A consumer that walks the
// set once, building each file's types as it goes — which is what most dynamic
// protobuf APIs make easy — fails outright on the other order, and it fails at
// the type reference rather than at the file, so the diagnostic points nowhere
// useful.
func TestFileDescriptorSetIsInDependencyOrder(t *testing.T) {
	var placed []string
	for _, file := range avrocpb.FileDescriptorSet().GetFile() {
		for _, dep := range file.GetDependency() {
			if !slices.Contains(placed, dep) {
				t.Errorf("%s is emitted before its import %s", file.GetName(), dep)
			}
		}
		placed = append(placed, file.GetName())
	}
}

// TestMarshalFileDescriptorSetIsDeterministic asserts what the artifact's
// consumers depend on: two encodings of the same source produce the same bytes.
// The set is published against a release and copied into an image, so bytes that
// moved between two builds would make a rebuild indistinguishable from a change
// to the contract.
func TestMarshalFileDescriptorSetIsDeterministic(t *testing.T) {
	first, err := avrocpb.MarshalFileDescriptorSet()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("encoded an empty FileDescriptorSet")
	}

	second, err := avrocpb.MarshalFileDescriptorSet()
	if err != nil {
		t.Fatalf("marshal again: %v", err)
	}

	if !bytes.Equal(first, second) {
		t.Fatalf("two encodings of the same set differ: %d bytes then %d bytes", len(first), len(second))
	}
}

// TestDynamicDecodeLeavesNothingUndescribed is the end-to-end half of the
// staleness gate. It decodes a descriptor through the published set alone and
// then asserts two things the coverage test cannot: that nothing in the
// descriptor landed in the dynamic message's unknown fields — every byte avroc
// wrote is described by what is published — and that re-encoding the dynamic
// message reproduces the descriptor exactly, which is the property a consumer
// relies on when it reads a descriptor, edits nothing, and hands it on.
func TestDynamicDecodeLeavesNothingUndescribed(t *testing.T) {
	descriptor, err := proto.Marshal(exampleDescriptor())
	if err != nil {
		t.Fatalf("encode descriptor: %v", err)
	}

	msg := newDynamicDescriptor(t)
	if err := proto.Unmarshal(descriptor, msg); err != nil {
		t.Fatalf("decode descriptor dynamically: %v", err)
	}

	assertNoUnknownFields(t, msg.ProtoReflect())

	roundTripped, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	if err != nil {
		t.Fatalf("re-encode descriptor: %v", err)
	}
	if !bytes.Equal(descriptor, roundTripped) {
		t.Fatalf("dynamic round trip changed the descriptor: %d bytes in, %d bytes out", len(descriptor), len(roundTripped))
	}
}

// newDynamicDescriptor builds a message type for GenerateRequest out of the
// published bytes and nothing else — the exact path a plugin author with no
// generated code takes.
func newDynamicDescriptor(t *testing.T) *dynamicpb.Message {
	t.Helper()

	irBinpb, err := avrocpb.MarshalFileDescriptorSet()
	if err != nil {
		t.Fatalf("marshal the published set: %v", err)
	}

	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(irBinpb, &set); err != nil {
		t.Fatalf("decode the published set: %v", err)
	}

	files, err := protodesc.NewFiles(&set)
	if err != nil {
		t.Fatalf("build a type registry from the published set: %v", err)
	}

	desc, err := files.FindDescriptorByName("GenerateRequest")
	if err != nil {
		t.Fatalf("find GenerateRequest in the published set: %v", err)
	}

	md, ok := desc.(protoreflect.MessageDescriptor)
	if !ok {
		t.Fatalf("GenerateRequest resolved to %T, not a message", desc)
	}
	return dynamicpb.NewMessage(md)
}

// assertNoUnknownFields walks a decoded message and reports any bytes the
// published set had no field for. Unknown fields are how a stale set fails
// quietly: the decode succeeds, the message looks plausible, and the parts the
// consumer never heard of are simply not there.
func assertNoUnknownFields(t *testing.T, msg protoreflect.Message) {
	t.Helper()

	if unknown := msg.GetUnknown(); len(unknown) > 0 {
		t.Errorf("%s carries %d bytes the published FileDescriptorSet does not describe", msg.Descriptor().FullName(), len(unknown))
	}

	msg.Range(func(fd protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		switch {
		case fd.IsList() && fd.Message() != nil:
			list := value.List()
			for i := range list.Len() {
				assertNoUnknownFields(t, list.Get(i).Message())
			}
		case fd.IsMap() && fd.MapValue().Message() != nil:
			value.Map().Range(func(_ protoreflect.MapKey, v protoreflect.Value) bool {
				assertNoUnknownFields(t, v.Message())
				return true
			})
		case !fd.IsList() && !fd.IsMap() && fd.Message() != nil:
			assertNoUnknownFields(t, value.Message())
		}
		return true
	})
}

// exampleDescriptor is a descriptor of the shape avroc emits: a version, one
// generator option, and one resolved record whose single field references an
// Avro primitive by name.
func exampleDescriptor() *avrocpb.GenerateRequest {
	return &avrocpb.GenerateRequest{
		Version: proto.Int32(1),
		Options: []*avrocpb.Option{
			{Name: proto.String("package_name"), Value: proto.String("gen")},
		},
		Schemas: []*avrocpb.Schema{
			{
				Namespace: proto.String("example.avro"),
				Type: &avrocpb.Type{
					Type: &avrocpb.Type_Record{
						Record: &avrocpb.Record{
							Name:      proto.String("TestRecord"),
							Namespace: proto.String("example.avro"),
							FullName:  proto.String("example.avro.TestRecord"),
							Fields: []*avrocpb.Field{
								{
									Name: proto.String("id"),
									Type: &avrocpb.Type{
										Type: &avrocpb.Type_Reference{
											Reference: &avrocpb.Reference{
												Name: proto.String("string"),
												Kind: avrocpb.TypeRefKind_TYPE_REF_KIND_PRIMITIVE.Enum(),
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// Example_readADescriptorWithoutGeneratedCode is the worked example
// docs/ir/SPEC.md points at: a consumer reads a descriptor through the published
// FileDescriptorSet alone, with no code generated from proto/ anywhere in it.
//
// Everything below the marked line uses only protobuf's own runtime — the
// descriptor types, a registry built from the published bytes, and a dynamic
// message — so it transliterates directly into any language whose protobuf
// runtime can load a FileDescriptorSet, which is every one of them. In the
// image the first two lines of the consumer half are a read of
// /usr/local/share/avroc/ir.binpb and the path avroc passed as --descriptor;
// they are spelled as in-process calls here so the example runs as a test.
func Example_readADescriptorWithoutGeneratedCode() {
	// The producer half. avroc encodes a descriptor and publishes the set that
	// describes it; a plugin author writes neither of these two lines.
	descriptor, err := proto.Marshal(exampleDescriptor())
	if err != nil {
		panic(err)
	}
	irBinpb, err := avrocpb.MarshalFileDescriptorSet()
	if err != nil {
		panic(err)
	}

	// ---- The consumer half: no generated code past this line. ----

	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(irBinpb, &set); err != nil {
		panic(err)
	}

	files, err := protodesc.NewFiles(&set)
	if err != nil {
		panic(err)
	}

	desc, err := files.FindDescriptorByName("GenerateRequest")
	if err != nil {
		panic(err)
	}
	md := desc.(protoreflect.MessageDescriptor)

	msg := dynamicpb.NewMessage(md)
	if err := proto.Unmarshal(descriptor, msg); err != nil {
		panic(err)
	}

	// The IR version comes first, always: docs/ir/SPEC.md makes reading it
	// before anything else a consumer's first obligation, and it is the one
	// field whose absence means the rest is not safe to look at.
	fmt.Println("ir version:", msg.Get(md.Fields().ByName("version")).Int())

	schemas := msg.Get(md.Fields().ByName("schemas")).List()
	for i := range schemas.Len() {
		schema := schemas.Get(i).Message()
		typ := schema.Get(schema.Descriptor().Fields().ByName("type")).Message()

		record := typ.Get(typ.Descriptor().Fields().ByName("record")).Message()
		fullName := record.Get(record.Descriptor().Fields().ByName("full_name")).String()
		fmt.Println("record:", fullName)

		fields := record.Get(record.Descriptor().Fields().ByName("fields")).List()
		for j := range fields.Len() {
			field := fields.Get(j).Message()
			name := field.Get(field.Descriptor().Fields().ByName("name")).String()

			fieldType := field.Get(field.Descriptor().Fields().ByName("type")).Message()
			ref := fieldType.Get(fieldType.Descriptor().Fields().ByName("reference")).Message()
			refName := ref.Get(ref.Descriptor().Fields().ByName("name")).String()

			fmt.Printf("  field %s: %s\n", name, refName)
		}
	}

	// Output:
	// ir version: 1
	// record: example.avro.TestRecord
	//   field id: string
}

// TestPublishedSetHasNoServiceDefinition guards the decision the set's doc
// comment records. docs/ir/SPEC.md says the IR MUST NOT define an RPC service,
// so publishing one in the IR's own self-description would contradict the
// specification in the artifact a plugin author reads instead of the
// specification.
func TestPublishedSetHasNoServiceDefinition(t *testing.T) {
	for _, file := range avrocpb.FileDescriptorSet().GetFile() {
		if services := file.GetService(); len(services) > 0 {
			t.Errorf("%s publishes %d service definition(s); the IR defines no service", file.GetName(), len(services))
		}
	}
}

// TestPublishedSetIsSelfContained asserts the set resolves on its own. A set
// naming an import it does not carry is the classic way this artifact breaks:
// it decodes, it looks complete, and it fails only in the consumer's registry,
// with an error naming a file they have never heard of.
func TestPublishedSetIsSelfContained(t *testing.T) {
	set := avrocpb.FileDescriptorSet()

	carried := make(map[string]struct{}, len(set.GetFile()))
	for _, file := range set.GetFile() {
		carried[file.GetName()] = struct{}{}
	}
	for _, file := range set.GetFile() {
		for _, dep := range file.GetDependency() {
			if _, ok := carried[dep]; !ok {
				t.Errorf("%s imports %s, which the set does not carry", file.GetName(), dep)
			}
		}
	}

	if _, err := protodesc.NewFiles(set); err != nil {
		t.Fatalf("the published set does not resolve on its own: %v", err)
	}
}

// TestMarshalledSetIsWhatTheArtifactHolds checks the encoded form decodes back
// to the same set, which is the only thing standing between an artifact written
// to disk and one a consumer can use.
func TestMarshalledSetIsWhatTheArtifactHolds(t *testing.T) {
	b, err := avrocpb.MarshalFileDescriptorSet()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(b, &got); err != nil {
		t.Fatalf("decode the encoded set: %v", err)
	}

	if !proto.Equal(&got, avrocpb.FileDescriptorSet()) {
		t.Fatal("the encoded set does not decode back to the set that was encoded")
	}
}

// fileNames is a small helper for diagnostics in tests that need to name what
// the set carried.
func fileNames(set *descriptorpb.FileDescriptorSet) []string {
	names := make([]string, 0, len(set.GetFile()))
	for _, file := range set.GetFile() {
		names = append(names, file.GetName())
	}
	return names
}

// TestFileDescriptorSetIsNotEmpty is the guard that keeps every other assertion
// in this file from passing vacuously.
func TestFileDescriptorSetIsNotEmpty(t *testing.T) {
	set := avrocpb.FileDescriptorSet()
	if len(set.GetFile()) == 0 {
		t.Fatal("the published FileDescriptorSet carries no files")
	}
	if !slices.Contains(fileNames(set), "generate_request.proto") {
		t.Fatalf("the set does not carry the descriptor's own file; it carries %v", fileNames(set))
	}
}

// TestModuleRootHasProtoDirectory keeps the coverage test honest about where it
// is looking, so a moved proto/ shows up as this failure rather than as an empty
// glob somewhere else.
func TestModuleRootHasProtoDirectory(t *testing.T) {
	info, err := os.Stat(filepath.Join(moduleRoot(t), "proto"))
	if err != nil {
		t.Fatalf("stat proto/: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("proto/ is not a directory")
	}
}
