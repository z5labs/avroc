// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Why avroc reads no ambient temporary directory at all (#218).
//
// One line in this package used to need a writable /tmp — the descriptor
// directory, created with os.MkdirTemp("", …) — and it is the reason
// .dagger/image.go grafted a 1777 /tmp into an image whose whole claim is
// "scratch plus the files docs/container/SPEC.md names". The cost of keeping it
// was not the directory: it was that every `docker run` line in the README, in
// docs/container/SPEC.md and in the worked example would have grown a
// `--tmpfs /tmp:mode=1777`, and an adopter who forgot it would get a Go standard
// library message about a directory they never asked for, from a program that had
// not reached its own argument parsing.
//
// So the requirement was removed rather than carried: the descriptor now goes
// into the generator's own output tree (newDescriptorDir), beside the scratch
// directory --out names. This check is what keeps it removed. A single
// os.MkdirTemp("", …) reintroduced anywhere avroc is built from would put the
// mount flag back on all of those command lines, and it would do it silently —
// on the developer's machine, in CI and in every container with a /tmp, it
// simply works.
//
// It is a source scan for the reason internal/plugin's determinism check is one:
// no run of the tests can catch it, because every machine the tests run on has a
// temporary directory. Only the published image does not.
//
// What the scan cannot see is worth writing down, because it is what keeps this
// from being read as a proof. It matches names in one file at a time, so a parent
// that is empty by way of a variable — `dir := ""; os.MkdirTemp(dir, …)` — reads as
// a temporary directory with a parent, and a dependency doing any of this is
// outside the scanned tree entirely. A dot import would hide a call from the walk
// altogether, so a file dot-importing one of the packages the ban is keyed on is
// itself reported rather than scanned and passed. The end-to-end check is
// `dagger call regeneration`, whose scratch containers have no /tmp and a TMPDIR
// naming a directory that is not there: it catches every one of those, including
// the dependency, and it catches them as a failed generation rather than as a
// finding. This scan is what makes the failure legible at the line that caused it.

// moduleRoot is this module's root directory, relative to this package, which is
// what the scanned directories below are resolved from.
func moduleRoot() string {
	return filepath.Join("..", "..")
}

// tempDirScanDirs are the directories avroc and its generators are built from —
// everything that ends up in one of the four published executables is under one
// of them — written out so that a new top-level directory holding runtime code is
// a deliberate addition to this list rather than a silent gap in it.
//
// The two Dagger modules are deliberately absent: they are separate modules that
// build and check images rather than run inside one, and a temporary directory on
// the engine is not a temporary directory in a scratch container.
func tempDirScanDirs() []string {
	return []string{"internal", "cmd", "avrocpb"}
}

// ambientTempFuncs are the functions whose answer is the machine's temporary
// directory rather than a path avroc chose. Naming one at all is the finding,
// not calling it: a function value is the same read one level of indirection
// away.
func ambientTempFuncs() map[string]map[string]string {
	const ambient = "answers with the machine's temporary directory, which a scratch image has none of"

	// io/ioutil is deprecated and nothing here imports it, and both of its
	// spellings are banned outright anyway: they are what somebody copying from
	// older code reaches for, and a ban keyed on os alone would let
	// ioutil.TempDir("", …) through while reporting that avroc reads no ambient
	// temporary directory. Outright rather than only-without-a-parent, because
	// ioutil.TempDir with a parent has os.MkdirTemp to be written as instead.
	const deprecated = "is io/ioutil's temporary directory; use os.MkdirTemp with a parent"

	return map[string]map[string]string{
		"os": {
			"TempDir": ambient,
		},
		"ioutil": {
			"TempDir":  deprecated,
			"TempFile": deprecated,
		},
	}
}

// ambientTempEnv are the environment variables that name a temporary directory.
// avroc reads none of them, and the string appearing in this source at all is
// what the check reports — an avroc that consulted TMPDIR by hand would put the
// image's /tmp back without ever naming os.TempDir.
func ambientTempEnv() []string {
	return []string{"TMPDIR", "TMP", "TEMP", "TEMPDIR"}
}

