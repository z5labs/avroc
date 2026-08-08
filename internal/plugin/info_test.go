// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package plugin

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/z5labs/avroc/avrocpb"
	"github.com/z5labs/avroc/internal/ir"

	"google.golang.org/protobuf/proto"
)

// testInfo is the declaration the tests in this package drive Main with. Its
// name is the suffix, not the filename: "test", which resolves to
// avroc-gen-test.
func testInfo(options ...string) Info {
	return NewInfo("test", options...)
}

func TestNewInfo(t *testing.T) {
	t.Run("declares the name it was given and the executable that carries it", func(t *testing.T) {
		info := NewInfo("go")

		if info.Name != "go" {
			t.Errorf("Name = %q, want go", info.Name)
		}
		if info.Executable() != "avroc-gen-go" {
			t.Errorf("Executable() = %q, want avroc-gen-go", info.Executable())
		}
	})

	t.Run("declares the IR version this build understands", func(t *testing.T) {
		if got := NewInfo("go").IRVersion; got != ir.Version {
			t.Errorf("IRVersion = %d, want %d", got, ir.Version)
		}
	})

	t.Run("declares a version for a bug report to quote", func(t *testing.T) {
		// Nothing asserts *which* version: avroc never interprets it, and the
		// value depends on how the executable was built. What the contract asks
		// of it is that it is there and does not change, both of which a caller
		// would notice the absence of and no test upstream of avroc would.
		if NewInfo("go").Version == "" {
			t.Error("Version is empty; it exists to be quoted in a bug report")
		}
	})

	t.Run("a generator with no options declares an empty vocabulary, not an absent one", func(t *testing.T) {
		// The distinction survives to avroc's reader: present-and-empty is "I
		// accept none", absent is "pass them through and I will decide". A
		// generator built here always knows which of those it means.
		info := NewInfo("pcf")
		if info.Options == nil {
			t.Fatal("Options is nil, which marshals to JSON null — neither thing the member can say")
		}
		if len(info.Options) != 0 {
			t.Errorf("Options = %v, want empty", info.Options)
		}
	})
}

func TestWriteInfo(t *testing.T) {
	t.Run("writes the members docs/plugin/SPEC.md requires", func(t *testing.T) {
		var buf bytes.Buffer
		if err := WriteInfo(&buf, NewInfo("go", "encoding", "package_name")); err != nil {
			t.Fatal(err)
		}

		var got struct {
			Name      *string   `json:"name"`
			Version   *string   `json:"version"`
			IRVersion *int32    `json:"ir_version"`
			Options   *[]string `json:"options"`
		}
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("declaration is not a JSON object: %v\n%s", err, buf.String())
		}

		if got.Name == nil || *got.Name != "go" {
			t.Errorf("name = %v, want go", got.Name)
		}
		if got.Version == nil || *got.Version == "" {
			t.Errorf("version = %v, want a non-empty string", got.Version)
		}
		if got.IRVersion == nil || *got.IRVersion != ir.Version {
			t.Errorf("ir_version = %v, want %d", got.IRVersion, ir.Version)
		}
		if got.Options == nil {
			t.Fatal("options is absent; this generator has a vocabulary and declares it")
		}
		if want := []string{"encoding", "package_name"}; strings.Join(*got.Options, ",") != strings.Join(want, ",") {
			t.Errorf("options = %v, want %v", *got.Options, want)
		}
	})

	t.Run("an empty vocabulary is written as an empty array, never as null", func(t *testing.T) {
		var buf bytes.Buffer
		if err := WriteInfo(&buf, NewInfo("pcf")); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), `"options": []`) {
			t.Errorf("declaration does not carry an empty options array:\n%s", buf.String())
		}
	})

	t.Run("a hand-built Info with no options is written the same way", func(t *testing.T) {
		// WriteInfo normalizes rather than trusting its argument, because Info is
		// an ordinary struct a caller may build without NewInfo, and a nil slice
		// would otherwise reach avroc as JSON null.
		var buf bytes.Buffer
		if err := WriteInfo(&buf, Info{Name: "x", Version: "1", IRVersion: ir.Version}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), `"options": []`) {
			t.Errorf("nil Options was not normalized:\n%s", buf.String())
		}
	})

	t.Run("the same executable writes the same bytes every time", func(t *testing.T) {
		// docs/plugin/SPEC.md requires the declaration to be identical across
		// invocations, for Determinism's reasons. Ranging over a map is how that
		// gets broken, and it breaks intermittently.
		var first, second bytes.Buffer
		for _, buf := range []*bytes.Buffer{&first, &second} {
			if err := WriteInfo(buf, NewInfo("go", "encoding", "package_name")); err != nil {
				t.Fatal(err)
			}
		}
		if first.String() != second.String() {
			t.Errorf("two declarations differ:\n%s\n%s", first.String(), second.String())
		}
	})
}

func TestMain_PluginInfo(t *testing.T) {
	t.Run("writes the declaration to standard output and exits zero", func(t *testing.T) {
		c, _ := newTestCLI(PluginInfoFlag)

		var stdout bytes.Buffer
		generated := false
		record := func(*avrocpb.GenerateRequest, FileWriter) error {
			generated = true
			return nil
		}

		if code := run(t.Context(), c, testInfo("k"), record, &stdout); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if generated {
			t.Error("the handshake ran the generator; it reads no descriptor and writes no file")
		}

		var got Info
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("standard output is not the declaration: %v\n%s", err, stdout.String())
		}
		if got.Name != "test" {
			t.Errorf("name = %q, want test", got.Name)
		}
	})

	t.Run("rejects any other argument alongside it", func(t *testing.T) {
		// The flag is the whole of its vector. Accepting a generation vector
		// beside it would mean a plugin could be asked to do both at once, and
		// the contract gives standard output to the declaration alone.
		for _, args := range [][]string{
			{PluginInfoFlag, "--out", "/tmp"},
			{"--descriptor", "d", "--out", "o", PluginInfoFlag},
		} {
			var stdout bytes.Buffer
			c, _ := newTestCLI(args...)

			if code := run(t.Context(), c, testInfo(), echoGenerate, &stdout); code == 0 {
				t.Errorf("exit code = 0 for %v", args)
			}
			if stdout.Len() != 0 {
				t.Errorf("a rejected handshake still wrote to standard output: %s", stdout.String())
			}
		}
	})

	t.Run("a generation invocation writes nothing to standard output", func(t *testing.T) {
		// Standard output carries exactly one thing in this contract, which is
		// what makes the declaration parseable without a mode flag.
		b, err := proto.Marshal(testDescriptor())
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "descriptor.binpb")
		if err := os.WriteFile(path, b, 0o444); err != nil {
			t.Fatal(err)
		}

		var stdout bytes.Buffer
		c, _ := newTestCLI("--descriptor", path, "--out", t.TempDir())

		if code := run(t.Context(), c, testInfo(), echoGenerate, &stdout); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if stdout.Len() != 0 {
			t.Errorf("a generation wrote to standard output: %s", stdout.String())
		}
	})
}
