package architecture

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The docs site renders diagrams with a vendored Mermaid, never a CDN copy
// (#73). To update it: download the npm tarball of the new version, check it
// against the registry's dist.integrity, copy package/dist/mermaid.min.js and
// package/LICENSE over the two files below, then update mermaidSHA256 here
// and INTEGRITY in docs/javascripts/diagrams.js
// (`openssl dgst -sha384 -binary mermaid.min.js | base64`).
const (
	mermaidVersion = "11.17.2" // npm mermaid@11.17.2
	mermaidSHA256  = "581ed7d74bd9048d0e3a91363927d72ef22942d7722546b27f7cc29e35390eb8"
	mermaidJS      = "docs/javascripts/vendor/mermaid.min.js"
	mermaidLicence = "docs/javascripts/vendor/mermaid-LICENSE.txt"
	diagramsJS     = "docs/javascripts/diagrams.js"
)

func TestVendoredMermaid(t *testing.T) {
	js := readFile(t, mermaidJS)
	if got := sha256.Sum256([]byte(js)); hex.EncodeToString(got[:]) != mermaidSHA256 {
		t.Errorf("%s: sha256 %x, want %s (mermaid %s): re-vendor it or update the pin", mermaidJS, got, mermaidSHA256, mermaidVersion)
	}
	if !strings.Contains(readFile(t, mermaidLicence), "MIT License") {
		t.Errorf("%s: the MIT licence must ship with the vendored file", mermaidLicence)
	}
	sum := sha512.Sum384([]byte(js))
	sri := "sha384-" + base64.StdEncoding.EncodeToString(sum[:])
	if !strings.Contains(readFile(t, diagramsJS), `"`+sri+`"`) {
		t.Errorf("%s: INTEGRITY must be %s, the vendored file's hash", diagramsJS, sri)
	}
	// Material renders fences of class "mermaid" itself and fetches Mermaid
	// from a CDN when it finds no global; the site's fence class keeps it out.
	mk := readFile(t, "mkdocs.yml")
	if !strings.Contains(mk, "class: aval-mermaid") || strings.Contains(mk, "class: mermaid") {
		t.Error("mkdocs.yml: the mermaid fence must have class aval-mermaid, never mermaid")
	}
}

// Concept pages are for newcomers: one screen each (#73).
func TestConceptPagesAreShort(t *testing.T) {
	pages := trackedFiles(t, "docs/concepts/*.md")
	if len(pages) == 0 {
		t.Fatal("no concept pages found")
	}
	for _, p := range pages {
		if n := strings.Count(readFile(t, p), "\n"); n > 120 {
			t.Errorf("%s: %d lines, the limit for a concept page is 120", p, n)
		}
	}
}

// Size budget for the images and recordings the site ships (#77): pages
// load fast, and git keeps every byte of a binary forever.
const (
	imageBudget     = 100 << 10  // per image
	recordingBudget = 1536 << 10 // per recording (TestDocsDemos: the same per GIF)
	mediaBudget     = 2 << 20    // all of them together
)

var (
	imageExt     = []string{".png", ".jpg", ".jpeg", ".svg", ".webp", ".ico"}
	recordingExt = []string{".gif", ".mp4", ".webm", ".cast"}
)

// mediaBudgetProblems checks file sizes (path to bytes) against the budget.
// Files that are neither images nor recordings are not counted.
func mediaBudgetProblems(sizes map[string]int64) []string {
	var probs []string
	var total int64
	for p, n := range sizes {
		ext := strings.ToLower(filepath.Ext(p))
		limit := int64(recordingBudget)
		switch {
		case slices.Contains(imageExt, ext):
			limit = imageBudget
		case !slices.Contains(recordingExt, ext):
			continue
		}
		total += n
		if n > limit {
			probs = append(probs, fmt.Sprintf("%s: %d bytes, the budget is %d", p, n, limit))
		}
	}
	if total > mediaBudget {
		probs = append(probs, fmt.Sprintf("images and recordings: %d bytes in total, the budget is %d", total, mediaBudget))
	}
	slices.Sort(probs)
	return probs
}

func TestDocsMediaBudget(t *testing.T) {
	sizes := map[string]int64{}
	for _, p := range trackedFiles(t, "docs", "overrides") {
		fi, err := os.Stat(filepath.Join(root, p))
		if err != nil {
			t.Fatal(err)
		}
		sizes[p] = fi.Size()
	}
	for _, p := range mediaBudgetProblems(sizes) {
		t.Error(p + " (compress it, or shorten the tape)")
	}
}

func TestMediaBudgetCatchesLargeFiles(t *testing.T) {
	got := mediaBudgetProblems(map[string]int64{
		"docs/a.PNG":       imageBudget + 1,
		"docs/b.svg":       imageBudget,
		"docs/c.gif":       recordingBudget + 1,
		"docs/d.gif":       recordingBudget,
		"docs/demo.wasm":   10 << 20,
		"docs/index.md":    10 << 20,
		"overrides/e.webm": 1,
	})
	want := []string{
		fmt.Sprintf("docs/a.PNG: %d bytes, the budget is %d", imageBudget+1, imageBudget),
		fmt.Sprintf("docs/c.gif: %d bytes, the budget is %d", recordingBudget+1, recordingBudget),
		fmt.Sprintf("images and recordings: %d bytes in total, the budget is %d", 2*imageBudget+2*recordingBudget+3, mediaBudget),
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// previewProblems checks that ci-docs uploads the built site, so a reviewer
// can preview a docs PR (#77).
func previewProblems(data []byte) ([]string, error) {
	var wf workflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, err
	}
	for _, s := range wf.Jobs["ci-docs"].Steps {
		if strings.HasPrefix(s.Uses, "actions/upload-artifact@") && strings.TrimSpace(s.With["path"]) == "site" {
			if s.With["name"] != "docs-site" {
				return []string{"ci-docs: name the site artifact docs-site"}, nil
			}
			return nil, nil
		}
	}
	return []string{"ci-docs: no actions/upload-artifact step with path: site (the PR preview)"}, nil
}

func TestPRsUploadSitePreview(t *testing.T) {
	probs, err := previewProblems(readCI(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range probs {
		t.Error(p)
	}
}

func TestPreviewCheckCatchesMissingUpload(t *testing.T) {
	const upload = "jobs:\n  ci-docs:\n    steps:\n      - uses: actions/upload-artifact@x\n        with:\n          path: site\n          name: "
	for wf, want := range map[string]int{
		"jobs:\n  ci-docs:\n    steps:\n      - run: make ci-docs\n": 1,
		upload + "other\n":     1,
		upload + "docs-site\n": 0,
	} {
		probs, err := previewProblems([]byte(wf))
		if err != nil || len(probs) != want {
			t.Errorf("%q: got %q, %v; want %d problems", wf, probs, err, want)
		}
	}
	if _, err := previewProblems([]byte("jobs: [")); err == nil {
		t.Error("invalid YAML: want an error")
	}
}
