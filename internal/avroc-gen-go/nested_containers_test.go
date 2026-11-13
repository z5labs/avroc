// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"path/filepath"
	"testing"

	"github.com/z5labs/avroc/avrocpb"
	"google.golang.org/protobuf/proto"
)

// nestedIssueSchema is the record from #237's reproduction, plus the shape
// only a hand-built descriptor reaches: the IDL parser accepts nothing but a
// name as a map's values, so map<array<int>> cannot be written in IDL, but the
// IR allows it and so the generator has to handle it.
func nestedIssueSchema() *avrocpb.Schema {
	return &avrocpb.Schema{
		Namespace: proto.String(unionTestNamespace),
		Type: recordOf("Outer",
			fieldOf("grid", arrayOf(arrayOf(primType("int")))),
			fieldOf("rows", arrayOf(mapOf(primType("int")))),
			fieldOf("maybeGrid", arrayOf(arrayOf(unionOf(primType("null"), primType("int"))))),
			fieldOf("byKey", mapOf(arrayOf(primType("int")))),
		),
	}
}

// nestedShapesSchema is every other nesting #237 asks for: deeper than two,
// a map in a map, and each kind of innermost type — a record, an enum and a
// fixed defined in full where they are first met, a named reference to one of
// them, a union holding one, and a container nested in a union's branch.
func nestedShapesSchema() *avrocpb.Schema {
	cellRef := &avrocpb.Type{Type: &avrocpb.Type_Reference{Reference: namedRef(unionTestNamespace + ".Cell")}}
	return &avrocpb.Schema{
		Namespace: proto.String(unionTestNamespace),
		Type: recordOf("Deeper",
			fieldOf("cube", arrayOf(arrayOf(arrayOf(primType("long"))))),
			fieldOf("nestedMaps", mapOf(mapOf(primType("string")))),
			fieldOf("cells", arrayOf(arrayOf(recordOf("Cell", fieldOf("c", primType("int")))))),
			fieldOf("tones", arrayOf(mapOf(enumOf("Tone", "LOW", "HIGH")))),
			fieldOf("hashes", mapOf(arrayOf(fixedOf("Hash", 2)))),
			fieldOf("cellsByKey", mapOf(mapOf(cellRef))),
			fieldOf("maybeCells", arrayOf(mapOf(arrayOf(unionOf(primType("null"), cellRef))))),
			fieldOf("choice", unionOf(primType("null"), arrayOf(arrayOf(primType("int"))))),
			fieldOf("choiceMap", unionOf(primType("null"), mapOf(arrayOf(primType("string"))))),
		),
	}
}

// TestNestedContainersCompileAndRoundTrip builds the generated code for both
// schemas above and runs testdata/nested/nested_test.go against it, which
// round-trips a non-empty value of every field and checks the bytes of the
// shapes #237 names against Avro's binary encoding.
//
// Building the package is part of the assertion and not incidental to it: a
// container item with no emitter used to leave a loop variable unused, and the
// compile error that caused was the only thing standing between it and
// encoding every nested container as empty. The round trip of non-empty
// values is what fails here whether or not the code compiles.
func TestNestedContainersCompileAndRoundTrip(t *testing.T) {
	testGeneratedPackage(t, "nested", filepath.Join("testdata", "nested", "nested_test.go"),
		nestedIssueSchema(), nestedShapesSchema())
}
