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

// arrayStream is everything the streaming reader and writer emitted for an
// array-rooted schema are generated from: the Go type of one item, and the
// plural the package-level entry points are named with.
//
// Both are derived once, in arrayStreamFor, so that the emission below is a
// substitution and cannot disagree with itself about what an item is called.
type arrayStream struct {
	// item is the Go type name of one item of the stream, e.g. Event.
	item string
	// plural names the whole stream, e.g. Events.
	plural string
}

// arrayStreamFor reports the streaming pair a schema's root type earns, or nil
// for a root that earns none.
//
// A schema whose root is array<T> says the file it describes is a *stream of T*
// rather than a value containing a T, and that is the one shape where the array
// need never be in memory at all: the block framing is a prefix code, so a
// reader can hand back one item at a time from an io.Reader that is never fully
// read, and a writer can emit one item at a time into an io.Writer without
// having been given the whole slice first. Every other root is a single value,
// and a single value is what MarshalAvroBinary and UnmarshalAvroBinary already
// are.
//
// The item has to be a type this generator wrote those methods for, because
// they are the whole of what avro.ArrayReader and avro.ArrayWriter consume. A
// record, an enum and a fixed have them, and so does a reference to any of
// them; an array of primitives, of arrays, of maps or of unions does not, so
// those roots generate what they generated before, which is nothing. Widening
// that set is a matter of the item having the methods, not of anything here.
//
// One decision covers both halves on purpose: a reader with no writer would be
// a stream nothing here can produce, and a writer with no reader one nothing
// here can consume.
func arrayStreamFor(schema *avrocpb.Schema) *arrayStream {
	root, ok := schema.GetType().GetType().(*avrocpb.Type_Array)
	if !ok {
		return nil
	}

	item := streamItemType(root.Array.GetItems())
	if item == "" {
		return nil
	}
	return &arrayStream{item: item, plural: pluralize(item)}
}

