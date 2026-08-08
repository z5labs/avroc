// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package plugin

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// generatorPackages are the directories that make up the generator half of
// docs/plugin/SPEC.md: the three built-in generators, the shared IR operations
// they perform, and this package, through which all three run.
//
// They are named as paths rather than imported because the check below is a
// property of the source, not of a running program.
func generatorPackages() []string {
	return []string{
		".",
		filepath.Join("..", "avroc-gen-go"),
		filepath.Join("..", "avroc-gen-json"),
		filepath.Join("..", "avroc-gen-pcf"),
		filepath.Join("..", "ir"),
	}
}

// forbiddenFuncs maps an import path to the functions in it whose result
// differs between two runs of the same executable over the same descriptor.
// docs/plugin/SPEC.md's "Determinism" forbids their results reaching a
// generator's output; forbidding them outright is the checkable form of that,
// and costs nothing, because no generator here has a use for one.
//
// What is forbidden is naming one of them at all, not calling one: the check
// below matches any reference, so `at := time.Now` is a finding as much as
// `time.Now()` is. That is deliberate. A function value is the same clock read
// one level of indirection away, and it is exactly what somebody reaches for
// when they want the call to be hard to see.
//
// SourceDateEpoch is the sanctioned way to get a timestamp and it reads the
// environment rather than the clock, which is why time.Unix is not here.
func forbiddenFuncs() map[string]map[string]string {
	const (
		clock   = "reads the wall clock; use plugin.SourceDateEpoch, which does not"
		ambient = "reads the machine rather than the descriptor"
		env     = "reads the environment; everything that configures a generator arrives as --opt"
	)

	return map[string]map[string]string{
		"time": {
			"Now":   clock,
			"Since": clock,
			"Until": clock,
		},
		"os": {
			"Hostname":    ambient,
			"Getpid":      ambient,
			"Getppid":     ambient,
			"Getuid":      ambient,
			"Geteuid":     ambient,
			"Getgid":      ambient,
			"Getwd":       ambient,
			"UserHomeDir": ambient,
			"Getenv":      env,
			"LookupEnv":   env,
			"Environ":     env,
		},
	}
}

// forbiddenImports are packages with no deterministic use at all, so importing
// one is itself the finding. os/user answers questions about the machine; both
// randoms answer differently every time by construction.
func forbiddenImports() map[string]string {
	const (
		random  = "randomness has no place in generated output"
		ambient = "reads the machine rather than the descriptor"
	)

	return map[string]string{
		"math/rand":    random,
		"math/rand/v2": random,
		"crypto/rand":  random,
		"os/user":      ambient,
	}
}

// TestNoGeneratorReadsTheClock is the static half of #120: no built-in
// generator embeds a timestamp, a hostname, a username or a random value,
// because none of them can reach one.
//
// It is static on purpose. The dynamic half — running a generator twice and
// comparing the bytes, in each generator's own determinism test and in
// `dagger call regeneration` — catches output ordered by map iteration, because
// Go randomises that on every range. It cannot catch a clock read: two runs a
// millisecond apart produce the same date, so a generator stamping one passes
// every repetition test there is and turns up a year later in somebody else's
// diff. Reading the source is what closes that gap.
//
// Test files are excluded. A test may read the clock freely; what it must not
// do is put the result in a generator's output, and none does.
func TestNoGeneratorReadsTheClock(t *testing.T) {
	for _, dir := range generatorPackages() {
		sources := nonTestSources(t, dir)
		if len(sources) == 0 {
			t.Fatalf("no non-test Go source found in %s: the check is looking in the wrong place", dir)
		}

		for _, path := range sources {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("failed to parse %s: %v", path, err)
			}
			for _, finding := range nondeterminismIn(fset, file) {
				t.Errorf("%s (docs/plugin/SPEC.md, Determinism)", finding)
			}
		}
	}
}

// nonTestSources lists the Go source files in dir that are compiled into the
// generator, which is every .go file that is not a test.
func nonTestSources(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("failed to read %s: %v", dir, err)
	}

	var sources []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		sources = append(sources, filepath.Join(dir, name))
	}
	return sources
}

