// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avrocpb_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/z5labs/avroc/avrocpb"
)

// TestImportPathHasNoInternalElement guards the property the whole package
// move was for: Go's internal/ rule is a rule about the import path, so a
// single path element named "internal" anywhere in it puts the IR out of reach
// of the third-party generators it exists for.
func TestImportPathHasNoInternalElement(t *testing.T) {
	pkgPath := reflect.TypeOf((*avrocpb.GenerateRequest)(nil)).Elem().PkgPath()

	for _, elem := range strings.Split(pkgPath, "/") {
		if elem == "internal" {
			t.Fatalf("IR package %q is unimportable from outside this module: no element of its import path may be \"internal\"", pkgPath)
		}
	}
}

// TestOutOfTreeModuleBuildsAgainstTheIR compiles a module that is not this one
// against the IR types. The path check above is necessary and not sufficient —
// it says the compiler will allow the import, not that the package presents a
// usable surface to a caller who has only the module — so the contract is
// asserted the way its audience meets it: a separate module, a plain import,
// go build.
func TestOutOfTreeModuleBuildsAgainstTheIR(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go toolchain on PATH to build an out-of-tree module with: %v", err)
	}

	root := moduleRoot(t)
	dir := t.TempDir()

	// A replace against the working tree, so this asserts the IR in hand
	// rather than whichever version happens to be published.
	writeFile(t, filepath.Join(dir, "go.mod"), `module example.com/avroc-gen-outoftree

go 1.25.0

require github.com/z5labs/avroc v0.0.0

replace github.com/z5labs/avroc => `+root+"\n")

	// The consumer's own go.sum would list the same hashes for the same
	// transitive dependencies, so copying rather than resolving keeps the
	// test off the network.
	sum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatalf("read go.sum: %v", err)
	}
	writeFile(t, filepath.Join(dir, "go.sum"), string(sum))

	writeFile(t, filepath.Join(dir, "main.go"), `package main

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/z5labs/avroc/avrocpb"
)

func main() {
	req := &avrocpb.GenerateRequest{
		Version: proto.Int32(1),
		Schemas: []*avrocpb.Schema{
			{
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
	}

	b, err := proto.Marshal(req)
	if err != nil {
		panic(err)
	}

	fmt.Println(req.GetVersion(), len(req.GetSchemas()), len(b))
}
`)

	cmd := exec.Command(goBin, "build", "./...")
	cmd.Dir = dir
	// -mod=mod lets the throwaway module record the indirect requirements
	// it picks up from this one, which a consumer's `go get` would do for
	// them.
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("out-of-tree module failed to build against the IR: %v\n%s", err, out)
	}
}

// moduleRoot walks up from the test's working directory to the directory
// holding go.mod.
func moduleRoot(t *testing.T) string {
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

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
