// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/ir"
	"github.com/z5labs/avroc/internal/plugin"

	"google.golang.org/protobuf/proto"
)

// identifierRequest builds an ordinary request over the schemas it is given,
// asking for single-object encoding when singleObject is set.
func identifierRequest(singleObject bool, schemas ...*avrocpb.Schema) *avrocpb.GenerateRequest {
	if singleObject {
		return singleObjectRequest(schemas...)
	}
	return &avrocpb.GenerateRequest{
		Version: proto.Int32(ir.Version),
		Options: []*avrocpb.Option{
			{Name: proto.String("package_name"), Value: proto.String("gen")},
		},
		Schemas: schemas,
	}
}

func schemaOf(root *avrocpb.Type) *avrocpb.Schema {
	return &avrocpb.Schema{Namespace: proto.String(unionTestNamespace), Type: root}
}

// TestGenerationRefusesCollidingIdentifiers is #247: a descriptor whose
// generated identifiers collide used to generate with exit zero and leave a
// package that does not compile. Each case is a collision go vet reported, or
// would have, against the code this generator wrote, and each must now be a
// diagnostic naming the identifier and both of the things that produced it,
// with no file written.
func TestGenerationRefusesCollidingIdentifiers(t *testing.T) {
	testCases := []struct {
		name         string
		singleObject bool
		schemas      []*avrocpb.Schema
		// want is every fragment the diagnostic has to carry: the identifier,
		// and the two sources.
		want []string
	}{
		{
			// The first of the issue's reproductions: go vet reported "field
			// and method with the same name MarshalAvroBinary".
			name: "a field and a generated method",
			schemas: []*avrocpb.Schema{schemaOf(recordOf("TestRecord",
				fieldOf("id", primType("long")),
				fieldOf("marshalAvroBinary", primType("string")),
			))},
			want: []string{
				"MarshalAvroBinary",
				"type TestRecord",
				"field marshalAvroBinary of record com.example.TestRecord",
				"method MarshalAvroBinary generated for record com.example.TestRecord",
				`schema "TestRecord"`,
			},
		},
		{
			// The second: go vet reported "TestRecordNullableHashUnion
			// redeclared in this block". The record comes from another schema in
			// the same generator entry, so the first schema's file would have
			// been written before the second's failed to compile.
			name: "a record and a generated union type, across schemas",
			schemas: []*avrocpb.Schema{
				schemaOf(recordOf("TestRecord",
					fieldOf("nullableHash", unionOf(primType("null"), primType("bytes"))),
				)),
				schemaOf(recordOf("TestRecordNullableHashUnion",
					fieldOf("id", primType("long")),
				)),
			},
			want: []string{
				"TestRecordNullableHashUnion",
				"the package",
				"interface for the union of field nullableHash of record TestRecord",
				`schema "TestRecord"`,
				"record com.example.TestRecordNullableHashUnion",
				`schema "TestRecordNullableHashUnion"`,
			},
		},
		{
			// #249's two methods are registered like every other: a field
			// named after either is refused rather than generated.
			name: "a field and the generated SizeAvro method",
			schemas: []*avrocpb.Schema{schemaOf(recordOf("Block",
				fieldOf("sizeAvro", primType("int")),
			))},
			want: []string{
				"SizeAvro",
				"type Block",
				"field sizeAvro of record com.example.Block",
				"method SizeAvro generated for record com.example.Block",
			},
		},
		{
			name: "a field and the generated AppendAvro method",
			schemas: []*avrocpb.Schema{schemaOf(recordOf("Block",
				fieldOf("appendAvro", primType("bytes")),
			))},
			want: []string{
				"AppendAvro",
				"type Block",
				"field appendAvro of record com.example.Block",
				"method AppendAvro generated for record com.example.Block",
			},
		},
		{
			name: "two fields that are given one Go name",
			schemas: []*avrocpb.Schema{schemaOf(recordOf("Pair",
				fieldOf("a_b", primType("int")),
				fieldOf("aB", primType("int")),
			))},
			want: []string{"AB", "type Pair", "field a_b of record com.example.Pair", "field aB of record com.example.Pair"},
		},
		{
			name: "an enum constant and a record",
			schemas: []*avrocpb.Schema{
				schemaOf(enumOf("Color", "RED", "GREEN")),
				schemaOf(recordOf("ColorRED", fieldOf("id", primType("long")))),
			},
			want: []string{
				"ColorRED",
				"constant for symbol RED of enum com.example.Color",
				"record com.example.ColorRED",
			},
		},
		{
			name: "a fixed defined in full by two schemas",
			schemas: []*avrocpb.Schema{
				schemaOf(recordOf("Left", fieldOf("h", fixedOf("Hash", 4)))),
				schemaOf(recordOf("Right", fieldOf("h", fixedOf("Hash", 4)))),
			},
			want: []string{"Hash", `fixed com.example.Hash in schema "Left"`, `fixed com.example.Hash in schema "Right"`},
		},
		{
			// A record named Array and an array branch are both given the
			// branch type UnionArray, so one union declares it twice.
			name: "two union branches",
			schemas: []*avrocpb.Schema{schemaOf(recordOf("Holder",
				fieldOf("choice", unionOf(
					recordOf("Array", fieldOf("id", primType("long"))),
					arrayOf(primType("int")),
				)),
			))},
			want: []string{
				"HolderChoiceUnionArray",
				"branch type for record com.example.Array in the union of field choice of record Holder",
				"branch type for array in the union of field choice of record Holder",
			},
		},
		{
			name:         "a field and the single-object Fingerprint method",
			singleObject: true,
			schemas: []*avrocpb.Schema{schemaOf(recordOf("Event",
				fieldOf("fingerprint", primType("bytes")),
			))},
			want: []string{
				"Fingerprint",
				"field fingerprint of record com.example.Event",
				"method Fingerprint generated for record com.example.Event",
			},
		},
		{
			name: "a record and a streaming reader",
			schemas: []*avrocpb.Schema{
				schemaOf(arrayOf(recordOf("Event", fieldOf("id", primType("long"))))),
				schemaOf(recordOf("EventReader", fieldOf("id", primType("long")))),
			},
			want: []string{
				"EventReader",
				"reader type for the stream of Event generated for the array root",
				"record com.example.EventReader",
			},
		},
		{
			name: "a record and a stream's package-level function",
			schemas: []*avrocpb.Schema{
				schemaOf(arrayOf(recordOf("Event", fieldOf("id", primType("long"))))),
				schemaOf(recordOf("WriteEvents", fieldOf("id", primType("long")))),
			},
			want: []string{
				"WriteEvents",
				"Write function for the stream of Event generated for the array root",
				"record com.example.WriteEvents",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			w := plugin.NewOutputDir(dir)

			err := Generate(t.Context(), identifierRequest(tc.singleObject, tc.schemas...), w)
			if err == nil {
				t.Fatal("Generate exited zero for a descriptor whose generated identifiers collide")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("diagnostic does not carry %q:\n%s", want, err)
				}
			}
			assertNothingWritten(t, dir, w)
		})
	}
}

