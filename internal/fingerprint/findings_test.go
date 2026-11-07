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

// TestComputeFindingsAllowMutableByFile: a generic ref (resources[0])
// repeats across kustomizations, so acknowledging one must not acknowledge
// the other. An entry with file: matches that file's finding only; a
// ref-only entry matches a ref reported in one file, and fails closed with
// a message asking for file: when several files report it.
func TestComputeFindingsAllowMutableByFile(t *testing.T) {
	remote := "resources:\n- https://example.com/remote.yaml\n"
	for _, tc := range []struct {
		name    string
		allow   []contract.AllowMutable
		allowed map[string]bool // file -> acknowledged
		hint    bool            // the unacknowledged findings ask for file:
	}{
		{"file matches its file only", []contract.AllowMutable{{File: "a/kustomization.yaml", Ref: "resources[0]", Reason: "r"}},
			map[string]bool{"a/kustomization.yaml": true}, false},
		{"both files", []contract.AllowMutable{{File: "a/kustomization.yaml", Ref: "resources[0]", Reason: "r"}, {File: "b/kustomization.yaml", Ref: "resources[0]", Reason: "r"}},
			map[string]bool{"a/kustomization.yaml": true, "b/kustomization.yaml": true}, false},
		{"ambiguous ref-only fails closed", []contract.AllowMutable{{Ref: "resources[0]", Reason: "r"}}, map[string]bool{}, true},
		{"ambiguous ref-only plus file", []contract.AllowMutable{{Ref: "resources[0]", Reason: "r"}, {File: "b/kustomization.yaml", Ref: "resources[0]", Reason: "r"}},
			map[string]bool{"b/kustomization.yaml": true}, true},
		{"file of another finding", []contract.AllowMutable{{File: "compose.yaml", Ref: "resources[0]", Reason: "r"}}, map[string]bool{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.c.Scope.Source = append(f.c.Scope.Source, "*/kustomization.yaml")
			f.write("a/kustomization.yaml", remote)
			f.write("b/kustomization.yaml", remote)
			f.c.Scope.AllowMutable = tc.allow
			fp, err := f.compute()
			if err != nil {
				t.Fatal(err)
			}
			if len(fp.Findings) != 2 {
				t.Fatalf("findings = %+v", fp.Findings)
			}
			for _, x := range fp.Findings {
				if (x.Allowed != "") != tc.allowed[x.File] {
					t.Errorf("%s: allowed = %q, want %v", x.File, x.Allowed, tc.allowed[x.File])
				}
				if hint := strings.Contains(x.Fix, "add file: "+x.File); x.Allowed == "" && hint != tc.hint {
					t.Errorf("%s: fix = %q, want hint %v", x.File, x.Fix, tc.hint)
				}
			}
		})
	}
}

// TestComputeFindingsRefOnlyUniqueStillWorks: a ref-only entry keeps
// acknowledging a ref reported in one file, even on several lines.
func TestComputeFindingsRefOnlyUniqueStillWorks(t *testing.T) {
	f := newFixture(t)
	f.write("compose.yaml", "services:\n  a:\n    image: postgres:16\n  b:\n    image: postgres:16\n")
	f.c.Scope.AllowMutable = []contract.AllowMutable{{Ref: "postgres:16", Reason: "r"}}
	fp, err := f.compute()
	if err != nil {
		t.Fatal(err)
	}
	if len(fp.Findings) != 2 || len(fp.Unpinned()) != 0 {
		t.Fatalf("findings = %+v", fp.Findings)
	}
}

// TestCanonicalAllowMutableFile: file: is part of the canonical contract,
// and an entry without it serializes, and so hashes, as before #126.
func TestCanonicalAllowMutableFile(t *testing.T) {
	c := testContract()
	c.Scope.AllowMutable = []contract.AllowMutable{{Ref: "x", Reason: "r"}}
	if s := string(canonicalContract(c)); !strings.Contains(s, `"allowMutable":[{"ref":"x","reason":"r"}]`) {
		t.Fatalf("empty file serialized: %s", s)
	}
	d := testContract()
	d.Scope.AllowMutable = []contract.AllowMutable{{File: "compose.yaml", Ref: "x", Reason: "r"}}
	if string(canonicalContract(c)) == string(canonicalContract(d)) {
		t.Fatal("file: does not change the canonical contract")
	}
}
