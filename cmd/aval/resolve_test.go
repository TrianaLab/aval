package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/TrianaLab/aval/internal/fingerprint"
	"github.com/TrianaLab/aval/internal/resolve"
)

// --explain reports what scope.resolve cost on stderr, so stdout stays the
// manifest that was hashed.
func TestExplainReportsResolveCost(t *testing.T) {
	fp := fingerprint.Fingerprint{Manifest: "m\n", Resolved: []resolve.Result{
		{Kind: "npm", Ref: "a@^1", ID: "sha512-x", Took: 1500 * time.Microsecond},
		{Kind: "url", Ref: "https://e/x", ID: "sha256:y", Took: time.Second},
	}}
	var out, warn bytes.Buffer
	if err := printFingerprint(&out, &warn, fp, nil, runFlags{explain: true}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "m\n" || !strings.Contains(warn.String(), "resolved npm a@^1 to sha512-x in 2ms\n") ||
		!strings.HasSuffix(warn.String(), "resolved 2 reference(s) in 1.002s\n") {
		t.Fatalf("stdout %q\nstderr %q", out.String(), warn.String())
	}
	warn.Reset()
	printResolveCost(&warn, fingerprint.Fingerprint{})
	if warn.Len() != 0 {
		t.Fatalf("no scope.resolve, no cost line: %q", warn.String())
	}
}