// TestEveryCollisionIsReported is the reason the check finishes the
// descriptor rather than stopping at the first collision: a person fixing a
// schema should learn every name at once rather than one per run. The order is
// the descriptor's, so the diagnostic is the same every time.
func TestEveryCollisionIsReported(t *testing.T) {
	dir := t.TempDir()
	w := plugin.NewOutputDir(dir)

	err := Generate(t.Context(), identifierRequest(false,
		schemaOf(recordOf("First", fieldOf("unmarshalAvroBinary", primType("long")))),
		schemaOf(recordOf("Second", fieldOf("marshal_avro_binary", primType("long")))),
	), w)
	if err == nil {
		t.Fatal("Generate exited zero for a descriptor whose generated identifiers collide")
	}

	first := strings.Index(err.Error(), "type First")
	second := strings.Index(err.Error(), "type Second")
	if first < 0 || second < 0 {
		t.Fatalf("diagnostic does not report both collisions:\n%s", err)
	}
	if first > second {
		t.Errorf("collisions are reported out of the descriptor's order:\n%s", err)
	}
	assertNothingWritten(t, dir, w)
}

// TestANameThatCollidesWithNothingGenerated is the other side of the check:
// it mirrors what is emitted and under which conditions, rather than refusing
// every name that sounds like a method. A field named fingerprint collides
// only when single-object encoding gives the record a Fingerprint method, and
// fields named like the methods #244's views will add collide with nothing
// generated today.
func TestANameThatCollidesWithNothingGenerated(t *testing.T) {
	_, err := generateToDir(t, t.TempDir(), identifierRequest(false, schemaOf(recordOf("Event",
		fieldOf("fingerprint", primType("bytes")),
		fieldOf("len", primType("long")),
		fieldOf("bytes", primType("bytes")),
		fieldOf("complete", primType("boolean")),
		fieldOf("value", primType("string")),
	))))
	if err != nil {
		t.Fatalf("Generate refused a descriptor with no colliding identifier: %v", err)
	}
}

