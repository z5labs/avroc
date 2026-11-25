// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocgengo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/z5labs/avroc/avrocpb"
	"google.golang.org/protobuf/proto"
)

// constantSizeSchema puts a primitive whose size does not depend on its value
// — boolean, float, double — in every container position: an array's items, a
// map's values, an array nested in an array, a map nested in an array, and an
// array in a union's branch. SizeAvro sizes such a container by multiplying,
// and the loop it would otherwise have written would mention its variable
// nowhere, which Go refuses to compile.
func constantSizeSchema() *avrocpb.Schema {
	return &avrocpb.Schema{
		Namespace: proto.String(unionTestNamespace),
		Type: recordOf("Readings",
			fieldOf("flags", arrayOf(primType("boolean"))),
			fieldOf("ratios", arrayOf(primType("float"))),
			fieldOf("weights", arrayOf(primType("double"))),
			fieldOf("weightByKey", mapOf(primType("double"))),
			fieldOf("flagByKey", mapOf(primType("boolean"))),
			fieldOf("flagGrid", arrayOf(arrayOf(primType("boolean")))),
			fieldOf("ratioRows", arrayOf(mapOf(primType("float")))),
			fieldOf("maybeRatios", unionOf(primType("null"), arrayOf(primType("float")))),
		),
	}
}

// TestContainersOfConstantSizeValuesCompileAndAreSized builds the generated
// code for constantSizeSchema and runs testdata/constantsize against it, which
// holds a non-empty value of every field to SizeAvro and AppendAvro's
// invariants and pins its bytes.
func TestContainersOfConstantSizeValuesCompileAndAreSized(t *testing.T) {
	testGeneratedPackage(t, "constantsize", filepath.Join("testdata", "constantsize", "readings_test.go"),
		constantSizeSchema())
}

// TestAContainerOfConstantSizeValuesIsSizedByMultiplying reads the generated
// source for where each container of constant-size values is sized.
func TestAContainerOfConstantSizeValuesIsSizedByMultiplying(t *testing.T) {
	code := generateString(t, constantSizeSchema())
	size := between(t, code, "func (x *Readings) SizeAvro() int {", "\n}\n")

	for _, want := range []string{
		"n += len(x.Flags) * 1",
		"n += len(x.Ratios) * 4",
		"n += len(x.Weights) * 8",
		"n += len(x.WeightByKey) * 8\n\t\tfor k := range x.WeightByKey {\n\t\t\tn += avro.SizeBytes(len(k))",
		"n += len(x.FlagGrid[i]) * 1",
		"n += len(x.RatioRows[i]) * 4\n\t\t\t\tfor k1 := range x.RatioRows[i] {",
	} {
		if !strings.Contains(size, want) {
			t.Errorf("SizeAvro does not contain %q:\n%s", want, size)
		}
	}

	branch := between(t, code, "func (x ReadingsMaybeRatiosUnionArray) SizeAvro() int {", "\n}\n")
	if !strings.Contains(branch, "n += len(x.Value) * 4") {
		t.Errorf("the array branch's SizeAvro is not sized by multiplying:\n%s", branch)
	}
}

// generateString generates schemas into a package and returns the first
// file's source, checked to be valid Go.
func generateString(t *testing.T, schemas ...*avrocpb.Schema) string {
	t.Helper()
	res, err := generateToDir(t, t.TempDir(), &avrocpb.GenerateRequest{
		Options: []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("gen")}},
		Schemas: schemas,
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	b, err := os.ReadFile(res.OutputFiles[0])
	if err != nil {
		t.Fatalf("read %s: %v", res.OutputFiles[0], err)
	}
	validateGoSyntax(t, string(b))
	return string(b)
}

// TestTheDirectEncodingInvariantsAreOneFile holds the two copies of the
// invariants SizeAvro and AppendAvro are checked against to each other. One
// is copied into every module testGeneratedPackage builds, which cannot import
// from this repository's tree; the other is a package of the example, which
// imports the committed generated code. They are byte-identical apart from the
// package clause, so a change to the invariants is a change to both.
func TestTheDirectEncodingInvariantsAreOneFile(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(b)
	}
	generator := read(filepath.Join("testdata", "directencoding", "directencoding_test.go"))
	example := read(filepath.Join("..", "..", "example", "directencoding", "invariants_test.go"))

	const generatorClause, exampleClause = "\npackage directencoding\n", "\npackage directencoding_test\n"
	if !strings.Contains(example, exampleClause) {
		t.Fatalf("the example's copy no longer declares %q", strings.TrimSpace(exampleClause))
	}
	if got := strings.Replace(example, exampleClause, generatorClause, 1); got != generator {
		t.Errorf("example/directencoding/invariants_test.go has drifted from testdata/directencoding/directencoding_test.go; they differ by more than the package clause")
	}
}
