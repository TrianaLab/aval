package fingerprint

import (
	"errors"
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

// TestComputeFindingsAllowMutableFiles: files: accepts its exact ref in
// every in-scope file its globs match, and nowhere else: not the same ref
// outside the globs, not another ref in a matched file, not runner.command.
// file: stays a literal path, so a[1] is not a glob there.
func TestComputeFindingsAllowMutableFiles(t *testing.T) {
	one := "resources:\n- https://example.com/one.yaml\n"
	two := "resources:\n- https://example.com/one.yaml\n- https://example.com/two.yaml\n"
	for _, tc := range []struct {
		name    string
		allow   contract.AllowMutable
		allowed []string // file ref, in findings order
	}{
		{"globs match two of three", contract.AllowMutable{Files: []string{"a/*", "b/*"}, Ref: "resources[0]", Reason: "r"},
			[]string{"a/kustomization.yaml resources[0]", "b/kustomization.yaml resources[0]"}},
		{"one glob, character class", contract.AllowMutable{Files: []string{"[ab]/kustomization.yaml"}, Ref: "resources[0]", Reason: "r"},
			[]string{"a/kustomization.yaml resources[0]", "b/kustomization.yaml resources[0]"}},
		{"different ref in a matched file", contract.AllowMutable{Files: []string{"a/*"}, Ref: "resources[1]", Reason: "r"},
			[]string{"a/kustomization.yaml resources[1]"}},
		{"** never reaches runner.command", contract.AllowMutable{Files: []string{"**"}, Ref: "a.io/b@latest", Reason: "r"}, nil},
		{"file: is literal", contract.AllowMutable{File: "a[1]/kustomization.yaml", Ref: "resources[0]", Reason: "r"},
			[]string{"a[1]/kustomization.yaml resources[0]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.c.Scope.Source = append(f.c.Scope.Source, "*/kustomization.yaml")
			f.c.Runner.Command = []string{"sh", "-c", "go install a.io/b@latest && go test ./..."}
			f.write("a/kustomization.yaml", two)
			f.write("b/kustomization.yaml", one)
			f.write("c/kustomization.yaml", one)
			f.write("a1/kustomization.yaml", one)
			f.write("a[1]/kustomization.yaml", one)
			f.c.Scope.AllowMutable = []contract.AllowMutable{tc.allow}
			fp, err := f.compute()
			if err != nil {
				t.Fatal(err)
			}
			var allowed []string
			for _, x := range fp.Findings {
				if x.Allowed != "" {
					allowed = append(allowed, x.File+" "+x.Ref)
				}
			}
			if len(fp.Findings) != 7 || strings.Join(allowed, ",") != strings.Join(tc.allowed, ",") {
				t.Fatalf("allowed = %q, want %q; findings = %+v", allowed, tc.allowed, fp.Findings)
			}
		})
	}
}

// TestComputeFindingsAllowMutableFilesUnmatched: a files glob must match a
// file in scope; one that matches nothing, or only a file outside the
// scope, is a contract error, never a silently unused entry.
func TestComputeFindingsAllowMutableFilesUnmatched(t *testing.T) {
	for _, glob := range []string{"deploy/*.yaml", "README.md"} {
		f := newFixture(t)
		f.write("README.md", "image: out-of-scope:1\n")
		f.c.Scope.AllowMutable = []contract.AllowMutable{{Ref: "x", Reason: "r"}, {Files: []string{"compose.yaml", glob}, Ref: "x", Reason: "r"}}
		_, err := f.compute()
		if !errors.Is(err, ErrContract) || !strings.Contains(err.Error(), `scope.allowMutable[1].files[1]: glob "`+glob+`" matches no file in scope`) {
			t.Fatalf("%s: got %v", glob, err)
		}
	}
}

// TestCanonicalAllowMutableFiles: files: is part of the canonical contract
// as a set (order and repeats do not matter), and an empty list serializes,
// and so hashes, as an entry without it.
func TestCanonicalAllowMutableFiles(t *testing.T) {
	canon := func(a ...contract.AllowMutable) string {
		c := testContract()
		c.Scope.AllowMutable = a
		return string(canonicalContract(c))
	}
	if s := canon(contract.AllowMutable{Files: []string{}, Ref: "x", Reason: "r"}); !strings.Contains(s, `"allowMutable":[{"ref":"x","reason":"r"}]`) {
		t.Fatalf("empty files serialized: %s", s)
	}
	ab := canon(contract.AllowMutable{Files: []string{"a", "b"}, Ref: "x", Reason: "r"})
	if ab != canon(contract.AllowMutable{Files: []string{"b", "a", "b"}, Ref: "x", Reason: "r"}, contract.AllowMutable{Files: []string{"a", "b"}, Ref: "x", Reason: "r"}) {
		t.Fatal("files order or a repeated entry changes the canonical contract")
	}
	for _, other := range []string{canon(contract.AllowMutable{Ref: "x", Reason: "r"}), canon(contract.AllowMutable{Files: []string{"a"}, Ref: "x", Reason: "r"})} {
		if other == ab {
			t.Fatal("files does not change the canonical contract")
		}
	}
}