// TestTheRegistryIsEverythingTheGeneratorDeclares holds the check to being the
// one place identifiers are registered. It parses what the generator actually
// wrote and requires every identifier declared there — in the package block,
// and among each type's fields and methods — to be one the registry declared,
// and the registry to declare nothing that was not written.
//
// Without it the check would be only as complete as its collision tests: an
// identifier a later story emits and never registers passes every case above,
// and then collides in a user's package exactly as #247 did.
func TestTheRegistryIsEverythingTheGeneratorDeclares(t *testing.T) {
	testCases := []struct {
		name         string
		singleObject bool
		compare      bool
		schemas      []*avrocpb.Schema
	}{
		{name: "every type constructor", schemas: append(determinismSchemas(), arrayRootSchema())},
		{name: "single-object encoding", singleObject: true, schemas: []*avrocpb.Schema{schemaOf(eventRecord())}},
		{name: "a union branch referring to a named type, and additional types", schemas: []*avrocpb.Schema{{
			Namespace: proto.String(unionTestNamespace),
			Type: recordOf("Referrer",
				fieldOf("kind", enumOf("Kind", "A", "B")),
				fieldOf("maybeKind", unionOf(primType("null"), namedType(unionTestNamespace+".Kind"))),
			),
			Types: []*avrocpb.Type{
				recordOf("Extra", fieldOf("choice", unionOf(primType("null"), primType("string")))),
				enumOf("Loose", "X"),
				fixedOf("Spare", 2),
			},
		}}},
		{name: "compare", compare: true, schemas: []*avrocpb.Schema{compareSampleSchema(), {
			Namespace: proto.String(unionTestNamespace),
			Type:      recordOf("Second", fieldOf("inner", namedType(unionTestNamespace+".Inner"))),
			Types:     []*avrocpb.Type{recordOf("Extra", fieldOf("id", primType("long")))},
		}}},
		{name: "a union root", schemas: []*avrocpb.Schema{
			schemaOf(unionOf(primType("null"), recordOf("Branch", fieldOf("id", primType("long"))))),
		}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := identifierRequest(tc.singleObject, tc.schemas...)
			if tc.compare {
				req = compareRequest("true", tc.schemas...)
			}
			res, err := generateToDir(t, t.TempDir(), req)
			if err != nil {
				t.Fatalf("Generate failed: %v", err)
			}
			emitted := declaredInFiles(t, res.OutputFiles)

			reg, err := declareIdentifiers(tc.schemas, genOptions{packageName: "gen", singleObject: tc.singleObject, compare: tc.compare})
			if err != nil {
				t.Fatalf("declareIdentifiers failed: %v", err)
			}
			registered := map[string][]string{"": slices.Sorted(maps.Keys(reg.pkg.seen))}
			for _, scope := range reg.types {
				registered[scope.owner] = slices.Sorted(maps.Keys(scope.seen))
			}

			for _, owner := range slices.Sorted(maps.Keys(emitted)) {
				if got, want := registered[owner], emitted[owner]; !slices.Equal(got, want) {
					t.Errorf("scope %q: registered %v, generator declared %v", owner, got, want)
				}
			}
			for owner := range registered {
				if _, ok := emitted[owner]; !ok {
					t.Errorf("scope %q is registered but nothing generated declares it", owner)
				}
			}
		})
	}
}

// declaredInFiles parses generated Go source and returns its declared
// identifiers by scope: the empty key for the package block, and each type's
// name for its fields and methods, each list sorted.
func declaredInFiles(t *testing.T, files []string) map[string][]string {
	t.Helper()

	scopes := map[string]map[string]bool{"": {}}
	add := func(owner, name string) {
		if scopes[owner] == nil {
			scopes[owner] = map[string]bool{}
		}
		scopes[owner][name] = true
	}

	fset := token.NewFileSet()
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read generated file: %v", err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("failed to parse generated file %s: %v", path, err)
		}

		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					add("", d.Name.Name)
					continue
				}
				recv := d.Recv.List[0].Type
				if star, ok := recv.(*ast.StarExpr); ok {
					recv = star.X
				}
				ident, ok := recv.(*ast.Ident)
				if !ok {
					t.Fatalf("method %s has a receiver of type %T, which this test does not know how to name", d.Name.Name, recv)
				}
				add(ident.Name, d.Name.Name)
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						add("", s.Name.Name)
						// A type with no members still has a scope, so that
						// one registered for it is compared against nothing.
						if scopes[s.Name.Name] == nil {
							scopes[s.Name.Name] = map[string]bool{}
						}
						for _, member := range typeMembers(s.Type) {
							add(s.Name.Name, member)
						}
					case *ast.ValueSpec:
						for _, name := range s.Names {
							add("", name.Name)
						}
					}
				}
			}
		}
	}

	declared := make(map[string][]string, len(scopes))
	for owner, names := range scopes {
		declared[owner] = slices.Sorted(maps.Keys(names))
	}
	return declared
}

// typeMembers is the fields of a struct type or the methods of an interface
// type, including an embedded field under the name Go gives it.
func typeMembers(expr ast.Expr) []string {
	var fields *ast.FieldList
	switch v := expr.(type) {
	case *ast.StructType:
		fields = v.Fields
	case *ast.InterfaceType:
		fields = v.Methods
	default:
		return nil
	}

	var names []string
	for _, f := range fields.List {
		if len(f.Names) == 0 {
			typ := f.Type
			if star, ok := typ.(*ast.StarExpr); ok {
				typ = star.X
			}
			if sel, ok := typ.(*ast.SelectorExpr); ok {
				names = append(names, sel.Sel.Name)
			}
			continue
		}
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
	}
	return names
}
