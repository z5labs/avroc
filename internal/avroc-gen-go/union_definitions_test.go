// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/z5labs/avroc/avrocpb"
	"google.golang.org/protobuf/proto"
)

// The builders below produce resolved IR the way avroc writes it: a named type
// is defined in full where it is first referenced, so placing a definition in
// a union branch is how a test puts it where #235 found the generator not
// looking.

const unionTestNamespace = "com.example"

func recordOf(name string, fields ...*avrocpb.Field) *avrocpb.Type {
	return &avrocpb.Type{Type: &avrocpb.Type_Record{Record: &avrocpb.Record{
		Name:      proto.String(name),
		Namespace: proto.String(unionTestNamespace),
		FullName:  proto.String(unionTestNamespace + "." + name),
		Fields:    fields,
	}}}
}

func fieldOf(name string, t *avrocpb.Type) *avrocpb.Field {
	return &avrocpb.Field{Name: proto.String(name), Type: t}
}

func enumOf(name string, symbols ...string) *avrocpb.Type {
	values := make([]*avrocpb.Ident, 0, len(symbols))
	for _, s := range symbols {
		values = append(values, &avrocpb.Ident{Value: proto.String(s)})
	}
	return &avrocpb.Type{Type: &avrocpb.Type_EnumType{EnumType: &avrocpb.Enum{
		Name:      proto.String(name),
		Namespace: proto.String(unionTestNamespace),
		FullName:  proto.String(unionTestNamespace + "." + name),
		Values:    values,
	}}}
}

func fixedOf(name string, size int32) *avrocpb.Type {
	return &avrocpb.Type{Type: &avrocpb.Type_Fixed{Fixed: &avrocpb.Fixed{
		Name:      proto.String(name),
		Namespace: proto.String(unionTestNamespace),
		FullName:  proto.String(unionTestNamespace + "." + name),
		Size:      proto.Int32(size),
	}}}
}

func unionOf(branches ...*avrocpb.Type) *avrocpb.Type {
	return &avrocpb.Type{Type: &avrocpb.Type_Union{Union: &avrocpb.Union{Types: branches}}}
}

func arrayOf(items *avrocpb.Type) *avrocpb.Type {
	return &avrocpb.Type{Type: &avrocpb.Type_Array{Array: &avrocpb.Array{Items: items}}}
}

func mapOf(values *avrocpb.Type) *avrocpb.Type {
	return &avrocpb.Type{Type: &avrocpb.Type_MapType{MapType: &avrocpb.Map{Values: values}}}
}

// collectedNames is the Go name of every type collectTypes returns, in order.
func collectedNames(types []typeToGenerate) []string {
	names := make([]string, 0, len(types))
	for _, ttg := range types {
		if ttg.typ.GetUnion() != nil {
			names = append(names, unionTypeName(ttg.recordName, ttg.fieldName))
			continue
		}
		names = append(names, goTypeForType(ttg.typ, ""))
	}
	return names
}

