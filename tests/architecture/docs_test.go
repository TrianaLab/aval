package architecture

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var (
	fence      = regexp.MustCompile("(?ms)^\\s*```.*?^\\s*```")
	inlineCode = regexp.MustCompile("`[^`\n]*`")
	inlineLink = regexp.MustCompile(`\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)`)
	refLink    = regexp.MustCompile(`(?m)^\s*\[[^\]]+\]:\s*<?(\S+?)>?(?:\s|$)`)
	heading    = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*#*\s*$`)
	slugDrop   = regexp.MustCompile(`[^\p{L}\p{N} _-]`)
)

// links returns the link targets of a Markdown document, ignoring code.
func links(md string) []string {
	md = inlineCode.ReplaceAllString(fence.ReplaceAllString(md, ""), "")
	var out []string
	for _, re := range []*regexp.Regexp{inlineLink, refLink} {
		for _, m := range re.FindAllStringSubmatch(md, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

// anchors returns the heading anchors GitHub generates for md.
func anchors(md string) []string {
	md = fence.ReplaceAllString(md, "")
	seen := map[string]int{}
	var out []string
	for _, m := range heading.FindAllStringSubmatch(md, -1) {
		s := strings.ReplaceAll(slugDrop.ReplaceAllString(strings.ToLower(m[1]), ""), " ", "-")
		if n := seen[s]; n > 0 {
			out = append(out, fmt.Sprintf("%s-%d", s, n))
		} else {
			out = append(out, s)
		}
		seen[s]++
	}
	return out
}

// brokenLinks reports every relative link in the Markdown file doc (a
// slash-separated path under the repository root) whose file or heading
// anchor does not exist. read returns a file's content and whether it exists.
// External URLs are not fetched: the gate stays offline and deterministic.
func brokenLinks(doc string, read func(string) (string, bool)) []string {
	src, _ := read(doc)
	var bad []string
	for _, l := range links(src) {
		if strings.Contains(l, "://") || strings.HasPrefix(l, "mailto:") {
			continue
		}
		file, frag, _ := strings.Cut(l, "#")
		target := doc
		if file != "" {
			target = path.Clean(path.Join(path.Dir(doc), file))
		}
		content, ok := read(target)
		switch {
		case !ok || strings.HasPrefix(target, "../"):
			bad = append(bad, fmt.Sprintf("%s: %s: no such file", doc, l))
		case frag != "" && strings.HasSuffix(target, ".md") && !slices.Contains(anchors(content), frag):
			bad = append(bad, fmt.Sprintf("%s: %s: no such heading", doc, l))
		}
	}
	return bad
}

// Every relative link and anchor in the repository's Markdown resolves.
func TestDocsLinksResolve(t *testing.T) {
	read := func(rel string) (string, bool) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if fi, err := os.Stat(p); err != nil || fi.IsDir() {
			return "", err == nil // a directory link resolves; it has no anchors
		}
		b, err := os.ReadFile(p)
		return string(b), err == nil
	}
	docs := trackedFiles(t, "*.md")
	if len(docs) == 0 {
		t.Fatal("no Markdown files found")
	}
	for _, doc := range docs {
		for _, p := range brokenLinks(doc, read) {
			t.Error(p)
		}
	}
}

func TestLinkCheckCatchesBrokenLinks(t *testing.T) {
	files := map[string]string{
		"README.md": "# Aval\n\n## How it works\n\n## Setup: Go 1.25\n\n## Setup: Go 1.25\n\n" +
			"[ok](docs/a.md) [ok](docs/a.md#trust-levels) [ok](#how-it-works)\n" +
			"[ok](#setup-go-125) [ok](#setup-go-125-1) [ok](examples) [ok](https://example.com/x)\n" +
			"[ok](<docs/a.md> \"title\") `[code](missing.md)`\n" +
			"```md\n[code](missing.md)\n```\n" +
			"[bad1](docs/missing.md) [bad2](docs/a.md#missing) [bad3](#nope) [bad4](../outside.md)\n\n" +
			"[ref]: docs/gone.md\n",
		"docs/a.md":     "# A\n\n## Trust levels\n\n[ok](../README.md#how-it-works) [bad5](a.md#a-1)\n",
		"examples":      "",
		"outside.md":    "",
		"../outside.md": "",
	}
	read := func(rel string) (string, bool) { s, ok := files[rel]; return s, ok }
	got := append(brokenLinks("README.md", read), brokenLinks("docs/a.md", read)...)
	want := []string{
		"README.md: docs/missing.md: no such file",
		"README.md: docs/a.md#missing: no such heading",
		"README.md: #nope: no such heading",
		"README.md: ../outside.md: no such file",
		"README.md: docs/gone.md: no such file",
		"docs/a.md: a.md#a-1: no such heading",
	}
	if !slices.Equal(got, want) {
		t.Errorf("broken links:\n got %q\nwant %q", got, want)
	}
}
