// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"strings"
	"testing"

	"github.com/z5labs/avroc/avrocpb"

	"google.golang.org/protobuf/proto"
)

// TestAnArrayRootGeneratesAStreamingReader is the story: schema array<Event>;
// says the file it describes is a stream of Event, and what this generator used
// to emit for it was Event and nothing else.
func TestAnArrayRootGeneratesAStreamingReader(t *testing.T) {
	content := renderSchema(t, "stream", arrayRootSchema())
	code := string(content)
	validateGoSyntax(t, code)

	expectations := []string{
		// The item type and its methods are still generated: the reader is in
		// addition to them, not instead of them.
		"type Event struct",
		"func (x *Event) UnmarshalAvroBinary(r *avro.BinaryReader) error",

		// The reader itself.
		"type EventReader struct {\n\t*avro.ArrayReader\n}",
		"func NewEventReader(r io.Reader) *EventReader",
		"avro.NewArrayReader(avro.NewBinaryReader(r))",

		// The iterator, and the package-level entry point named after it.
		"func (x *EventReader) All() iter.Seq2[*Event, error]",
		"func StreamEvents(r io.Reader) iter.Seq2[*Event, error]",

		// The imports the above needs.
		"\"io\"",
		"\"iter\"",
	}
	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

// TestTheStreamingReaderReimplementsNoBlockFraming holds the split this story
// was written around: the block framing is type-independent, so it lives in
// avro-go and the generated code is a wrapper over it. A generated long count,
// or a generated test of it for a negative one, would be a second
// implementation of the framing to keep correct — one copy per schema.
func TestTheStreamingReaderReimplementsNoBlockFraming(t *testing.T) {
	content := renderSchema(t, "stream", arrayRootSchema())

	// Everything the reader does with the stream, it does through these.
	stream := streamSection(t, string(content), "type EventReader struct", "type EventWriter struct")
	for _, allowed := range []string{
		"avro.NewArrayReader(avro.NewBinaryReader(r))",
		"x.ArrayReader.Next(v)",
	} {
		if !strings.Contains(stream, allowed) {
			t.Errorf("expected the streaming reader to call %q, got:\n%s", allowed, stream)
		}
	}

	// And these are the framing, which it must not be doing itself.
	for _, forbidden := range []string{"ReadLong", "WriteLong", "io.CopyN", "io.Discard"} {
		if strings.Contains(stream, forbidden) {
			t.Errorf("the streaming reader re-implements block framing: it references %q in:\n%s", forbidden, stream)
		}
	}
}

// streamSection returns the generated file from marker up to the next
// top-level declaration that is not part of the same section, so an assertion
// about the reader or the writer is not satisfied or defeated by code
// elsewhere in the file.
func streamSection(t *testing.T, code, marker, end string) string {
	t.Helper()

	i := strings.Index(code, marker)
	if i < 0 {
		t.Fatalf("generated code has no %q:\n%s", marker, code)
	}
	section := code[i:]
	if end != "" {
		if j := strings.Index(section, end); j >= 0 {
			section = section[:j]
		}
	}
	return section
}

// TestAnArrayRootGeneratesAStreamingWriter is the other half of the story: a
// stream nothing can produce is a stream, and what this generator used to emit
// for an array root took a []Event that already existed.
func TestAnArrayRootGeneratesAStreamingWriter(t *testing.T) {
	content := renderSchema(t, "stream", arrayRootSchema())
	code := string(content)
	validateGoSyntax(t, code)

	expectations := []string{
		// The writer, over avro-go's, whose Close is left promoted rather than
		// wrapped because terminating the array is what it does.
		"type EventWriter struct {\n\t*avro.ArrayWriter\n}",
		"func NewEventWriter(w io.Writer, opts ...avro.ArrayWriterOption) *EventWriter",
		"avro.NewArrayWriter(avro.NewBinaryWriter(w), opts...)",

		// Write is the one promoted method that is shadowed, so that the
		// stream's item type is the compiler's to enforce.
		"func (x *EventWriter) Write(v *Event) error",
		"return x.ArrayWriter.Write(v)",

		// And the package-level entry point, which owns the close.
		"func WriteEvents(w io.Writer, f func(*EventWriter) error, opts ...avro.ArrayWriterOption) error",
		"avro.WriteArray(avro.NewBinaryWriter(w)",
	}
	for _, exp := range expectations {
		if !strings.Contains(code, exp) {
			t.Errorf("expected generated code to contain %q, got:\n%s", exp, code)
		}
	}
}

// TestTheStreamingWriterReimplementsNoBlockFraming is the reader's rule applied
// to the writer, where there is more to get wrong: the item count, the negated
// count and byte size a sized block carries, the buffer that a size prefix
// forces, and the zero-count block that terminates the array are all
// type-independent, so all of them stay in avro-go.
func TestTheStreamingWriterReimplementsNoBlockFraming(t *testing.T) {
	content := renderSchema(t, "stream", arrayRootSchema())

	writer := streamSection(t, string(content), "type EventWriter struct", "")
	for _, allowed := range []string{
		"avro.NewArrayWriter(avro.NewBinaryWriter(w), opts...)",
		"x.ArrayWriter.Write(v)",
		"avro.WriteArray(avro.NewBinaryWriter(w)",
	} {
		if !strings.Contains(writer, allowed) {
			t.Errorf("expected the streaming writer to call %q, got:\n%s", allowed, writer)
		}
	}

	// The framing, the buffering it would need, and a Close of its own — a
	// generated Close would be a second place the terminating block is decided.
	code := codeOnly(writer)
	for _, forbidden := range []string{
		"WriteLong", "ReadLong", "WriteFixed", "bytes.Buffer", "func (x *EventWriter) Close",
	} {
		if strings.Contains(code, forbidden) {
			t.Errorf("the streaming writer re-implements block framing: it references %q in:\n%s", forbidden, code)
		}
	}
}

// TestTheStreamingWriterDoesNotChooseTheBlockMode holds the one design decision
// the story called out: whether blocks carry their size is a trade the caller
// makes — unsized blocks buffer nothing, sized blocks cost a bounded buffer and
// make the output skippable — so the generator emits neither choice. It passes
// avro-go's options through, which is what keeps the default unsized and the
// buffer bound the caller's to set.
func TestTheStreamingWriterDoesNotChooseTheBlockMode(t *testing.T) {
	content := renderSchema(t, "stream", arrayRootSchema())

	writer := streamSection(t, string(content), "type EventWriter struct", "")
	if !strings.Contains(writer, "opts ...avro.ArrayWriterOption") {
		t.Errorf("the streaming writer does not accept avro-go's writer options:\n%s", writer)
	}

	// Comments are dropped first: the generated documentation names
	// avro.WithSizedBlocks because that is how a caller opts in, and telling a
	// reader where the choice is made is the opposite of making it for them.
	code := codeOnly(writer)
	for _, forbidden := range []string{"avro.WithSizedBlocks", "avro.DefaultBlockBufferSize"} {
		if strings.Contains(code, forbidden) {
			t.Errorf("the streaming writer decides the block mode for the caller: it references %q in:\n%s", forbidden, code)
		}
	}
}

// codeOnly returns s with its comment lines removed, so an assertion about what
// the generated code does is not answered by what its documentation says.
func codeOnly(s string) string {
	var b strings.Builder
	for line := range strings.Lines(s) {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}

// TestOnlyAnArrayRootGeneratesAStreamingReader keeps the reader and the writer
// to the one root shape that means "a stream of T". Every other root is a
// single value, and a single value is what the marshalling methods already are.
func TestOnlyAnArrayRootGeneratesAStreamingReader(t *testing.T) {
	testCases := []struct {
		name   string
		schema *avrocpb.Schema
		want   bool
	}{
		{
			name:   "array of records",
			schema: arrayRootSchema(),
			want:   true,
		},
		{
			name:   "array of a named reference",
			schema: arrayOfSchema(&avrocpb.Type{Type: &avrocpb.Type_Reference{Reference: namedRef("org.example.Event")}}),
			want:   true,
		},
		{
			name: "array of enums",
			schema: arrayOfSchema(&avrocpb.Type{Type: &avrocpb.Type_EnumType{EnumType: &avrocpb.Enum{
				Name:      proto.String("Kind"),
				Namespace: proto.String("org.example"),
				FullName:  proto.String("org.example.Kind"),
				Values:    []*avrocpb.Ident{{Value: proto.String("FOO")}},
			}}}),
			want: true,
		},
		{
			name: "array of fixed",
			schema: arrayOfSchema(&avrocpb.Type{Type: &avrocpb.Type_Fixed{Fixed: &avrocpb.Fixed{
				Name:      proto.String("MD5"),
				Namespace: proto.String("org.example"),
				FullName:  proto.String("org.example.MD5"),
				Size:      proto.Int32(16),
			}}}),
			want: true,
		},
		{
			// A primitive item names a Go type with no UnmarshalAvroBinary, so
			// there is nothing for avro.ArrayReader to decode into.
			name:   "array of primitives",
			schema: arrayOfSchema(primType("string")),
			want:   false,
		},
		{
			name: "array of arrays",
			schema: arrayOfSchema(&avrocpb.Type{Type: &avrocpb.Type_Array{
				Array: &avrocpb.Array{Items: primType("string")},
			}}),
			want: false,
		},
		{
			name: "array of maps",
			schema: arrayOfSchema(&avrocpb.Type{Type: &avrocpb.Type_MapType{
				MapType: &avrocpb.Map{Values: primType("string")},
			}}),
			want: false,
		},
		{
			name: "array of unions",
			schema: arrayOfSchema(&avrocpb.Type{Type: &avrocpb.Type_Union{
				Union: &avrocpb.Union{Types: []*avrocpb.Type{primType("null"), primType("string")}},
			}}),
			want: false,
		},
		{
			name: "record root",
			schema: &avrocpb.Schema{
				Namespace: proto.String("org.example"),
				Type: &avrocpb.Type{Type: &avrocpb.Type_Record{Record: &avrocpb.Record{
					Name:      proto.String("Event"),
					Namespace: proto.String("org.example"),
					FullName:  proto.String("org.example.Event"),
				}}},
			},
			want: false,
		},
		{
			name: "map root",
			schema: &avrocpb.Schema{
				Namespace: proto.String("org.example"),
				Type: &avrocpb.Type{Type: &avrocpb.Type_MapType{
					MapType: &avrocpb.Map{Values: primType("string")},
				}},
			},
			want: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := arrayStreamFor(tc.schema) != nil
			if got != tc.want {
				t.Errorf("streamReaderFor produced a reader: %t, want %t", got, tc.want)
			}

			content := renderSchema(t, "stream", tc.schema)
			code := string(content)
			validateGoSyntax(t, code)

			// The emission and the decision agree, and the imports follow the
			// emission: an unused "io" is a file that does not compile.
			for _, marker := range []string{"iter.Seq2", "avro.ArrayReader", "avro.ArrayWriter", "\"io\"", "\"iter\""} {
				if strings.Contains(code, marker) != tc.want {
					t.Errorf("generated code contains %q: %t, want %t\n%s", marker, strings.Contains(code, marker), tc.want, code)
				}
			}
		})
	}
}

// arrayOfSchema is `schema array<items>;` in the namespace the other array-root
// fixtures use.
func arrayOfSchema(items *avrocpb.Type) *avrocpb.Schema {
	return &avrocpb.Schema{
		Namespace: proto.String("org.example"),
		Type: &avrocpb.Type{
			Type: &avrocpb.Type_Array{Array: &avrocpb.Array{Items: items}},
		},
	}
}

func TestPluralize(t *testing.T) {
	testCases := []struct {
		name string
		want string
	}{
		{name: "Event", want: "Events"},
		{name: "MD5", want: "MD5s"},
		{name: "Status", want: "Statuses"},
		{name: "Box", want: "Boxes"},
		{name: "Topaz", want: "Topazes"},
		{name: "Batch", want: "Batches"},
		{name: "Hash", want: "Hashes"},
		{name: "Entry", want: "Entries"},
		{name: "Day", want: "Days"},
		{name: "Y", want: "Ys"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pluralize(tc.name); got != tc.want {
				t.Errorf("pluralize(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestPluralizeIsRegularAndOnlyRegular records what the rule gives up on, so
// that "Childs" is a decision this repository made and can be read, rather than
// a surprise the next person finds in a generated identifier.
//
// Every case here needs a fact about the word that its spelling does not carry:
// which nouns are irregular, which -ch is pronounced /k/, which single -z sits
// after a stressed syllable and so doubles. Quiz and Topaz differ in that last
// one alone, which is why neither a "z doubles" rule nor an "-es" rule is right
// for both, and why the one that can be read off the letters is the one here.
func TestPluralizeIsRegularAndOnlyRegular(t *testing.T) {
	knownWrong := map[string]string{
		"Child":   "Children",
		"Person":  "People",
		"Stomach": "Stomachs",
		"Quiz":    "Quizzes",
	}

	for name, english := range knownWrong {
		t.Run(name, func(t *testing.T) {
			if got := pluralize(name); got == english {
				t.Errorf("pluralize(%q) = %q, which is the English plural: the rule has grown a case, so this expectation is stale rather than failing", name, got)
			}
		})
	}
}