// streamItemType returns the Go type name of an array item this generator has
// generated marshalling methods for, or the empty string for anything else.
func streamItemType(t *avrocpb.Type) string {
	switch v := t.GetType().(type) {
	case *avrocpb.Type_Record, *avrocpb.Type_EnumType, *avrocpb.Type_Fixed:
		return toPascalCase(ir.NamedTypeName(t))
	case *avrocpb.Type_Reference:
		// A reference to a named type is a type written out in full somewhere
		// else in this schema, so the Go type exists and carries the methods. A
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
//
// "Regular" is doing real work in that sentence, and the cases it gives up on
// are not only the famous ones. Every English plural that needs a fact about the
// word beyond its spelling is out of reach here: Childs and Persons, but equally
// Stomaches, and Quizes for a quiz whose z doubles only because the syllable is
// stressed — where the otherwise identical Topazes does not. Nothing short of a
// dictionary tells those apart, so the rule is deliberately the one that can be
// read off the letters, and a schema author who dislikes what it made of their
// type name has the whole of the item type's own API available un-pluralised.
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
func generateStreamReader(cb *codeBuilder, s arrayStream) {
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

// generateStreamWriter writes the streaming writer for an array-rooted schema.
//
// It is the reader's counterpart and is a wrapper for the same reason: block
// boundaries are the writer's whole job, they are type-independent, and
// avro.ArrayWriter already owns them — the item count, the negated count and
// byte size of a sized block, the buffering a size prefix requires, and the
// zero-count block that terminates the array. What the generated code
// contributes is the type.
//
// Two things about that contribution are decisions rather than mechanics.
//
// Write is *shadowed* where the reader's Next is promoted unchanged. The
// embedded writer's Write takes any avro.BinaryMarshaler, which would let any
// generated type in this package be written into a stream of this one — an
// error that survives to whatever reads the file back. The generated Write is
// that method with the item type filled in, so the stream's element type is the
// compiler's to enforce. The embedded writer is still exported, so the untyped
// form remains available to a caller who wants it.
//
// Close is *not* shadowed and not hidden, because an array is terminated by a
// zero-count block: a writer that is never closed leaves a truncated array
// rather than a complete one missing a flush. Presenting it as a detail the
// wrapper handles would be the way to get that wrong. The package-level entry
// point is the answer instead — it closes on the callback returning nil and
// leaves the array unterminated otherwise, so the ordinary way to write a
// stream has no close to forget, and a stream written the explicit way and left
// open is reported as avro.ErrTruncatedArray when it is read back.
func generateStreamWriter(cb *codeBuilder, s arrayStream) {
	writer := s.item + "Writer"

	cb.newline()
	cb.writef("// %s writes a stream of %s values as an Avro array without\n", writer, s.item)
	cb.writeln("// materialising it.")
	cb.writeln("//")
	cb.writef("// The embedded [avro.ArrayWriter] is the whole of the block framing, including\n")
	cb.writeln("// the zero-count block that terminates the array. Its Close writes that block,")
	cb.writeln("// so Close is neither optional nor merely a flush: a writer that is never")
	cb.writeln("// closed leaves a truncated array behind, which reading it back reports as")
	cb.writef("// [avro.ErrTruncatedArray]. Write%s closes for you.\n", s.plural)
	cb.writef("type %s struct {\n", writer)
	cb.writeln("\t*avro.ArrayWriter")
	cb.writeln("}")
	cb.newline()

	cb.writef("// New%s returns a writer encoding an Avro array of %s to w.\n", writer, s.item)
	cb.writeln("//")
	cb.writeln("// By default the blocks it emits declare no size and nothing is buffered: each")
	cb.writeln("// item reaches w as it is written. Pass [avro.WithSizedBlocks] to batch items")
	cb.writeln("// into blocks that declare their encoded size, which a reader can discard with")
	cb.writeln("// SkipBlock instead of decoding, at the cost of a bounded buffer.")
	cb.writef("func New%s(w io.Writer, opts ...avro.ArrayWriterOption) *%s {\n", writer, writer)
	cb.writef("\treturn &%s{ArrayWriter: avro.NewArrayWriter(avro.NewBinaryWriter(w), opts...)}\n", writer)
	cb.writeln("}")
	cb.newline()

	cb.writeln("// Write encodes v as the next item of the array.")
	cb.writeln("//")
	cb.writef("// It is the embedded writer's Write with the stream's item type filled in: a\n")
	cb.writef("// stream of %s holds %s values, and this is what makes writing anything\n", s.item, s.item)
	cb.writeln("// else a compile error rather than a convention. Call the embedded writer's")
	cb.writeln("// Write directly to write any [avro.BinaryMarshaler] instead.")
	cb.writef("func (x *%s) Write(v *%s) error {\n", writer, s.item)
	cb.writeln("\treturn x.ArrayWriter.Write(v)")
	cb.writeln("}")
	cb.newline()

	cb.writef("// Write%s encodes the %s values f writes as an Avro array in w,\n", s.plural, s.item)
	cb.writeln("// terminating the array once f returns without error. It is the ordinary way")
	cb.writeln("// to produce such a stream:")
	cb.writeln("//")
	cb.writef("//\terr := Write%s(w, func(s *%s) error {\n", s.plural, writer)
	cb.writef("//\t\tfor _, v := range %s {\n", strings.ToLower(s.plural))
	cb.writeln("//\t\t\tif err := s.Write(v); err != nil {")
	cb.writeln("//\t\t\t\treturn err")
	cb.writeln("//\t\t\t}")
	cb.writeln("//\t\t}")
	cb.writeln("//\t\treturn nil")
	cb.writeln("//\t})")
	cb.writeln("//")
	cb.writeln("// If f returns an error the array is left unterminated, since a partial array")
	cb.writeln("// should not be presented as a complete one.")
	cb.writef("func Write%s(w io.Writer, f func(*%s) error, opts ...avro.ArrayWriterOption) error {\n", s.plural, writer)
	cb.writeln("\treturn avro.WriteArray(avro.NewBinaryWriter(w), func(a *avro.ArrayWriter) error {")
	cb.writef("\t\treturn f(&%s{ArrayWriter: a})\n", writer)
	cb.writeln("\t}, opts...)")
	cb.writeln("}")
}
