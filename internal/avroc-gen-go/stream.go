// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"strings"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/ir"
)

// streamReader is everything the streaming reader emitted for an array-rooted
// schema is generated from: the Go type of one item, and the plural the
// package-level entry point is named with.
//
// Both are derived once, in streamReaderFor, so that the emission below is a
// substitution and cannot disagree with itself about what an item is called.
type streamReader struct {
	// item is the Go type name of one item of the stream, e.g. Event.
	item string
	// plural names the whole stream, e.g. Events.
	plural string
}

// streamReaderFor reports the streaming reader a schema's root type earns, or
// nil for a root that earns none.
//
// A schema whose root is array<T> says the file it describes is a *stream of T*
// rather than a value containing a T, and that is the one shape where decoding
// into memory can be avoided entirely: the block framing is a prefix code, so a
// reader can hand back one item at a time from an io.Reader that is never fully
// read. Every other root is a single value, and a single value is what
// UnmarshalAvroBinary already is.
//
// The item has to be a type this generator wrote an UnmarshalAvroBinary for,
// because that method is the whole of what avro.ArrayReader consumes. A record,
// an enum and a fixed have one, and so does a reference to any of them; an
// array of primitives, of arrays, of maps or of unions does not, so those roots
// generate what they generated before, which is nothing. Widening that set is a
// matter of the item having a method, not of anything here.
func streamReaderFor(schema *avrocpb.Schema) *streamReader {
	root, ok := schema.GetType().GetType().(*avrocpb.Type_Array)
	if !ok {
		return nil
	}

	item := streamItemType(root.Array.GetItems())
	if item == "" {
		return nil
	}
	return &streamReader{item: item, plural: pluralize(item)}
}

// streamItemType returns the Go type name of an array item this generator has
// generated an UnmarshalAvroBinary for, or the empty string for anything else.
func streamItemType(t *avrocpb.Type) string {
	switch v := t.GetType().(type) {
	case *avrocpb.Type_Record, *avrocpb.Type_EnumType, *avrocpb.Type_Fixed:
		return toPascalCase(ir.NamedTypeName(t))
	case *avrocpb.Type_Reference:
		// A reference to a named type is a type written out in full somewhere
		// else in this schema, so the Go type exists and carries the method. A
		// reference to a primitive names a Go type that does not.
		if v.Reference.GetKind() == avrocpb.TypeRefKind_TYPE_REF_KIND_NAMED {
			return toPascalCase(v.Reference.GetName())
		}
		return ""
	default:
		return ""
	}
}

// pluralize returns the English plural of a PascalCase Go type name.
//
// The package-level entry point is named for what it yields — StreamEvents, not
// StreamEvent — because the call sites it is written for read as a range over a
// collection. The rules below are the regular ones and nothing more: this is
// naming, so an irregular noun getting Childs rather than Children is a wart in
// a name and not a defect in the encoding, and a table of exceptions would be a
// second thing to keep correct for no gain in what the code does.
func pluralize(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, "s"),
		strings.HasSuffix(lower, "x"),
		strings.HasSuffix(lower, "z"),
		strings.HasSuffix(lower, "ch"),
		strings.HasSuffix(lower, "sh"):
		return name + "es"
	case len(name) > 1 && strings.HasSuffix(lower, "y") && !isVowel(lower[len(lower)-2]):
		return name[:len(name)-1] + "ies"
	default:
		return name + "s"
	}
}

func isVowel(b byte) bool {
	return strings.IndexByte("aeiou", b) >= 0
}

// generateStreamReader writes the streaming reader for an array-rooted schema.
//
// It is a wrapper and deliberately nothing more. avro.ArrayReader owns the
// block framing — the long item count, the negative count whose absolute value
// is the count and which is followed by the block's encoded size, the zero
// count that terminates the array — and that framing is entirely
// type-independent, so a second copy of it emitted once per schema would be a
// second copy to keep correct. What the generated code contributes is the type:
// the destination avro.ArrayReader.Next decodes into, and the element type of
// the iterator.
//
// The reader embeds *avro.ArrayReader rather than hiding it. Skipping and reuse
// are the two things a caller reaches for a streaming reader to do — reuse one
// destination across the whole array, or discard a sized block with SkipBlock
// instead of decoding items nobody wants — and both are already methods on the
// embedded reader, promoted unchanged. Wrapping them would be a forwarding
// layer whose only effect is to be one version behind avro-go.
func generateStreamReader(cb *codeBuilder, s streamReader) {
	reader := s.item + "Reader"

	cb.newline()
	cb.writef("// %s reads a stream of %s values from an Avro array without\n", reader, s.item)
	cb.writeln("// materialising it.")
	cb.writeln("//")
	cb.writef("// The embedded [avro.ArrayReader] is the whole of the block framing. Its Next\n")
	cb.writef("// decodes into a destination the caller owns, so one %s can be reused for the\n", s.item)
	cb.writeln("// whole stream, and its SkipBlock discards a sized block without decoding any")
	cb.writeln("// item at all.")
	cb.writef("type %s struct {\n", reader)
	cb.writeln("\t*avro.ArrayReader")
	cb.writeln("}")
	cb.newline()

	cb.writef("// New%s returns a reader decoding the Avro array in r.\n", reader)
	cb.writef("func New%s(r io.Reader) *%s {\n", reader, reader)
	cb.writef("\treturn &%s{ArrayReader: avro.NewArrayReader(avro.NewBinaryReader(r))}\n", reader)
	cb.writeln("}")
	cb.newline()

	cb.writef("// All returns an iterator over the stream's remaining items, decoding each\n")
	cb.writef("// into a newly allocated %s.\n", s.item)
	cb.writeln("//")
	cb.writeln("// Iteration stops at the array's terminating block, and at the first error,")
	cb.writef("// which is yielded once as a nil *%s alongside it. Call the embedded reader's\n", s.item)
	cb.writeln("// Next directly to decode into a destination of your own instead.")
	cb.writef("func (x *%s) All() iter.Seq2[*%s, error] {\n", reader, s.item)
	cb.writef("\treturn func(yield func(*%s, error) bool) {\n", s.item)
	cb.writeln("\t\tfor {")
	cb.writef("\t\t\tv := new(%s)\n", s.item)
	cb.writeln("\t\t\tok, err := x.ArrayReader.Next(v)")
	cb.writeln("\t\t\tif err != nil {")
	cb.writeln("\t\t\t\tyield(nil, err)")
	cb.writeln("\t\t\t\treturn")
	cb.writeln("\t\t\t}")
	cb.writeln("\t\t\tif !ok {")
	cb.writeln("\t\t\t\treturn")
	cb.writeln("\t\t\t}")
	cb.writeln("\t\t\tif !yield(v, nil) {")
	cb.writeln("\t\t\t\treturn")
	cb.writeln("\t\t\t}")
	cb.writeln("\t\t}")
	cb.writeln("\t}")
	cb.writeln("}")
	cb.newline()

	cb.writef("// Stream%s returns an iterator over the %s values encoded as an Avro array\n", s.plural, s.item)
	cb.writeln("// in r. It is the ordinary way to consume such a stream:")
	cb.writeln("//")
	cb.writef("//\tfor v, err := range Stream%s(r) {\n", s.plural)
	cb.writeln("//\t\t...")
	cb.writeln("//\t}")
	cb.writef("func Stream%s(r io.Reader) iter.Seq2[*%s, error] {\n", s.plural, s.item)
	cb.writef("\treturn New%s(r).All()\n", reader)
	cb.writeln("}")
}
