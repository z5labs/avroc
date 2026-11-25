// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"errors"
	"fmt"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/ir"
)

// checkIdentifiers refuses a descriptor whose generated Go identifiers would
// collide, and refuses it before a file has been written (#247).
//
// No naming scheme can prevent a collision. An Avro name can be any
// [A-Za-z_][A-Za-z0-9_]*, so every identifier this generator derives — a
// MarshalAvroBinary method, a TestRecordNullableHashUnion type, a constructor
// for a stream — is also the identifier some field, record or enum symbol
// would be given. Before this check such a descriptor generated with exit
// zero and handed back a package that did not compile, which is the generator
// claiming a success it had not had. So the generator detects instead.
//
// Go asks two things of the identifiers in one generated package, and they are
// the two kinds of scope registered here:
//
//   - the package block, across every schema in the descriptor, since every
//     schema in one invocation is written into one package; and
//   - each declared type's fields and methods together, since Go rejects a
//     struct whose field and method share a name exactly as it rejects two
//     fields that do.
//
// This is a table of the *Go* identifiers the generator is about to emit and
// not a symbol table over Avro names, which docs/ir/SPEC.md forbids a generator
// to build: what an Avro name means was settled by avroc's resolver, and what
// this generator makes of it in Go is the one thing only this generator knows.
//
// It is the one place identifiers are registered. Anything new this generator
// emits is declared in declareGenerated or declareStream below rather than
// checked somewhere of its own. Every name derived from the schema comes from
// the function the emission takes it from — toPascalCase, unionTypeName,
// unionMemberName, unionMarker, the arrayStream methods — and so does the one
// condition, hasFingerprint. The fixed method names (MarshalAvroBinary and the
// rest) are written out here and in their emitters both, and
// TestTheRegistryIsEverythingTheGeneratorDeclares is what holds the two
// together: it parses what the generator wrote and fails on any identifier
// declared there and not here, or here and not there.
//
// It runs over the whole descriptor before the generation loop for
// checkSingleObjectRoots' reason: that loop writes as it goes. And every
// collision is reported rather than the first, in the order the descriptor
// reaches them, because a person fixing one schema should not have to run the
// generator once per name to find the rest.
func checkIdentifiers(schemas []*avrocpb.Schema, opts genOptions) error {
	reg, err := declareIdentifiers(schemas, opts)
	if err != nil {
		return err
	}
	return errors.Join(reg.collisions...)
}

// declareIdentifiers registers every identifier generating schemas would
// emit, and the collisions between them.
//
// It is separate from checkIdentifiers so that the registry itself can be
// compared against what the generator actually emits: a registry that missed
// an identifier would pass every collision test it was never written for.
func declareIdentifiers(schemas []*avrocpb.Schema, opts genOptions) (*identifierRegistry, error) {
	reg := &identifierRegistry{pkg: newIdentifierScope("", "the package")}
	named := collectNamedTypes(schemas)

	for _, schema := range schemas {
		// A schema Validate refuses is one this generator cannot represent,
		// and the names it would be given are not names worth reporting a
		// collision between. Its own diagnostic is the useful one.
		if err := ir.Validate(schema); err != nil {
			return nil, err
		}

		base := ir.SchemaBaseName(schema)
		primary := schemaRecordName(schema)
		for _, t := range collectTypes(schema) {
			reg.declareGenerated(base, t, opts, primary, named)
		}
		if stream := arrayStreamFor(schema); stream != nil {
			reg.declareStream(base, *stream)
		}
	}
	return reg, nil
}

// declaration is one identifier the generator is about to emit, together with
// what produced it, which is everything a collision diagnostic has to say.
type declaration struct {
	// schema is the base name of the schema the identifier came from
	// (ir.SchemaBaseName), which is what its file name is derived from.
	schema string
	// source is the record, field, symbol or generated construct within it.
	source string
}

func (d declaration) String() string {
	return fmt.Sprintf("%s in schema %q", d.source, d.schema)
}

// identifierScope is one Go scope: a set of identifiers that must be distinct.
type identifierScope struct {
	// owner is the type whose fields and methods the scope holds, or the empty
	// string for the package block.
	owner string
	// label names the scope in a diagnostic, e.g. "the package" or
	// "the fields and methods of type TestRecord".
	label string
	seen  map[string]declaration
}

