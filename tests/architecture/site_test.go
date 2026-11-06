package architecture

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
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
