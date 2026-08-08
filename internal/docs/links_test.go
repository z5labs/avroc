// Copyright (c) 2026 Z5Labs and Contributors
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package docs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEveryRelativeLinkInTheDocumentationResolves walks every Markdown file in
// the repository and requires each relative link in it to name something that
// exists, resolved from the directory of the file the link is written in — and,
// where the link carries a fragment, a heading in the target that GitHub would
// give that anchor.
//
// External links are not checked: this test makes no network calls, so it is
// the same test offline, in CI and on a laptop with no credentials.
func TestEveryRelativeLinkInTheDocumentationResolves(t *testing.T) {
	root := repositoryRoot(t)

	docs := markdownFiles(t, root)
	if len(docs) == 0 {
		t.Fatal("found no Markdown files to check; the walk is wrong, not the documentation")
	}

	for _, doc := range docs {
		rel, err := filepath.Rel(root, doc)
		if err != nil {
			t.Fatalf("relativising %s: %v", doc, err)
		}

		t.Run(filepath.ToSlash(rel), func(t *testing.T) {
			content, err := os.ReadFile(doc)
			if err != nil {
				t.Fatalf("reading %s: %v", rel, err)
			}

			for _, l := range linksIn(string(content)) {
				if err := checkLink(doc, l.target); err != nil {
					t.Errorf("%s:%d: %v", filepath.ToSlash(rel), l.line, err)
				}
			}
		})
	}
}

func TestLinkExtractionAndChecking(t *testing.T) {
	t.Run("links inside a fenced block are examples, not links", func(t *testing.T) {
		md := "before\n\n```markdown\n[a](../nowhere.md)\n```\n\n[b](../elsewhere.md)\n"

		got := linksIn(md)

		if len(got) != 1 || got[0].target != "../elsewhere.md" {
			t.Errorf("expected only the link outside the fence, got %v", got)
		}
	})

	t.Run("a tilde fence closes with tildes and not with backticks", func(t *testing.T) {
		md := "~~~\n[a](nowhere.md)\n```\n[b](also-nowhere.md)\n~~~\n[c](real.md)\n"

		got := linksIn(md)

		if len(got) != 1 || got[0].target != "real.md" {
			t.Errorf("expected only the link after the tilde fence closed, got %v", got)
		}
	})

	t.Run("a reference definition is a link too", func(t *testing.T) {
		md := "[label]: ./sibling.md\n"

		got := linksIn(md)

		if len(got) != 1 || got[0].target != "./sibling.md" {
			t.Errorf("expected the reference definition's target, got %v", got)
		}
	})

	t.Run("line numbers are reported", func(t *testing.T) {
		md := "one\ntwo\n[a](b.md)\n"

		got := linksIn(md)

		if len(got) != 1 || got[0].line != 3 {
			t.Errorf("expected the link on line 3, got %v", got)
		}
	})

	dir := t.TempDir()
	doc := filepath.Join(dir, "SPEC.md")
	writeFile(t, doc, "# The Title\n\n## A Section, With Punctuation\n\n```\n## Not A Heading\n```\n")
	writeFile(t, filepath.Join(dir, "other.md"), "# Other\n")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("creating sub: %v", err)
	}

	from := filepath.Join(dir, "sub", "README.md")
	writeFile(t, from, "# From\n\n## Local Heading\n")

	tests := []struct {
		name    string
		target  string
		wantErr bool
	}{
		{name: "external links are not checked", target: "https://example.invalid/nowhere"},
		{name: "a mailto is not checked", target: "mailto:nobody@example.invalid"},
		{name: "a sibling that exists", target: "../other.md"},
		{name: "a sibling that does not", target: "../missing.md", wantErr: true},
		{name: "a directory", target: "../sub"},
		{name: "an anchor in another file", target: "../SPEC.md#a-section-with-punctuation"},
		{name: "an anchor no heading gives", target: "../SPEC.md#not-a-heading", wantErr: true},
		{name: "an anchor in this file", target: "#local-heading"},
		{name: "an anchor this file does not have", target: "#absent", wantErr: true},
		{name: "a fragment on a directory", target: "../sub#anything", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := checkLink(from, test.target)

			if test.wantErr && err == nil {
				t.Errorf("expected %q to be reported as broken", test.target)
			}
			if !test.wantErr && err != nil {
				t.Errorf("expected %q to resolve, got %v", test.target, err)
			}
		})
	}
}