func newIdentifierScope(owner, label string) *identifierScope {
	return &identifierScope{owner: owner, label: label, seen: make(map[string]declaration)}
}

// identifierRegistry is every scope of one generated package, and the
// collisions found in them so far.
type identifierRegistry struct {
	pkg *identifierScope
	// types is the member scope of every type declared, in declaration order.
	types      []*identifierScope
	collisions []error
}

// declare records name in scope, and a collision if it is already there.
func (reg *identifierRegistry) declare(scope *identifierScope, name string, d declaration) {
	if prev, ok := scope.seen[name]; ok {
		reg.collisions = append(reg.collisions, fmt.Errorf(
			"generated identifier %s collides in %s: %s and %s",
			name, scope.label, prev, d,
		))
		return
	}
	scope.seen[name] = d
}

// declareType records a type in the package block and returns the scope its
// fields and methods are declared in.
//
// Each declaration gets a scope of its own, even when its name collides with
// another type's: the collision is already reported at package scope, and
// pooling two types' members would report every method they both have as a
// second collision that is only the first one again.
func (reg *identifierRegistry) declareType(name string, d declaration) *identifierScope {
	reg.declare(reg.pkg, name, d)
	members := newIdentifierScope(name, "the fields and methods of type "+name)
	reg.types = append(reg.types, members)
	return members
}

// declareGenerated declares everything one collected type emits: the
// declarations generateType writes, the methods generateMarshalMethod,
// generateDirectMethods and generateUnmarshalMethod write, and the Fingerprint
// generateFileCode writes for the schema's primary record, and the
// Compare<Record>Avro function generateCompareFunc writes for every record
// under compare=true, and the <Record>View, its members and its offset
// constants generateView writes for every record under views=true.
func (reg *identifierRegistry) declareGenerated(schema string, ttg typeToGenerate, opts genOptions, primary string, named namedTypes) {
	at := func(source string) declaration { return declaration{schema: schema, source: source} }

	switch v := ttg.typ.GetType().(type) {
	case *avrocpb.Type_Record:
		r := v.Record
		label := "record " + avroName(r.GetFullName(), r.GetName())
		members := reg.declareType(toPascalCase(r.GetName()), at(label))

		for _, f := range r.GetFields() {
			reg.declare(members, toFieldName(f.GetName()), at(fmt.Sprintf("field %s of %s", f.GetName(), label)))
		}
		methods := []string{"MarshalAvroBinary", "SizeAvro", "AppendAvro", "UnmarshalAvroBinary"}
		if hasFingerprint(r, primary, opts.singleObject) {
			methods = append(methods, "Fingerprint")
		}
		if opts.compare {
			reg.declare(reg.pkg, compareFuncName(r), at("Compare function generated for "+label))
		}
		for _, m := range methods {
			reg.declare(members, m, at(fmt.Sprintf("method %s generated for %s", m, label)))
		}
		if opts.views {
			reg.declareView(r, label, at, named, opts.compare)
		}

	case *avrocpb.Type_EnumType:
		e := v.EnumType
		name := toPascalCase(e.GetName())
		label := "enum " + avroName(e.GetFullName(), e.GetName())
		members := reg.declareType(name, at(label))

		for _, sym := range e.GetValues() {
			reg.declare(reg.pkg, name+toPascalCase(sym.GetValue()),
				at(fmt.Sprintf("constant for symbol %s of %s", sym.GetValue(), label)))
		}
		for _, m := range []string{"MarshalAvroBinary", "SizeAvro", "AppendAvro", "UnmarshalAvroBinary"} {
			reg.declare(members, m, at(fmt.Sprintf("method %s generated for %s", m, label)))
		}

	case *avrocpb.Type_Fixed:
		f := v.Fixed
		label := "fixed " + avroName(f.GetFullName(), f.GetName())
		members := reg.declareType(toPascalCase(f.GetName()), at(label))
		for _, m := range []string{"MarshalAvroBinary", "SizeAvro", "AppendAvro", "UnmarshalAvroBinary"} {
			reg.declare(members, m, at(fmt.Sprintf("method %s generated for %s", m, label)))
		}

	case *avrocpb.Type_Union:
		unionName := unionTypeName(ttg.recordName, ttg.fieldName)
		marker := unionMarker(unionName)
		label := "the union"
		if ttg.fieldName != "" {
			label = fmt.Sprintf("the union of field %s of record %s", ttg.fieldName, ttg.recordName)
		}

		iface := reg.declareType(unionName, at("interface for "+label))
		for _, m := range []string{marker, "MarshalAvroBinary", "SizeAvro", "AppendAvro"} {
			reg.declare(iface, m, at(fmt.Sprintf("method %s of the interface for %s", m, label)))
		}
		reg.declare(reg.pkg, "unmarshal"+unionName, at("unmarshal function for "+label))

		for _, branch := range v.Union.GetTypes() {
			branchLabel := fmt.Sprintf("branch type for %s in %s", branchName(branch), label)
			members := reg.declareType(unionMemberName(unionName, branch), at(branchLabel))
			if !isNullType(branch) {
				reg.declare(members, "Value", at("field Value of the "+branchLabel))
			}
			for _, m := range []string{marker, "MarshalAvroBinary", "SizeAvro", "AppendAvro"} {
				reg.declare(members, m, at(fmt.Sprintf("method %s of the %s", m, branchLabel)))
			}
		}
	}
}

