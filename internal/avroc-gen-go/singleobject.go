// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"fmt"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/ir"
)

// checkSingleObjectRoots refuses every schema this generator cannot give
// single-object encoding to, and refuses it before a file has been written.
//
// Avro's single-object encoding frames one value behind the fingerprint of the
// schema that value was written against, and this generator publishes that
// fingerprint as a Fingerprint method on the Go type it generated for the
// schema's root record. A root that is not a record has no such type, so there
// was nowhere to put the method and none was emitted — the run exited zero, and
// a user who asked for single-object encoding was handed ordinary binary
// encoding with nothing on stderr saying so (#173). Silence was the bug:
// docs/plugin/SPEC.md makes a non-zero exit how a generator says it cannot do
// what an option asked, and avroc discards the invocation's scratch directory
// on one, so the user is told rather than shipped the wrong thing.
//
// It walks every schema in the descriptor before the generation loop rather
// than checking inside it, because that loop writes as it goes: refusing the
// third schema from within it would leave the first two behind in the output
// directory. avroc would discard them, so nothing wrong reaches the user's
// tree either way — but a generator that has written half a package before
// noticing it was never going to finish has read the descriptor in the wrong
// order.
//
// Refusing is a complete fix on its own, and not the only possible one:
// single-object encoding frames *one* value, and for schema array<Event>; that
// value is the array, so the fingerprint is the array schema's and belongs once
// at the head of the stream rather than once per item. Supporting that is a
// later story, and it turns this refusal into generation.
func checkSingleObjectRoots(schemas []*avrocpb.Schema) error {
	for _, schema := range schemas {
		if _, ok := schema.GetType().GetType().(*avrocpb.Type_Record); ok {
			continue
		}

		// The diagnostic below names a type constructor, so it has to be one
		// that exists. Validate owns that closed set, and a root outside it is
		// a schema this generator cannot represent at all rather than one an
		// option was wrong about — so it is reported as the former, in the
		// words the rest of the repository uses for it.
		if err := ir.Validate(schema); err != nil {
			return err
		}

		return fmt.Errorf(
			"encoding=single_object requires a record at the schema root: schema %q has a root of type %s",
			ir.SchemaBaseName(schema),
			typeConstructor(schema.GetType()),
		)
	}
	return nil
}

// typeConstructor names a type the way Avro's specification does, for a
// diagnostic a person reads.
//
// A reference is named by what it refers to — the primitive's name, or the
// named type's — because "reference" is a fact about how the IR encodes a type
// and not about the schema anybody wrote. The default arm is unreachable for a
// type Validate has accepted and is there because a switch on a closed set
// still needs one.
func typeConstructor(t *avrocpb.Type) string {
	switch v := t.GetType().(type) {
	case *avrocpb.Type_Record:
		return "record"
	case *avrocpb.Type_EnumType:
		return "enum"
	case *avrocpb.Type_Fixed:
		return "fixed"
	case *avrocpb.Type_Array:
		return "array"
	case *avrocpb.Type_MapType:
		return "map"
	case *avrocpb.Type_Union:
		return "union"
	case *avrocpb.Type_Reference:
		return v.Reference.GetName()
	default:
		return "unknown"
	}
}
