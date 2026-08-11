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
	"slices"
	"strconv"
	"strings"
	"testing"
)

// What the rule protects, and why the ban has the shape it has.
//
// docs/plugin/SPEC.md's "Determinism" says one thing: no value that differs
// between two runs may reach a generated *byte*. That is a property of a data
// flow, and a data flow is not something a file-at-a-time source scan can see.
// So the check below approximates it, and the approximation used to be as
// coarse as it could be — naming time.Now at all, anywhere in a package the
// generators are built from, was the finding. That cost nothing for as long as
// no such package had a use for a clock, and for a long time none did.
//
// #196 gives one a use. A generator that opens a span reaches, through the
// OpenTelemetry SDK, both of the things the ban names: a span is two wall-clock
// timestamps, and an exporter finds its collector in the environment.
//
// The resolution is deliberately not a carve-out in [forbiddenFuncs]. Allowing
// time.Now there would allow it in every package at once, including the ones
// that write files, which is the whole of what the rule is for. What is done
// instead is to shrink the over-approximation by exactly the amount the new use
// requires: telemetry lives in one package that produces nothing through
// [FileWriter], that package is named — by identity, in [exemptPackages], not
// by a name pattern, a build tag or a comment directive — and every package
// that can produce output stays under the absolute ban.
//
// An exemption is only worth as much as the checks around it, so there are
// three, and none of them is satisfied by the exempt package merely passing:
//
//   - [TestEveryPackageThatCanProduceOutputIsStillUnderTheBan] writes the
//     banned set out as literals, so the ban cannot be narrowed by editing the
//     list the check reads.
//   - [TestTheExemptPackageCannotProduceGeneratedOutput] walks the exempt
//     package's module-local import graph and requires that it cannot reach
//     this package, which declares [FileWriter]. That is a necessary condition
//     and not a sufficient one — internal/ir cannot reach [FileWriter] either,
//     and its bytes are generated output — which is exactly why the list is
//     explicit and short rather than derived from the graph.
//   - [TestTheBanStillCatchesNondeterminismOnAGeneratorsOutputPath] puts a
//     clock read and an environment read into a copy of a generator's own
//     output-writing source and requires the check to report both. A check
//     whose failure path has never run is a check nobody knows the state of.

// fileWriterPackage is the import path of the package declaring [FileWriter],
// which is the whole of how a generator produces a byte. A package that cannot
// reach it cannot write generated output.
const fileWriterPackage = "github.com/z5labs/avroc/internal/plugin"

// modulePath is this repository's module path, and the prefix that tells an
// import of a package the ban has jurisdiction over from an import of one it
// does not.
const modulePath = "github.com/z5labs/avroc"

// moduleRoot is the module's root directory, relative to this package, which is
// where a module-local import path is resolved from.
func moduleRoot() string {
	return filepath.Join("..", "..")
}

// bannedPackages are the directories that can put a byte in a generated file:
// the three built-in generators, the shared IR operations they perform, and
// this package, through which all three run and which owns the writing. Every
// one of them is under the absolute ban.
//
// They are named as paths rather than imported because the check below is a
// property of the source, not of a running program.
func bannedPackages() []string {
	return []string{
		".",
		filepath.Join("..", "avroc-gen-go"),
		filepath.Join("..", "avroc-gen-json"),
		filepath.Join("..", "avroc-gen-pcf"),
		filepath.Join("..", "ir"),
	}
}

// exemptPackages are the directories linked into a generator that the absolute
// ban does not apply to, because they produce nothing through [FileWriter].
//
// There is one, internal/telemetry, and the shortness of the list is the
// point: this is the entire amount by which the over-approximation described
// at the top of this file has been shrunk. A package earns a place here by
// being unable to write output at all, which is asserted rather than assumed
// of it — see [TestTheExemptPackageCannotProduceGeneratedOutput] — and not by
// having a good reason to read a clock.
func exemptPackages() []string {
	return []string{filepath.Join("..", "telemetry")}
}

// forbiddenFuncs maps an import path to the functions in it whose result
// differs between two runs of the same executable over the same descriptor.
// docs/plugin/SPEC.md's "Determinism" forbids their results reaching a
// generator's output; forbidding them outright, in every package that can
// produce output, is the checkable form of that.
//
// Nothing is ever added here as an exception. The unit of exemption is a
// package, in [exemptPackages], because a function allowed here is allowed in
// the file-writing packages too.
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
// because no package that can write one can reach one.
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
	for _, dir := range bannedPackages() {
		findings, err := scanPackage(dir)
		if err != nil {
			t.Fatalf("failed to scan %s: %v", dir, err)
		}
		for _, finding := range findings {
			t.Errorf("%s (docs/plugin/SPEC.md, Determinism)", finding)
		}
	}
}

