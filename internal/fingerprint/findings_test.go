package fingerprint

import (
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/contract"
)

// TestComputeFindings: mutable references in hashed files and in
// runner.command are reported, outside the manifest, and scope.allowMutable
// acknowledges exactly the refs it names.
func TestComputeFindings(t *testing.T) {
	f := newFixture(t)
	f.write("compose.yaml", "services:\n  db:\n    image: postgres:16\n")
	f.write("README.md", "image: out-of-scope:1\n") // not hashed, not scanned
	f.c.Runner.Command = []string{"sh", "-c", "go install a.io/b@latest && go test ./..."}
	f.c.Scope.AllowMutable = []contract.AllowMutable{{Ref: "postgres:16", Reason: "scanned nightly"}}
	fp, err := f.compute()
	if err != nil {
		t.Fatal(err)
	}
	if len(fp.Findings) != 2 || fp.Findings[0].Ref != "postgres:16" || fp.Findings[0].Allowed != "scanned nightly" {
		t.Fatalf("findings = %+v", fp.Findings)
	}
	if u := fp.Unpinned(); len(u) != 1 || u[0].Ref != "a.io/b@latest" || u[0].File != "runner.command" {
		t.Fatalf("unpinned = %+v", u)
	}
	if strings.Contains(fp.Manifest, "postgres") {
		t.Fatalf("findings leaked into the manifest:\n%s", fp.Manifest)
	}
}

// TestComputeFindingsScanErrorIsNeverAllowed: an entry that names a file
// does not accept the finding that Aval could not read that file.
func TestComputeFindingsScanErrorIsNeverAllowed(t *testing.T) {
	f := newFixture(t)
	f.write("compose.yaml", strings.Repeat("#", 1<<20+1))
	f.c.Scope.AllowMutable = []contract.AllowMutable{{Ref: "compose.yaml", Reason: "no lockfile on purpose"}}
	fp, err := f.compute()
	if err != nil {
		t.Fatal(err)
	}
	if u := fp.Unpinned(); len(u) != 1 || u[0].Ecosystem != "scan" || u[0].Ref != "compose.yaml" {
		t.Fatalf("unpinned = %+v", u)
	}
}