// nondeterminismIn reports every forbidden import and every reference to a
// forbidden function in one file, each as a finished sentence naming the
// position. It returns findings rather than failing a test so that the check
// can be tested against source that must fail it.
//
// It matches selectors, not call expressions, so a forbidden function is
// reported wherever it is named — passed, assigned or called. Local import
// names are resolved rather than assumed, so an aliased import cannot smuggle
// one past either.
func nondeterminismIn(fset *token.FileSet, file *ast.File) []string {
	funcs, imports := forbiddenFuncs(), forbiddenImports()
	var findings []string

	// Local name to import path, for the packages this check has an opinion
	// about. A dot import would bind names with no qualifier at all, putting
	// them out of the selector check's reach; it is reported rather than
	// resolved, because nothing here uses one.
	watched := make(map[string]string)
	for _, spec := range file.Imports {
		at := fset.Position(spec.Pos())

		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			findings = append(findings, fmt.Sprintf("%s: unparseable import path %s", at, spec.Path.Value))
			continue
		}
		if why, forbidden := imports[path]; forbidden {
			findings = append(findings, fmt.Sprintf("%s imports %q: %s", at, path, why))
			continue
		}
		if _, ok := funcs[path]; !ok {
			continue
		}
		local := path[strings.LastIndex(path, "/")+1:]
		if spec.Name != nil {
			if spec.Name.Name == "." {
				findings = append(findings, fmt.Sprintf(
					"%s dot-imports %q, putting its names out of reach of the determinism check", at, path))
				continue
			}
			local = spec.Name.Name
		}
		watched[local] = path
	}
	if len(watched) == 0 {
		return findings
	}

	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		path, ok := watched[ident.Name]
		if !ok {
			return true
		}
		why, forbidden := funcs[path][sel.Sel.Name]
		if !forbidden {
			return true
		}
		findings = append(findings, fmt.Sprintf("%s references %s.%s, which %s",
			fset.Position(sel.Pos()), path, sel.Sel.Name, why))
		return true
	})

	return findings
}

// TestDeterminismCheckSeesAForbiddenReference guards the check against its own
// worst failure. A scan that reports nothing because it resolves no import, or
// looks at the wrong identifier, passes silently and forever; feeding it source
// that must fail is the only way to know it can fail at all.
func TestDeterminismCheckSeesAForbiddenReference(t *testing.T) {
	testCases := []struct {
		name string
		src  string
	}{
		{
			name: "a plain call",
			src:  "package p\n\nimport \"time\"\n\nfunc f() any { return time.Now() }\n",
		},
		{
			name: "behind an alias",
			src:  "package p\n\nimport clock \"time\"\n\nfunc f() any { return clock.Now() }\n",
		},
		{
			name: "the environment",
			src:  "package p\n\nimport \"os\"\n\nfunc f() string { return os.Getenv(\"USER\") }\n",
		},
		{
			name: "a forbidden import",
			src:  "package p\n\nimport \"os/user\"\n\nfunc f() any { u, _ := user.Current(); return u }\n",
		},
		{
			// Named but not called: the function value is the same clock read
			// with one more step in front of it.
			name: "assigned rather than called",
			src:  "package p\n\nimport \"time\"\n\nfunc f() any { at := time.Now; return at }\n",
		},
		{
			name: "passed as an argument",
			src:  "package p\n\nimport \"time\"\n\nfunc g(any) {}\n\nfunc f() { g(time.Now) }\n",
		},
		{
			name: "a dot import of a watched package",
			src:  "package p\n\nimport . \"time\"\n\nfunc f() any { return Now() }\n",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if findings := findingsFor(t, tc.src); len(findings) == 0 {
				t.Errorf("the determinism check accepted source it must reject:\n%s", tc.src)
			}
		})
	}
}

// TestDeterminismCheckAcceptsDeterministicSource is the other half: a check
// that reported everything would be as useless as one that reported nothing,
// and would make every deterministic use of a watched package unwritable.
func TestDeterminismCheckAcceptsDeterministicSource(t *testing.T) {
	testCases := []struct {
		name string
		src  string
	}{
		{
			name: "a timestamp from SOURCE_DATE_EPOCH",
			src:  "package p\n\nimport \"time\"\n\nfunc f(sec int64) time.Time { return time.Unix(sec, 0).UTC() }\n",
		},
		{
			name: "reading the descriptor from disk",
			src:  "package p\n\nimport \"os\"\n\nfunc f(p string) ([]byte, error) { return os.ReadFile(p) }\n",
		},
		{
			name: "a method that happens to be called Now",
			src:  "package p\n\ntype c struct{}\n\nfunc (c) Now() int { return 0 }\n\nfunc f(x c) int { return x.Now() }\n",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if findings := findingsFor(t, tc.src); len(findings) > 0 {
				t.Errorf("the determinism check rejected deterministic source: %v\n%s", findings, tc.src)
			}
		})
	}
}

// findingsFor parses one snippet of source and runs the check over it.
func findingsFor(t *testing.T, src string) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "case.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("failed to parse the case: %v", err)
	}
	return nondeterminismIn(fset, file)
}