// tempFuncsNeedingAParent are the functions that fall back to the machine's
// temporary directory when their first argument is empty. They are perfectly
// good with a parent — newDescriptorDir and newScratchDir are both built on one
// — so what is banned is the empty first argument and not the function.
func tempFuncsNeedingAParent() map[string][]string {
	return map[string][]string{
		"os": {"MkdirTemp", "CreateTemp"},
	}
}

// TestAvrocReadsNoAmbientTemporaryDirectory is #218 held as a property of the
// source: nothing in internal/, cmd/ or avrocpb/ asks the machine where to put a
// temporary file.
//
// Test files are excluded, and t.TempDir is why. A test may put its fixtures
// wherever the machine likes — it is not what runs inside the published image —
// and every test in this repository does exactly that.
func TestAvrocReadsNoAmbientTemporaryDirectory(t *testing.T) {
	var scanned int
	for _, dir := range tempDirScanDirs() {
		sources, err := goSourcesUnder(filepath.Join(moduleRoot(), dir))
		if err != nil {
			t.Fatalf("failed to read %s: %v", dir, err)
		}
		if len(sources) == 0 {
			t.Errorf("no non-test Go source found under %s: the check is looking in the wrong place", dir)
		}
		scanned += len(sources)

		for _, src := range sources {
			findings, err := ambientTempFindings(src)
			if err != nil {
				t.Fatalf("failed to scan %s: %v", src, err)
			}
			for _, finding := range findings {
				t.Errorf("%s (#218: avroc writes each invocation's descriptor into the generator's own output tree, so the published image needs no /tmp)", finding)
			}
		}
	}

	// A scan that read nothing passes vacuously, and the directories above are
	// relative paths that a package move would quietly invalidate.
	if scanned == 0 {
		t.Fatal("no Go source was scanned at all")
	}
}

// TestTheAmbientTemporaryDirectoryCheckCatchesEachWayOfReadingOne runs the
// check's failure path, which is otherwise never run: the committed tree passes
// by construction, so a check that had stopped reporting anything would look
// exactly the same from here.
//
// Each case is a way somebody would plausibly reintroduce the requirement, and
// the last three are the ones a check aimed only at os.TempDir would miss.
func TestTheAmbientTemporaryDirectoryCheckCatchesEachWayOfReadingOne(t *testing.T) {
	testCases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "os.TempDir called",
			body: `func f() string { return os.TempDir() }`,
			want: "os.TempDir",
		},
		{
			name: "os.TempDir taken as a value",
			body: `func f() func() string { return os.TempDir }`,
			want: "os.TempDir",
		},
		{
			name: "MkdirTemp with no parent",
			body: `func f() (string, error) { return os.MkdirTemp("", "descriptor-*") }`,
			want: "os.MkdirTemp",
		},
		{
			name: "CreateTemp with no parent",
			body: `func f() (*os.File, error) { return os.CreateTemp("", "descriptor-*") }`,
			want: "os.CreateTemp",
		},
		{
			name: "TMPDIR read by name",
			body: `func f(env map[string]string) string { return env["TMPDIR"] }`,
			want: "TMPDIR",
		},
		{
			name: "os imported under another name",
			body: `func f() string { return stdos.TempDir() }`,
			want: "TempDir",
		},
		{
			name: "io/ioutil's spelling",
			body: `func f() (string, error) { return ioutil.TempDir("", "descriptor-") }`,
			want: "ioutil.TempDir",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "scanned.go")
			source := "package scanned\n\nimport (\n\t\"io/ioutil\"\n\t\"os\"\n\tstdos \"os\"\n)\n\nvar _ = os.Getpid\nvar _ = stdos.Getpid\nvar _ = ioutil.Discard\n\n" + tc.body + "\n"
			if err := os.WriteFile(src, []byte(source), 0o644); err != nil {
				t.Fatal(err)
			}

			findings, err := ambientTempFindings(src)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(findings, func(f string) bool { return strings.Contains(f, tc.want) }) {
				t.Errorf("the check accepted %s; findings: %v", tc.body, findings)
			}
		})
	}
}

