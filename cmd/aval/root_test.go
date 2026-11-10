package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// rootContract writes a git working tree with aval/unit.yaml rooted at
// the tree (scope.root: ..) and src/a.go, go.mod and x.json at the root.
// extra lines go at the top level of the contract. It returns the tree's
// real path and the contract path.
func rootContract(t *testing.T, root string, extra ...string) (string, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{".git/HEAD": "x\n", "src/a.go": "package a\n", "go.mod": "module x\n", "x.json": "{\"input\": true}\n"} {
		write(t, dir, rel, body)
	}
	body := "apiVersion: " + contract.APIVersion + `
kind: Test
metadata: {name: unit}
runner: {command: [sh, -c, "pwd > ranin"]}
scope:
  root: ` + root + `
  source: ["src/**", "*.json"]
  dependencies: [go.mod]
cacheability: hermetic
` + strings.Join(extra, "\n") + "\n"
	write(t, dir, "aval/unit.yaml", body)
	return dir, filepath.Join(dir, "aval", "unit.yaml")
}

// aval run runs runner.command and environment.probe in the root, and
// aval explain prints the root and root-relative paths.
func TestScopeRootRun(t *testing.T) {
	noLookup(t)
	dir, c := rootContract(t, "..", `environment:
  toolchain: {mod: "1"}
  probe: {mod: [sh, -c, "test -f go.mod && echo mod 1"]}`)
	code, stdout, stderr := runCLI("run", "-f", c, "--result-file", filepath.Join(dir, ".aval", "r.json"))
	got, _ := os.ReadFile(filepath.Join(dir, "ranin"))
	if code != ExitOK || strings.TrimSpace(string(got)) != dir || strings.Contains(stderr, "environment.probe") {
		t.Fatalf("exit %d, ran in %q, stdout %q, stderr %q", code, got, stdout, stderr)
	}
	code, stdout, _ = runCLI("explain", "-f", c)
	if code != ExitOK || !strings.Contains(stdout, "\nroot "+dir+"\n") || !strings.Contains(stdout, " src/a.go\n") || !strings.Contains(stdout, " go.mod\n") {
		t.Fatalf("explain: exit %d, stdout %q", code, stdout)
	}
}

// A root outside the git working tree, or no working tree at all, is a
// usage error (exit 2) for run and explain.
func TestScopeRootFailsClosed(t *testing.T) {
	_, above := rootContract(t, "../..")
	dir, none := rootContract(t, "..")
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ file, want string }{
		{above, "scope.root ../.. leads outside the git working tree"},
		{none, "no git working tree (.git) at or above"},
	} {
		for _, cmd := range []string{"run", "explain"} {
			code, _, stderr := runCLI(cmd, "-f", tc.file)
			if code != ExitUsage || !strings.Contains(stderr, tc.want) {
				t.Fatalf("%s %s: exit %d, stderr %q", cmd, tc.file, code, stderr)
			}
		}
	}
	c, err := contract.Load(none)
	if err != nil {
		t.Fatal(err)
	}
	c.Outputs = []string{"x.json"}
	if _, _, err := contractRoot(none, c); err == nil {
		t.Fatal("contractRoot without a working tree")
	}
	if _, err := verifyOutputs(t.Context(), "reg.example/r", c, none, listed("x.json")); err == nil || errors.Is(err, evidence.ErrVerification) {
		t.Fatalf("verifyOutputs without a root: %v", err)
	}
}

// False hit: the output skip set is computed against the root. An output
// beside the contract does not hide the input of the same name at the
// root, and an output at the root that is an input is refused.
func TestScopeRootOutputs(t *testing.T) {
	dir, c := rootContract(t, "..")
	code, stdout, stderr := runCLI("run", "-f", c, "--explain", "--fingerprint-only", "--result-file", filepath.Join(dir, "aval", "x.json"))
	if code != ExitOK || !strings.Contains(stdout, " x.json\n") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	code, _, stderr = runCLI("run", "-f", c, "--fingerprint-only", "--result-file", filepath.Join(dir, "x.json"))
	if code != ExitUsage || !strings.Contains(stderr, "is an input of the test") {
		t.Fatalf("output at the root: exit %d, stderr %q", code, stderr)
	}
}

// outputs resolve from the root: collected and checked there, and the
// contract file is named by its path relative to the root.
func TestScopeRootCollectOutputs(t *testing.T) {
	dir, file := rootContract(t, "..")
	write(t, dir, "coverage.xml", "cov")
	c, err := contract.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	c.Outputs = []string{"coverage.xml"}
	files, err := collect(t, file, c)
	if err != nil || len(files) != 1 || files[0].Path != "coverage.xml" || string(files[0].Data) != "cov" {
		t.Fatalf("collectOutputs = %+v, %v", files, err)
	}
	c.Outputs = []string{"aval/*.yaml"}
	if _, err := collect(t, file, c); err == nil || !strings.Contains(err.Error(), "output aval/unit.yaml is the contract") {
		t.Fatalf("contract as output: %v", err)
	}
	if _, err := verifyOutputs(t.Context(), "reg.example/r", c, file, listed("aval/unit.yaml")); !errors.Is(err, evidence.ErrVerification) {
		t.Fatalf("verifyOutputs contract: %v", err)
	}
}

// A reused result restores its outputs under the root.
func TestScopeRootRestore(t *testing.T) {
	fakeOutputs(t, nil, nil)
	fakeCandidates(t, nil, map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner, outputs: listed("coverage.xml")}})
	dir, c := rootContract(t, "..", "outputs: [coverage.xml]")
	code, stdout, stderr := runCLI("run", "-f", c, "--registry", "reg.example/r", "--policy", "p.yaml", "--result-file", filepath.Join(dir, ".aval", "r.json"))
	got, _ := os.ReadFile(filepath.Join(dir, "coverage.xml"))
	if code != ExitOK || string(got) != "bytes of coverage.xml" || !strings.Contains(stdout, "restored coverage.xml\n") {
		t.Fatalf("exit %d, coverage.xml %q, stdout %q, stderr %q", code, got, stdout, stderr)
	}
}

// collect is collectOutputs for c loaded from file, as signAndPush calls it.
func collect(t *testing.T, file string, c *contract.Contract) ([]registry.Output, error) {
	t.Helper()
	dir, name, err := contractRoot(file, c)
	if err != nil {
		t.Fatal(err)
	}
	return collectOutputs(dir, name, c)
}