func TestHeadingAnchorsMatchGitHubsRules(t *testing.T) {
	md := strings.Join([]string{
		"# avroc's own generators",
		"## The IR `FileDescriptorSet`",
		"### Tags — and what pinning one buys",
		"## Repeated",
		"## Repeated",
	}, "\n")

	got := headingAnchors(md)

	for _, want := range []string{
		"avrocs-own-generators",
		"the-ir-filedescriptorset",
		"tags--and-what-pinning-one-buys",
		"repeated",
		"repeated-1",
	} {
		if !got[want] {
			t.Errorf("expected anchor %q, got %v", want, got)
		}
	}
}

// link is one relative or absolute reference written in a Markdown file,
// together with the line it was written on so a failure names it.
type link struct {
	target string
	line   int
}

func (l link) String() string { return fmt.Sprintf("%s (line %d)", l.target, l.line) }

var (
	inlineLinkRE    = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)\)`)
	referenceLinkRE = regexp.MustCompile(`^\[[^\]]+\]:\s*(\S+)`)
	headingRE       = regexp.MustCompile(`^#{1,6}\s+(.*\S)\s*$`)
	notAnchorRE     = regexp.MustCompile(`[^\w\s-]`)
)

// linksIn extracts every link target in a Markdown document, ignoring the
// contents of fenced code blocks: a document that shows another document how to
// write a link — docs/CONVENTIONS.md does exactly that — is quoting a template,
// not pointing at a file, and resolving the quoted path from the quoting file's
// directory is how a correct document gets reported as broken.
func linksIn(md string) []link {
	var links []link
	var fence string

	for i, line := range strings.Split(md, "\n") {
		trimmed := strings.TrimSpace(line)

		switch {
		case fence != "":
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			continue
		case strings.HasPrefix(trimmed, "```"):
			fence = "```"
			continue
		case strings.HasPrefix(trimmed, "~~~"):
			fence = "~~~"
			continue
		}

		for _, m := range inlineLinkRE.FindAllStringSubmatch(line, -1) {
			links = append(links, link{target: m[1], line: i + 1})
		}
		if m := referenceLinkRE.FindStringSubmatch(trimmed); m != nil {
			links = append(links, link{target: m[1], line: i + 1})
		}
	}

	return links
}

// headingAnchors returns the anchors GitHub gives a document's headings:
// lowercased, formatting and punctuation dropped, spaces hyphenated, and a
// numeric suffix on the second and later use of one text.
func headingAnchors(md string) map[string]bool {
	anchors := make(map[string]bool)
	seen := make(map[string]int)
	var fence string

	for _, line := range strings.Split(md, "\n") {
		trimmed := strings.TrimSpace(line)

		switch {
		case fence != "":
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			continue
		case strings.HasPrefix(trimmed, "```"):
			fence = "```"
			continue
		case strings.HasPrefix(trimmed, "~~~"):
			fence = "~~~"
			continue
		}

		m := headingRE.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}

		text := strings.NewReplacer("`", "", "*", "", "_", "").Replace(m[1])
		anchor := strings.ReplaceAll(notAnchorRE.ReplaceAllString(strings.ToLower(text), ""), " ", "-")

		if n := seen[anchor]; n > 0 {
			anchors[fmt.Sprintf("%s-%d", anchor, n)] = true
		} else {
			anchors[anchor] = true
		}
		seen[anchor]++
	}

	return anchors
}

// checkLink resolves one link target from the file it was written in.
func checkLink(from, target string) error {
	if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
		return nil
	}

	path, fragment, _ := strings.Cut(target, "#")

	resolved := from
	if path != "" {
		resolved = filepath.Join(filepath.Dir(from), filepath.FromSlash(path))

		info, err := os.Stat(resolved)
		if err != nil {
			return fmt.Errorf("%s does not resolve: %w", target, err)
		}
		if fragment != "" && info.IsDir() {
			return fmt.Errorf("%s names a fragment of a directory, which has no headings", target)
		}
	}

	if fragment == "" {
		return nil
	}

	content, err := os.ReadFile(resolved)
	if err != nil {
		return fmt.Errorf("%s: reading the target: %w", target, err)
	}
	if !headingAnchors(string(content))[fragment] {
		return fmt.Errorf("%s: no heading in %s gives the anchor %q", target, filepath.Base(resolved), fragment)
	}

	return nil
}

// markdownFiles is every Markdown file in the repository, minus the directories
// that hold something other than this repository's documentation: .git, and
// .claude, which is where the backlog cycle checks out whole worktrees.
func markdownFiles(t *testing.T, root string) []string {
	t.Helper()

	var docs []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".claude", "bin", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".md") {
			docs = append(docs, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	return docs
}

// repositoryRoot is the directory holding go.mod, found by walking up from the
// package directory the test runs in.
func repositoryRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getting the working directory: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("walked to the filesystem root without finding go.mod")
		}
		dir = parent
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