// TestEveryPackageThatCanProduceOutputIsStillUnderTheBan writes the banned set
// out as literals, so that narrowing the ban takes a change to this test and
// not only to the list the check reads. An exemption granted by quietly
// dropping a directory from [bannedPackages] would leave every other check here
// passing.
func TestEveryPackageThatCanProduceOutputIsStillUnderTheBan(t *testing.T) {
	want := []string{
		".",
		"../avroc-gen-go",
		"../avroc-gen-json",
		"../avroc-gen-pcf",
		"../ir",
	}

	got := bannedPackages()
	for _, dir := range want {
		if !slices.Contains(got, filepath.FromSlash(dir)) {
			t.Errorf("%s can produce generated output and is no longer under the determinism ban", dir)
		}
	}

	// Scanning a package that is not there passes vacuously, so every banned
	// directory has to hold source the check actually read.
	for _, dir := range got {
		sources, err := nonTestSources(dir)
		if err != nil {
			t.Fatalf("failed to read %s: %v", dir, err)
		}
		if len(sources) == 0 {
			t.Errorf("no non-test Go source found in %s: the ban is looking in the wrong place", dir)
		}
	}
}

// TestTheExemptPackageCannotProduceGeneratedOutput is what makes the exemption
// narrow rather than merely stated. A package is exempt only because nothing it
// contains can become a generated byte, and the whole of how a byte becomes one
// is [FileWriter]; so an exempt package that could reach [FileWriter] would be
// an exemption from the rule itself.
//
// Reachability is the necessary condition and not the sufficient one — see the
// note at the top of this file — so the exempt set stays explicit, and this
// test is what stops it being widened to a package that writes files.
func TestTheExemptPackageCannotProduceGeneratedOutput(t *testing.T) {
	// Asked of this package rather than assumed of it: if FileWriter moves, the
	// reachability question below would go on being asked about the wrong
	// package and go on being answered no.
	if !declaresFileWriter(t, ".") {
		t.Fatalf("FileWriter is no longer declared in %s: the exemption is asking about the wrong package", fileWriterPackage)
	}

	exempt := exemptPackages()
	if len(exempt) == 0 {
		t.Fatal("no exempt package: the check below would pass with the exemption widened to anything")
	}

	banned := bannedPackages()
	for _, dir := range exempt {
		if slices.Contains(banned, dir) {
			t.Errorf("%s is both banned and exempt", dir)
			continue
		}

		sources, err := nonTestSources(dir)
		if err != nil {
			t.Fatalf("failed to read the exempt package %s: %v", dir, err)
		}
		if len(sources) == 0 {
			t.Errorf("no non-test Go source found in the exempt package %s", dir)
			continue
		}

		chain, err := importPathTo(dir, fileWriterPackage)
		if err != nil {
			t.Fatalf("failed to walk the imports of %s: %v", dir, err)
		}
		if chain != nil {
			t.Errorf("%s is exempt from the determinism ban but reaches %s, so it can produce generated output: %s",
				dir, fileWriterPackage, strings.Join(chain, " -> "))
		}
	}
}

// TestTheBanStillCatchesNondeterminismOnAGeneratorsOutputPath is the negative
// case the exemption is worthless without. It takes a generator's own source —
// a copy of it, so nothing in the repository is edited — puts a clock read and
// then an environment read into the function that writes the files, and
// requires the check to report each one, naming it.
//
// The source is copied rather than written out here so that the case cannot
// drift into a hand-made snippet that merely resembles a generator. If the
// exemption were ever widened to cover a package that writes output, this is
// what would notice.
func TestTheBanStillCatchesNondeterminismOnAGeneratorsOutputPath(t *testing.T) {
	const (
		generator = "avroc-gen-go"
		file      = "generate.go"
		fn        = "Generate"
	)

	testCases := []struct {
		name       string
		importPath string
		stmt       string
		want       string
	}{
		{
			name:       "a clock read",
			importPath: "time",
			stmt:       "_ = time.Now()",
			want:       "time.Now",
		},
		{
			name:       "an environment read",
			importPath: "os",
			stmt:       `_ = os.Getenv("SOURCE_DATE_EPOCH")`,
			want:       "os.Getenv",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := injectedCopy(t, filepath.Join("..", generator), file, fn, tc.importPath, tc.stmt)

			findings, err := scanPackage(dir)
			if err != nil {
				t.Fatalf("failed to scan the patched copy of %s: %v", generator, err)
			}

			named := slices.ContainsFunc(findings, func(finding string) bool {
				return strings.Contains(finding, tc.want) && strings.Contains(finding, file)
			})
			if !named {
				t.Errorf("the determinism check accepted %s in %s.%s, which writes generated files; findings: %v",
					tc.stmt, file, fn, findings)
			}
		})
	}
}

// scanPackage reports every finding in the non-test sources of one directory.
// It is the check itself, separated from the test that runs it over the
// repository so that it can be run over source that must fail it.
func scanPackage(dir string) ([]string, error) {
	sources, err := nonTestSources(dir)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("no non-test Go source found in %s: the check is looking in the wrong place", dir)
	}

	var findings []string
	for _, path := range sources {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", path, err)
		}
		findings = append(findings, nondeterminismIn(fset, file)...)
	}
	return findings, nil
}