// declareView declares r's view as generateView writes it: the type, its
// offset constants in the package block, and its fields and methods —
// viewPlan.members, which the emitter reads too.
//
// A record readOptions would have refused has no plan, and declares nothing:
// its refusal is the diagnostic worth reading, and it is reached first.
func (reg *identifierRegistry) declareView(r *avrocpb.Record, label string, at func(string) declaration, named namedTypes, compare bool) {
	plan, err := named.viewPlanFor(r)
	if err != nil {
		return
	}
	view := reg.declareType(plan.viewName, at("view type generated for "+label))
	for _, f := range plan.fields {
		if f.offsetConst != "" {
			reg.declare(reg.pkg, f.offsetConst, at(fmt.Sprintf("offset constant for field %s of %s", f.name, label)))
		}
	}
	for _, m := range plan.members(compare) {
		reg.declare(view, m.name, at(fmt.Sprintf("%s of the view generated for %s", m.source, label)))
	}
}

// declareStream declares the streaming reader and writer an array root earns,
// as generateStreamReader and generateStreamWriter write them.
//
// The methods the embedded avro.ArrayReader and avro.ArrayWriter promote are
// not declared: they are promoted from a deeper level than anything declared
// on the wrapper, so Go does not count them against it, and none of them is
// derived from a name in the schema.
func (reg *identifierRegistry) declareStream(schema string, s arrayStream) {
	at := func(source string) declaration { return declaration{schema: schema, source: source} }
	label := fmt.Sprintf("the stream of %s generated for the array root", s.item)

	reader := reg.declareType(s.reader(), at("reader type for "+label))
	reg.declare(reader, "ArrayReader", at("embedded field ArrayReader of the reader for "+label))
	reg.declare(reader, "All", at("method All of the reader for "+label))
	reg.declare(reg.pkg, s.newReader(), at("reader constructor for "+label))
	reg.declare(reg.pkg, s.streamFunc(), at("Stream function for "+label))

	writer := reg.declareType(s.writer(), at("writer type for "+label))
	reg.declare(writer, "ArrayWriter", at("embedded field ArrayWriter of the writer for "+label))
	reg.declare(writer, "Write", at("method Write of the writer for "+label))
	reg.declare(reg.pkg, s.newWriter(), at("writer constructor for "+label))
	reg.declare(reg.pkg, s.writeFunc(), at("Write function for "+label))
}

// branchName names a union branch for a diagnostic: a named type defined in
// the branch by its constructor and full name, and anything else as
// typeConstructor names it.
func branchName(t *avrocpb.Type) string {
	if full := ir.FullName(t); full != "" {
		return typeConstructor(t) + " " + full
	}
	return typeConstructor(t)
}

// avroName is a named type's full name for a diagnostic, falling back to its
// simple name for a type the producer gave no full name.
func avroName(fullName, name string) string {
	if fullName != "" {
		return fullName
	}
	return name
}