// TestANamedTypeDefinedInAUnionBranchIsCollected is the collection itself, for
// every position the definition can be reached in from a union. It asserts the
// order as well as the membership, because the order is the order the file is
// written in, and a definition comes before the union built from it for the
// reason a record's nested types come before the record.
func TestANamedTypeDefinedInAUnionBranchIsCollected(t *testing.T) {
	null := primType("null")

	testCases := []struct {
		name   string
		fields []*avrocpb.Field
		want   []string
	}{
		{
			name:   "an enum branch",
			fields: []*avrocpb.Field{fieldOf("tone", unionOf(null, enumOf("Tone", "LOW", "HIGH")))},
			want:   []string{"Tone", "OuterToneUnion", "Outer"},
		},
		{
			name:   "a record branch",
			fields: []*avrocpb.Field{fieldOf("inner", unionOf(null, recordOf("Inner", fieldOf("x", primType("int")))))},
			want:   []string{"Inner", "OuterInnerUnion", "Outer"},
		},
		{
			name:   "a fixed branch",
			fields: []*avrocpb.Field{fieldOf("hash", unionOf(null, fixedOf("Hash", 4)))},
			want:   []string{"Hash", "OuterHashUnion", "Outer"},
		},
		{
			name:   "the issue's schema",
			fields: []*avrocpb.Field{fieldOf("id", primType("long")), fieldOf("tone", unionOf(null, enumOf("Tone", "LOW", "HIGH"))), fieldOf("inner", unionOf(null, recordOf("Inner", fieldOf("x", primType("int")))))},
			want:   []string{"Tone", "OuterToneUnion", "Inner", "OuterInnerUnion", "Outer"},
		},
		{
			name:   "a union inside an array",
			fields: []*avrocpb.Field{fieldOf("items", arrayOf(unionOf(null, recordOf("InArr", fieldOf("a", primType("int"))))))},
			want:   []string{"InArr", "OuterItemsUnion", "Outer"},
		},
		{
			name:   "a union inside a map",
			fields: []*avrocpb.Field{fieldOf("keyed", mapOf(unionOf(null, enumOf("InMap", "A"))))},
			want:   []string{"InMap", "OuterKeyedUnion", "Outer"},
		},
		{
			name: "a union inside a record that is itself a union branch",
			fields: []*avrocpb.Field{fieldOf("wrap", unionOf(null, recordOf("Wrapper",
				fieldOf("deep", unionOf(null, recordOf("Deep", fieldOf("d", primType("string")))))))),
			},
			want: []string{"Deep", "WrapperDeepUnion", "Wrapper", "OuterWrapUnion", "Outer"},
		},
		{
			name:   "a map's values",
			fields: []*avrocpb.Field{fieldOf("values", mapOf(recordOf("MapVal", fieldOf("v", primType("long")))))},
			want:   []string{"MapVal", "Outer"},
		},
		{
			name:   "an array in a union branch",
			fields: []*avrocpb.Field{fieldOf("listed", unionOf(null, arrayOf(enumOf("InList", "A"))))},
			want:   []string{"InList", "OuterListedUnion", "Outer"},
		},
		{
			name:   "a map in a union branch",
			fields: []*avrocpb.Field{fieldOf("mapped", unionOf(null, mapOf(fixedOf("InUMap", 2))))},
			want:   []string{"InUMap", "OuterMappedUnion", "Outer"},
		},
		{
			// A union reached through a branch's array would be named exactly as
			// the union holding it, so it is not collected: one declaration of
			// OuterNestedUnion, not two. The type its branch defines still is.
			name:   "a union inside an array in a union branch",
			fields: []*avrocpb.Field{fieldOf("nested", unionOf(null, arrayOf(unionOf(null, enumOf("Leaf", "A")))))},
			want:   []string{"Leaf", "OuterNestedUnion", "Outer"},
		},
		{
			// A reference defines nothing: the type was written out earlier.
			name:   "a reference branch",
			fields: []*avrocpb.Field{fieldOf("ref", unionOf(null, &avrocpb.Type{Type: &avrocpb.Type_Reference{Reference: namedRef("com.example.Elsewhere")}}))},
			want:   []string{"OuterRefUnion", "Outer"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			schema := &avrocpb.Schema{Namespace: proto.String(unionTestNamespace), Type: recordOf("Outer", tc.fields...)}

			got := collectedNames(collectTypes(schema))
			if !slices.Equal(got, tc.want) {
				t.Errorf("collected %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAMapRootCollectsItsValuesDefinition is the map case at the root rather
// than in a field, which collectTypeAndNested reaches without a record around
// it.
func TestAMapRootCollectsItsValuesDefinition(t *testing.T) {
	schema := &avrocpb.Schema{
		Namespace: proto.String(unionTestNamespace),
		Type:      mapOf(recordOf("MapVal", fieldOf("v", primType("long")))),
	}

	got := collectedNames(collectTypes(schema))
	if want := []string{"MapVal"}; !slices.Equal(got, want) {
		t.Errorf("collected %v, want %v", got, want)
	}
}

// unionHolderSchema is every position #235 asks about in one record, so that
// one compilation covers all of them together, which is where two of them
// declaring the same name would show.
func unionHolderSchema() *avrocpb.Schema {
	null := primType("null")
	return &avrocpb.Schema{
		Namespace: proto.String(unionTestNamespace),
		Type: recordOf("Holder",
			fieldOf("id", primType("long")),
			fieldOf("tone", unionOf(null, enumOf("Tone", "LOW", "HIGH"))),
			fieldOf("hash", unionOf(null, fixedOf("Hash", 4))),
			fieldOf("wrap", unionOf(null, recordOf("Wrapper",
				fieldOf("deep", unionOf(null, recordOf("Deep", fieldOf("d", primType("string")))))))),
			fieldOf("items", arrayOf(unionOf(null, recordOf("InArr", fieldOf("a", primType("int")))))),
			fieldOf("keyed", mapOf(unionOf(null, recordOf("InMap", fieldOf("m", primType("int")))))),
			fieldOf("values", mapOf(recordOf("MapVal", fieldOf("v", primType("long"))))),
			fieldOf("listed", unionOf(null, arrayOf(enumOf("InList", "A", "B")))),
			fieldOf("mapped", unionOf(null, mapOf(fixedOf("InUMap", 2)))),
		),
	}
}

// TestUnionBranchDefinitionsCompileAndRoundTrip builds the generated code for
// unionHolderSchema in a module of its own and runs
// testdata/unionholder/holder_test.go against it.
//
// Everything else in this package asserts about source text, which can say a
// declaration was written and cannot say that the package compiles — the
// failure #235 was. example/unions covers the schema from the issue through
// avroc itself; this covers the positions the IDL parser cannot express, such
// as a union as an array's items or a map's values, which only a descriptor
// built by hand reaches.
func TestUnionBranchDefinitionsCompileAndRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and tests a module of its own")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go toolchain on PATH to build the generated code with: %v", err)
	}

	root := unionTestModuleRoot(t)
	dir := t.TempDir()

	res, err := generateToDir(t, filepath.Join(dir, "holder"), &avrocpb.GenerateRequest{
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("holder")}},
		Schemas: []*avrocpb.Schema{unionHolderSchema()},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if len(res.OutputFiles) != 1 {
		t.Fatalf("generated %v, want one file", res.OutputFiles)
	}

	// The module requires this one through a replace, so the avro-go it
	// builds against is the version this repository pins rather than a
	// version written down a second time here; -mod=mod below lets it record
	// that requirement for itself, as avrocpb's out-of-tree build does.
	writeTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/unionholder\n\ngo 1.26.0\n\nrequire github.com/z5labs/avroc v0.0.0\n\nreplace github.com/z5labs/avroc => "+root+"\n")
	sum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatalf("read go.sum: %v", err)
	}
	writeTestFile(t, filepath.Join(dir, "go.sum"), string(sum))

	roundTrip, err := os.ReadFile(filepath.Join("testdata", "unionholder", "holder_test.go"))
	if err != nil {
		t.Fatalf("read round-trip test: %v", err)
	}
	writeTestFile(t, filepath.Join(dir, "holder", "holder_test.go"), string(roundTrip))

	cmd := exec.Command(goBin, "test", "-mod=mod", "-count=1", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		code, _ := os.ReadFile(res.OutputFiles[0])
		t.Fatalf("generated code failed to build or round-trip: %v\n%s\n\ngenerated:\n%s", err, out, code)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// unionTestModuleRoot walks up from the test's working directory to the
// directory holding go.mod.
func unionTestModuleRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod found above %q", dir)
		}
		dir = parent
	}
}