// nonTestSources lists the Go source files in dir that are compiled into the
// generator, which is every .go file that is not a test.
func nonTestSources(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var sources []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		sources = append(sources, filepath.Join(dir, name))
	}
	return sources, nil
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

// declaresFileWriter reports whether dir declares the FileWriter type.
func declaresFileWriter(t *testing.T, dir string) bool {
	t.Helper()

	sources, err := nonTestSources(dir)
	if err != nil {
		t.Fatalf("failed to read %s: %v", dir, err)
	}

	for _, path := range sources {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", path, err)
		}

		found := false
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if ok && spec.Name.Name == "FileWriter" {
				found = true
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

// importPathTo walks the module-local import graph from dir, breadth first, and
// returns the chain of import paths by which it reaches target, or nil when it
// does not. Imports of other modules are not followed: nothing outside this
// repository can reach [FileWriter], which is unexported to it.
func importPathTo(dir, target string) ([]string, error) {
	type node struct {
		dir   string
		chain []string
	}

	seen := make(map[string]bool)
	queue := []node{{dir: dir}}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		imports, err := importsOf(current.dir)
		if err != nil {
			return nil, err
		}
		for _, path := range imports {
			if path != modulePath && !strings.HasPrefix(path, modulePath+"/") {
				continue
			}
			if seen[path] {
				continue
			}
			seen[path] = true

			chain := append(append([]string(nil), current.chain...), path)
			if path == target {
				return chain, nil
			}
			rel := filepath.FromSlash(strings.TrimPrefix(path, modulePath+"/"))
			queue = append(queue, node{dir: filepath.Join(moduleRoot(), rel), chain: chain})
		}
	}
	return nil, nil
}

// importsOf lists the import paths named by dir's non-test sources.
func importsOf(dir string) ([]string, error) {
	sources, err := nonTestSources(dir)
	if err != nil {
		return nil, err
	}

	var paths []string
	for _, source := range sources {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, source, nil, parser.ImportsOnly|parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", source, err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return nil, fmt.Errorf("%s: unparseable import path %s", source, spec.Path.Value)
			}
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// injectedCopy copies dir's non-test sources into a temporary directory and
// rewrites the copy of file so that the body of fn begins with stmt, adding an
// import of importPath if the file does not already have one. It returns the
// temporary directory, for the check to be run over.
//
// fn is required to be on the output-writing path — its body must call
// WriteFile — so that a restructuring which moves the write elsewhere fails
// here, loudly, instead of leaving the negative case pointed at a function that
// no longer produces a byte.
func injectedCopy(t *testing.T, dir, file, fn, importPath, stmt string) string {
	t.Helper()

	sources, err := nonTestSources(dir)
	if err != nil {
		t.Fatalf("failed to read %s: %v", dir, err)
	}
	if len(sources) == 0 {
		t.Fatalf("no non-test Go source found in %s", dir)
	}

	tmp := t.TempDir()
	patched := false
	for _, source := range sources {
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("failed to read %s: %v", source, err)
		}
		if filepath.Base(source) == file {
			content = inject(t, source, content, fn, importPath, stmt)
			patched = true
		}
		if err := os.WriteFile(filepath.Join(tmp, filepath.Base(source)), content, 0o600); err != nil {
			t.Fatalf("failed to write the copy of %s: %v", source, err)
		}
	}
	if !patched {
		t.Fatalf("%s has no %s to patch", dir, file)
	}
	return tmp
}

// inject rewrites one file's source so that the body of fn begins with stmt.
func inject(t *testing.T, name string, src []byte, fn, importPath, stmt string) []byte {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("failed to parse %s: %v", name, err)
	}

	var decl *ast.FuncDecl
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if ok && fd.Name.Name == fn && fd.Body != nil {
			decl = fd
			break
		}
	}
	if decl == nil {
		t.Fatalf("%s declares no func %s to inject into", name, fn)
	}
	if !callsWriteFile(decl) {
		t.Fatalf("%s.%s is not on the output-writing path: nothing in its body calls WriteFile", name, fn)
	}

	needsImport := true
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err == nil && path == importPath {
			needsImport = false
			break
		}
	}

	// The body first, because inserting the import ahead of it would move the
	// offset the statement goes at.
	at := fset.Position(decl.Body.Lbrace).Offset + 1
	out := slices.Concat(src[:at], []byte("\n\t"+stmt+"\n"), src[at:])
	if needsImport {
		at = fset.Position(file.Name.End()).Offset
		out = slices.Concat(out[:at], []byte("\n\nimport "+strconv.Quote(importPath)), out[at:])
	}
	return out
}

// callsWriteFile reports whether anything in fn's body calls a WriteFile.
func callsWriteFile(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return !found
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "WriteFile" {
			found = true
		}
		return !found
	})
	return found
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
