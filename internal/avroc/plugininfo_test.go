// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package avroc

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/cli"
	"github.com/z5labs/avroc/internal/ir"
	"github.com/z5labs/avroc/internal/plugin"

	"google.golang.org/protobuf/proto"
)

func TestParsePluginInfo(t *testing.T) {
	t.Run("accepts the declaration docs/plugin/SPEC.md specifies", func(t *testing.T) {
		info, err := parsePluginInfo([]byte(`{
  "name": "go",
  "version": "0.2.0",
  "ir_version": 1,
  "options": ["package_name", "encoding"]
}`))
		if err != nil {
			t.Fatal(err)
		}

		if *info.Name != "go" {
			t.Errorf("name = %q, want go", *info.Name)
		}
		if *info.Version != "0.2.0" {
			t.Errorf("version = %q, want 0.2.0", *info.Version)
		}
		if *info.IRVersion != 1 {
			t.Errorf("ir_version = %d, want 1", *info.IRVersion)
		}
		if info.Options == nil || len(*info.Options) != 2 {
			t.Errorf("options = %v, want two entries", info.Options)
		}
	})

	t.Run("ignores a member it does not recognise", func(t *testing.T) {
		// A plugin declaring more than this version of avroc reads is not
		// thereby broken — the whole point of a handshake is that the two ends
		// may be different ages.
		if _, err := parsePluginInfo([]byte(`{"name":"go","version":"1","ir_version":1,"features":["x"]}`)); err != nil {
			t.Errorf("an unknown member was rejected: %v", err)
		}
	})

	t.Run("tells an absent options member from an empty one", func(t *testing.T) {
		absent, err := parsePluginInfo([]byte(`{"name":"go","version":"1","ir_version":1}`))
		if err != nil {
			t.Fatal(err)
		}
		if absent.Options != nil {
			t.Errorf("options = %v, want absent", *absent.Options)
		}

		empty, err := parsePluginInfo([]byte(`{"name":"go","version":"1","ir_version":1,"options":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		if empty.Options == nil {
			t.Fatal("an empty options array was read as an absent member; those are opposite instructions")
		}
		if len(*empty.Options) != 0 {
			t.Errorf("options = %v, want empty", *empty.Options)
		}
	})

	t.Run("rejects what is not a usable declaration", func(t *testing.T) {
		testCases := []struct {
			name   string
			stdout string
			names  string // a fragment the failure has to name
		}{
			{
				name:   "a plugin that wrote nothing",
				stdout: "",
				names:  "not a JSON object",
			},
			{
				name:   "a plugin that printed a usage line instead",
				stdout: "Usage: avroc-gen-go --descriptor <path> --out <dir>\n",
				names:  "not a JSON object",
			},
			{
				name:   "a JSON array",
				stdout: `["go"]`,
				names:  "not a JSON object",
			},
			{
				name:   "JSON null, which parses but declares nothing",
				stdout: `null`,
				names:  "name, version, ir_version",
			},
			{
				name:   "a declaration missing ir_version",
				stdout: `{"name":"go","version":"1"}`,
				names:  "ir_version",
			},
			{
				name:   "a declaration missing every required member",
				stdout: `{}`,
				names:  "name, version, ir_version",
			},
			{
				name:   "an empty name",
				stdout: `{"name":"","version":"1","ir_version":1}`,
				names:  "empty",
			},
			{
				name:   "a declaration with something written after it",
				stdout: `{"name":"go","version":"1","ir_version":1}` + "\ngenerating...\n",
				names:  "trailing data",
			},
			{
				// The dangerous one, and the reason options is decoded from raw
				// bytes rather than straight into a pointer: read as an absent
				// member, a null vocabulary lets every option in the manifest past
				// a check the plugin looked like it had asked for. A Go plugin with
				// a nil slice and no encoder set up to omit it emits exactly this.
				name:   "an options member written as null",
				stdout: `{"name":"go","version":"1","ir_version":1,"options":null}`,
				names:  "null",
			},
			{
				name:   "an options member that is not an array",
				stdout: `{"name":"go","version":"1","ir_version":1,"options":"package_name"}`,
				names:  "not an array of strings",
			},
			{
				name:   "an options array of something other than strings",
				stdout: `{"name":"go","version":"1","ir_version":1,"options":[1,2]}`,
				names:  "not an array of strings",
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := parsePluginInfo([]byte(tc.stdout))
				if err == nil {
					t.Fatalf("parsePluginInfo accepted %q", tc.stdout)
				}
				if !strings.Contains(err.Error(), tc.names) {
					t.Errorf("failure %q does not name %q", err.Error(), tc.names)
				}
			})
		}
	})

	t.Run("an ir_version of zero is a declaration, not an absence", func(t *testing.T) {
		// No conforming plugin declares 0, but the two have to arrive here
		// distinguishably: one is a plugin that is too old and the other is a
		// plugin that never answered the question.
		info, err := parsePluginInfo([]byte(`{"name":"go","version":"1","ir_version":0}`))
		if err != nil {
			t.Fatal(err)
		}
		if *info.IRVersion != 0 {
			t.Errorf("ir_version = %d, want 0", *info.IRVersion)
		}
	})
}

// declaration builds a parsed declaration for the checks below, so that a test
// about checkPluginInfo is not also a test about JSON.
func declaration(name string, irVersion int32, options *[]string) *pluginInfo {
	return &pluginInfo{
		Name:      proto.String(name),
		Version:   proto.String("1.2.3"),
		IRVersion: proto.Int32(irVersion),
		Options:   options,
	}
}

func TestCheckPluginInfo(t *testing.T) {
	t.Run("accepts a generator that matches", func(t *testing.T) {
		err := checkPluginInfo("avroc-gen-go", nil, declaration("go", ir.Version, nil))
		if err != nil {
			t.Error(err)
		}
	})

	t.Run("rejects a declaration naming a different generator", func(t *testing.T) {
		// The file on PATH is not the generator the manifest asked for, and
		// generating with it would attribute its output to the wrong plugin.
		err := checkPluginInfo("avroc-gen-go", nil, declaration("rust", ir.Version, nil))
		if err == nil {
			t.Fatal("a mismatched name was accepted")
		}
		for _, want := range []string{"avroc-gen-go", "rust"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("failure %q does not name %q", err.Error(), want)
			}
		}
	})

	t.Run("rejects a plugin older than the descriptor avroc writes", func(t *testing.T) {
		err := checkPluginInfo("avroc-gen-go", nil, declaration("go", ir.Version-1, nil))
		if err == nil {
			t.Fatal("a plugin too old for this avroc's IR was accepted")
		}
		// docs/plugin/SPEC.md requires the diagnostic to name both numbers and
		// the generator, because the user's next move depends on which end is
		// behind.
		for _, want := range []string{"avroc-gen-go", fmt.Sprint(ir.Version - 1), fmt.Sprint(ir.Version)} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("failure %q does not name %q", err.Error(), want)
			}
		}
	})

	t.Run("accepts a plugin newer than the descriptor avroc writes", func(t *testing.T) {
		// A monotonic version is what makes a newer plugin able to read an older
		// descriptor; only the other direction is a failure.
		err := checkPluginInfo("avroc-gen-go", nil, declaration("go", ir.Version+1, nil))
		if err != nil {
			t.Error(err)
		}
	})

	t.Run("rejects a manifest option the plugin declared no support for", func(t *testing.T) {
		opts := []*avrocpb.Option{{Name: proto.String("packag_name"), Value: proto.String("gen")}}
		vocabulary := []string{"package_name"}

		err := checkPluginInfo("avroc-gen-go", opts, declaration("go", ir.Version, &vocabulary))
		if err == nil {
			t.Fatal("an undeclared option was accepted")
		}
		if !strings.Contains(err.Error(), "packag_name") {
			t.Errorf("failure %q does not name the option", err.Error())
		}
	})

	t.Run("a plugin declaring an empty vocabulary accepts no option at all", func(t *testing.T) {
		opts := []*avrocpb.Option{{Name: proto.String("anything"), Value: proto.String("x")}}

		if err := checkPluginInfo("avroc-gen-pcf", opts, declaration("pcf", ir.Version, &[]string{})); err == nil {
			t.Fatal("a generator that accepts no options was handed one")
		}
	})

	t.Run("a plugin declaring no vocabulary is handed the options and decides itself", func(t *testing.T) {
		opts := []*avrocpb.Option{{Name: proto.String("anything"), Value: proto.String("x")}}

		if err := checkPluginInfo("avroc-gen-go", opts, declaration("go", ir.Version, nil)); err != nil {
			t.Errorf("an absent options member was read as an empty one: %v", err)
		}
	})

	t.Run("accepts every option the plugin declared", func(t *testing.T) {
		opts := []*avrocpb.Option{
			{Name: proto.String("encoding"), Value: proto.String("single_object")},
			{Name: proto.String("package_name"), Value: proto.String("gen")},
		}
		vocabulary := []string{"encoding", "package_name"}

		if err := checkPluginInfo("avroc-gen-go", opts, declaration("go", ir.Version, &vocabulary)); err != nil {
			t.Error(err)
		}
	})
}

// writeNamedShellGenerator writes body out as an executable shell script called
// avroc-gen-<name> in its own directory, and returns the path.
//
// A shell script on purpose, as everywhere else in this package: the handshake
// costs a conforming plugin one printf, and a test driving one is what holds
// avroc to that.
func writeNamedShellGenerator(t *testing.T, name, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "avroc-gen-"+name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// conformingHandshake is the body of the smallest plugin that can exist: one
// printf, guarded on the flag.
func conformingHandshake(name string, irVersion int32) string {
	return fmt.Sprintf(`if [ "$1" = "--plugin-info" ]; then
  printf '{"name":"%s","version":"9.9.9","ir_version":%d,"options":[]}\n'
  exit 0
fi
echo "error: unexpected vector" >&2
exit 1
`, name, irVersion)
}

func TestQueryPluginInfo(t *testing.T) {
	t.Run("reads what a conforming plugin declares", func(t *testing.T) {
		path := writeNamedShellGenerator(t, "test", conformingHandshake("test", ir.Version))

		info, err := queryPluginInfo(t.Context(), "avroc-gen-test", path)
		if err != nil {
			t.Fatal(err)
		}
		if *info.Name != "test" {
			t.Errorf("name = %q, want test", *info.Name)
		}
		if *info.Version != "9.9.9" {
			t.Errorf("version = %q, want 9.9.9", *info.Version)
		}
	})

	t.Run("passes the flag and nothing else", func(t *testing.T) {
		// docs/plugin/SPEC.md gives --plugin-info a vector of its own: no
		// descriptor is read and no other argument is accepted, so a plugin that
		// checks is entitled to fail one that carried more.
		recorded := filepath.Join(t.TempDir(), "args")
		path := writeNamedShellGenerator(t, "test", fmt.Sprintf(`: > '%s'
for a in "$@"; do printf '%%s\n' "$a" >> '%s'; done
printf '{"name":"test","version":"1","ir_version":%d}\n'
`, recorded, recorded, ir.Version))

		if _, err := queryPluginInfo(t.Context(), "avroc-gen-test", path); err != nil {
			t.Fatal(err)
		}

		args := readLines(t, recorded)
		if len(args) != 1 || args[0] != "--plugin-info" {
			t.Errorf("argument vector = %q, want just --plugin-info", args)
		}
	})

	t.Run("a plugin that does not implement the handshake fails the run rather than crashing it", func(t *testing.T) {
		// The case the acceptance criteria name: an older plugin that has never
		// heard of the flag. It exits non-zero with a usage line, and what a user
		// needs back is the generator's name and that line — not a stack trace
		// attributed to nobody.
		path := writeNamedShellGenerator(t, "old", `echo "avroc-gen-old: unknown option $1" >&2
exit 2
`)

		_, err := queryPluginInfo(t.Context(), "avroc-gen-old", path)
		if err == nil {
			t.Fatal("a plugin that rejected the flag was accepted")
		}
		for _, want := range []string{"avroc-gen-old", "unknown option"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("failure %q does not carry %q", err.Error(), want)
			}
		}
	})

	t.Run("a plugin that exits zero with nothing to say", func(t *testing.T) {
		path := writeNamedShellGenerator(t, "silent", "exit 0\n")

		_, err := queryPluginInfo(t.Context(), "avroc-gen-silent", path)
		if err == nil {
			t.Fatal("a silent plugin was accepted")
		}
		if !strings.Contains(err.Error(), "wrote nothing") {
			t.Errorf("failure %q does not say the plugin wrote nothing", err.Error())
		}
	})

	t.Run("quotes what it received, bounded", func(t *testing.T) {
		// A plugin that ignored the flag and generated anyway can print a great
		// deal, and a diagnostic is worth nothing once it has scrolled the real
		// one out of view.
		path := writeNamedShellGenerator(t, "loud", `awk 'BEGIN { while (i++ < 200) printf "not-a-declaration " }'
exit 0
`)

		_, err := queryPluginInfo(t.Context(), "avroc-gen-loud", path)
		if err == nil {
			t.Fatal("a plugin writing prose to standard output was accepted")
		}
		if !strings.Contains(err.Error(), "...") {
			t.Errorf("failure %q was not truncated", err.Error())
		}
		if len(err.Error()) > 4*maxQuotedBytes {
			t.Errorf("failure is %d bytes long", len(err.Error()))
		}
	})

	t.Run("an executable that is not there", func(t *testing.T) {
		_, err := queryPluginInfo(t.Context(), "avroc-gen-absent", filepath.Join(t.TempDir(), "avroc-gen-absent"))
		if err == nil {
			t.Fatal("a generator that could not be run was accepted")
		}
		if !strings.Contains(err.Error(), "avroc-gen-absent") {
			t.Errorf("failure %q does not name the generator", err.Error())
		}
	})
}

func TestCheckGenerators(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("accepts a run whose generators all conform", func(t *testing.T) {
		tasks := []genTask{
			{name: "avroc-gen-a", executablePath: writeNamedShellGenerator(t, "a", conformingHandshake("a", ir.Version))},
			{name: "avroc-gen-b", executablePath: writeNamedShellGenerator(t, "b", conformingHandshake("b", ir.Version))},
		}

		if err := checkGenerators(t.Context(), discard, tasks); err != nil {
			t.Error(err)
		}
	})

	t.Run("reports the first failing generator in task order", func(t *testing.T) {
		// Sequential and ordered so that a manifest with two broken generators
		// reports the same one every time, rather than whichever process lost the
		// race.
		tasks := []genTask{
			{name: "avroc-gen-a", executablePath: writeNamedShellGenerator(t, "a", conformingHandshake("a", ir.Version-1))},
			{name: "avroc-gen-b", executablePath: writeNamedShellGenerator(t, "b", conformingHandshake("b", ir.Version-1))},
		}

		for range 5 {
			err := checkGenerators(t.Context(), discard, tasks)
			if err == nil {
				t.Fatal("two generators too old for this avroc's IR were accepted")
			}
			if !strings.Contains(err.Error(), "avroc-gen-a") {
				t.Fatalf("failure %q is not about the first generator", err.Error())
			}
		}
	})

	t.Run("runs one generator at a time", func(t *testing.T) {
		// Sequential on purpose, and stated here rather than left to be inferred
		// from the test above: #184 bounded the generation pool, and a story about
		// concurrency is exactly where someone would think to parallelise the
		// handshake too. It is a few short-lived execs, and what it buys is a
		// manifest with two broken generators failing on the same one every time
		// rather than on whichever process lost the race.
		trace := filepath.Join(t.TempDir(), "trace")
		tasks := make([]genTask, 0, 3)
		for _, name := range []string{"a", "b", "c"} {
			body := fmt.Sprintf(`printf '%%s start\n' '%s' >> '%s'
sleep 0.2
printf '%%s end\n' '%s' >> '%s'
`, name, trace, name, trace) + conformingHandshake(name, ir.Version)
			tasks = append(tasks, genTask{
				name:           "avroc-gen-" + name,
				executablePath: writeNamedShellGenerator(t, name, body),
			})
		}

		if err := checkGenerators(t.Context(), discard, tasks); err != nil {
			t.Fatal(err)
		}

		if peak := peakConcurrency(t, trace); peak != 1 {
			t.Errorf("%d generators answered the handshake at once, want one at a time", peak)
		}
		want := []string{"a", "b", "c"}
		if got := generatorsThatStarted(t, trace); !slices.Equal(got, want) {
			t.Errorf("the handshake ran the generators in the order %q, want the manifest's %q", got, want)
		}
	})

	t.Run("rejects an option no generator declared", func(t *testing.T) {
		tasks := []genTask{{
			name:           "avroc-gen-a",
			executablePath: writeNamedShellGenerator(t, "a", conformingHandshake("a", ir.Version)),
			options:        []*avrocpb.Option{{Name: proto.String("package_name"), Value: proto.String("gen")}},
		}}

		if err := checkGenerators(t.Context(), discard, tasks); err == nil {
			t.Fatal("an option a generator declared no support for was accepted")
		}
	})
}

// TestTheDeclarationThisRepositorysGeneratorsWriteIsOneAvrocAccepts is the
// agreement test across the process boundary.
//
// internal/plugin is the generator's half of docs/plugin/SPEC.md and this
// package is avroc's; they share no code and no constant on purpose, so that a
// third-party generator implements the contract without importing anything from
// this repository. That leaves nothing but a test to notice if the two halves
// drift apart, and this is it: the exact bytes a generator built here writes,
// parsed and checked by the exact code that reads a stranger's.
func TestTheDeclarationThisRepositorysGeneratorsWriteIsOneAvrocAccepts(t *testing.T) {
	for _, generator := range []struct {
		name    string
		options []string
	}{
		{name: "go", options: []string{"encoding", "package_name"}},
		{name: "json"},
		{name: "pcf"},
	} {
		t.Run(generator.name, func(t *testing.T) {
			var declared bytes.Buffer
			if err := plugin.WriteInfo(&declared, plugin.NewInfo(generator.name, generator.options...)); err != nil {
				t.Fatal(err)
			}

			info, err := parsePluginInfo(declared.Bytes())
			if err != nil {
				t.Fatalf("avroc cannot read what a generator here writes: %v\n%s", err, declared.String())
			}

			options := make([]*avrocpb.Option, 0, len(generator.options))
			for _, key := range generator.options {
				options = append(options, &avrocpb.Option{Name: proto.String(key), Value: proto.String("v")})
			}

			if err := checkPluginInfo("avroc-gen"+"-"+generator.name, options, info); err != nil {
				t.Errorf("avroc rejects a generator built from this repository: %v", err)
			}
		})
	}
}

// TestRunGenerateStopsAtTheHandshake is the whole cycle: a manifest, a generator
// on PATH that is too old for this avroc's IR, and no output.
//
// Failing early is the entire justification for the handshake, and the only
// evidence that it happened early is that nothing was generated.
func TestRunGenerateStopsAtTheHandshake(t *testing.T) {
	workingDir := t.TempDir()
	writeIDL(t, workingDir, "schema.avdl")

	manifest := `{"inputs":["schema.avdl"],"generators":[{"name":"old","out":"gen"}]}`
	if err := os.WriteFile(filepath.Join(workingDir, manifestFilename), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	// A generator that answers the handshake honestly and would happily generate
	// if it were ever asked to. It is not asked to.
	binDir := t.TempDir()
	body := conformingHandshake("old", ir.Version-1) + fmt.Sprintf("mkdir -p '%s'\ntouch '%s'\n",
		filepath.Join(workingDir, "gen"), filepath.Join(workingDir, "gen", "generated.txt"))
	if err := os.WriteFile(filepath.Join(binDir, "avroc-gen-old"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	c := cli.Context{
		Log: slog.New(slog.NewTextHandler(&logs, nil)),
		Env: cli.EnvironmentFunc(func(key string) (string, bool) {
			if key == "PATH" {
				return binDir, true
			}
			return "", false
		}),
		OpenDir:    func(dir string) fs.FS { return os.DirFS(dir) },
		WorkingDir: workingDir,
	}

	if code := runGenerate(t.Context(), c, noopTracer()); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(logs.String(), "IR version") {
		t.Errorf("the failure does not name the IR version: %s", logs.String())
	}
	if _, err := os.Stat(filepath.Join(workingDir, "gen", "generated.txt")); err == nil {
		t.Error("the generator ran after failing the handshake; the point of the handshake is that it does not")
	}
}