// TestTheAmbientTemporaryDirectoryCheckReportsADotImportItCannotSeeThrough is
// the one evasion the walk cannot be made to catch: `import . "os"` puts
// `TempDir()` in the file as a bare identifier, with no selector to match. The
// check reports the import itself rather than scanning the file and finding
// nothing, which is the difference between a hole and a refusal.
func TestTheAmbientTemporaryDirectoryCheckReportsADotImportItCannotSeeThrough(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "scanned.go")
	source := `package scanned

import . "os"

func f() string { return TempDir() }
`
	if err := os.WriteFile(src, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := ambientTempFindings(src)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(findings, func(f string) bool { return strings.Contains(f, "dot-imported") }) {
		t.Errorf("the check said nothing about a dot import of os; findings: %v", findings)
	}
}

// TestTheAmbientTemporaryDirectoryCheckAcceptsATemporaryDirectoryWithAParent is
// the other side of the previous test, and it is what stops the ban being
// widened into "avroc may not create a temporary directory". It may — it creates
// two per invocation — as long as it says where.
func TestTheAmbientTemporaryDirectoryCheckAcceptsATemporaryDirectoryWithAParent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "scanned.go")
	source := `package scanned

import "os"

func f(parent string) (string, error) { return os.MkdirTemp(parent, ".descriptor-") }
`
	if err := os.WriteFile(src, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := ambientTempFindings(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("the check reported %v for a temporary directory created inside a named parent", findings)
	}
}

// goSourcesUnder is every non-test Go file beneath dir, sorted.
func goSourcesUnder(dir string) ([]string, error) {
	var sources []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		sources = append(sources, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(sources)
	return sources, nil
}

// ambientTempFindings reports every way one file asks the machine for a
// temporary directory: a reference to one of [ambientTempFuncs], one of
// [tempFuncsNeedingAParent] called with an empty first argument, or one of
// [ambientTempEnv] written as a string.
//
// The import is resolved to its path rather than assumed from the local name, so
// a file importing os under another name is scanned as os and a local variable
// called os is not mistaken for the package. A **dot import** of a package the
// ban is keyed on is reported outright instead: it puts the call in the file as a
// bare identifier with no selector for the walk to match, so the alternative is
// not scanning it and saying nothing.
func ambientTempFindings(path string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}

	banned := ambientTempFuncs()
	needParent := tempFuncsNeedingAParent()

	var findings []string
	report := func(pos token.Pos, format string, args ...any) {
		findings = append(findings, fmt.Sprintf("%s: %s", fset.Position(pos), fmt.Sprintf(format, args...)))
	}

	// Local name to import path, for every import this file has.
	imports := make(map[string]string)
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, err
		}
		name := importPath[strings.LastIndex(importPath, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name == "." {
			if _, keyed := banned[importPath[strings.LastIndex(importPath, "/")+1:]]; keyed {
				report(spec.Pos(), "%q is dot-imported, which puts its functions in this file without a selector this check can see", importPath)
			}
			continue
		}
		imports[name] = importPath
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			pkg, ok := selectedPackage(node, imports)
			if !ok {
				return true
			}
			if why, banned := banned[pkg][node.Sel.Name]; banned {
				report(node.Pos(), "%s.%s %s", pkg, node.Sel.Name, why)
			}
		case *ast.CallExpr:
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := selectedPackage(sel, imports)
			if !ok {
				return true
			}
			if !slices.Contains(needParent[pkg], sel.Sel.Name) {
				return true
			}
			if len(node.Args) == 0 || !isEmptyString(node.Args[0]) {
				return true
			}
			report(node.Pos(), "%s.%s is called with no parent directory, so it falls back to the machine's temporary directory", pkg, sel.Sel.Name)
		case *ast.BasicLit:
			if node.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(node.Value)
			if err != nil {
				return true
			}
			if slices.Contains(ambientTempEnv(), value) {
				report(node.Pos(), "%q names an ambient temporary directory", value)
			}
		}
		return true
	})
	return findings, nil
}

// selectedPackage resolves the package half of a selector to its import path's
// last element, so that a file importing os under another name is still read as
// os. It reports false for a selector on anything that is not an imported
// package.
func selectedPackage(sel *ast.SelectorExpr, imports map[string]string) (string, bool) {
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	importPath, ok := imports[ident.Name]
	if !ok {
		return "", false
	}
	return importPath[strings.LastIndex(importPath, "/")+1:], true
}

// isEmptyString reports whether an argument is the literal "".
func isEmptyString(arg ast.Expr) bool {
	lit, ok := arg.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	value, err := strconv.Unquote(lit.Value)
	return err == nil && value == ""
}
