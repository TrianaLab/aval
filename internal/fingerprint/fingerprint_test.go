package fingerprint

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/TrianaLab/aval/internal/contract"
)

func testContract() *contract.Contract {
	return &contract.Contract{
		APIVersion: contract.APIVersion,
		Kind:       contract.Kind,
		Metadata:   contract.Metadata{Name: "it"},
		Runner:     contract.Runner{Command: []string{"go", "test", "./..."}},
		Scope: contract.Scope{
			Source:       []string{"**/*.go", "compose.yaml"},
			Exclude:      []string{"vendor/**"},
			Dependencies: []string{"go.sum"},
		},
		Environment: contract.Environment{
			Toolchain: map[string]string{"go": "1.25", "docker": "27"},
			Env:       []string{"DB_IMAGE", "MODE"},
		},
		Cacheability: contract.ControlledEnvironment,
	}
}

var baseFiles = map[string]string{
	"main.go":       "package main\n",
	"pkg/a/a.go":    "package a\n",
	"compose.yaml":  "services: {}\n",
	"go.sum":        "example.com/x v1.0.0 h1:abc\n",
	"README.md":     "not in scope\n",
	"vendor/v/v.go": "package v\n",
}

func writeTree(t *testing.T, files map[string]string, order []string) string {
	t.Helper()
	dir := t.TempDir()
	if order == nil {
		for f := range files {
			order = append(order, f)
		}
	}
	for _, f := range order {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(files[f]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

var fixedEnv LookupEnv = func(k string) (string, bool) {
	if k == "DB_IMAGE" {
		return "postgres:16", true
	}
	return "", false
}

func compute(t *testing.T, dir string, c *contract.Contract) string {
	t.Helper()
	fp, err := Compute(dir, c, fixedEnv)
	if err != nil {
		t.Fatal(err)
	}
	return fp.Digest
}

func TestSameInputSameHash(t *testing.T) {
	a := compute(t, writeTree(t, baseFiles, nil), testContract())
	b := compute(t, writeTree(t, baseFiles, nil), testContract())
	if a != b {
		t.Fatalf("same inputs in different directories produced %s and %s", a, b)
	}
}

func TestScopeChangeChangesHash(t *testing.T) {
	base := compute(t, writeTree(t, baseFiles, nil), testContract())
	for _, f := range []string{"main.go", "pkg/a/a.go", "compose.yaml", "go.sum"} {
		changed := clone(baseFiles)
		changed[f] += "// changed\n"
		if got := compute(t, writeTree(t, changed, nil), testContract()); got == base {
			t.Errorf("changing in-scope file %s did not change the fingerprint", f)
		}
	}
	added := clone(baseFiles)
	added["pkg/b/b.go"] = "package b\n"
	if compute(t, writeTree(t, added, nil), testContract()) == base {
		t.Error("adding an in-scope file did not change the fingerprint")
	}
	removed := clone(baseFiles)
	delete(removed, "pkg/a/a.go")
	if compute(t, writeTree(t, removed, nil), testContract()) == base {
		t.Error("removing an in-scope file did not change the fingerprint")
	}
	renamed := clone(baseFiles)
	renamed["pkg/a/renamed.go"] = renamed["pkg/a/a.go"]
	delete(renamed, "pkg/a/a.go")
	if compute(t, writeTree(t, renamed, nil), testContract()) == base {
		t.Error("renaming an in-scope file did not change the fingerprint")
	}
}

func TestOutOfScopeChangeKeepsHash(t *testing.T) {
	base := compute(t, writeTree(t, baseFiles, nil), testContract())
	changed := clone(baseFiles)
	changed["README.md"] = "edited\n"
	changed["vendor/v/v.go"] = "package v // excluded\n"
	changed["docs/new.md"] = "new\n"
	if got := compute(t, writeTree(t, changed, nil), testContract()); got != base {
		t.Fatalf("out-of-scope changes altered the fingerprint: %s != %s", got, base)
	}
}

func TestReorderingKeepsHash(t *testing.T) {
	base := compute(t, writeTree(t, baseFiles, nil), testContract())

	// Files created in a different order.
	order := []string{"vendor/v/v.go", "go.sum", "pkg/a/a.go", "README.md", "compose.yaml", "main.go"}
	if got := compute(t, writeTree(t, baseFiles, order), testContract()); got != base {
		t.Error("file creation order changed the fingerprint")
	}

	// Globs and env names listed in a different order.
	c := testContract()
	c.Scope.Source = []string{"compose.yaml", "**/*.go"}
	c.Environment.Env = []string{"MODE", "DB_IMAGE"}
	if got := compute(t, writeTree(t, baseFiles, nil), c); got != base {
		t.Error("glob or env order changed the fingerprint")
	}
}

func TestContractAndEnvironmentAffectHash(t *testing.T) {
	dir := writeTree(t, baseFiles, nil)
	base := compute(t, dir, testContract())

	c := testContract()
	c.Runner.Command = []string{"go", "test", "-race", "./..."}
	if compute(t, dir, c) == base {
		t.Error("changing runner.command did not change the fingerprint")
	}
	c = testContract()
	c.Environment.Toolchain["go"] = "1.26"
	if compute(t, dir, c) == base {
		t.Error("changing declared toolchain did not change the fingerprint")
	}
	fp, err := Compute(dir, testContract(), func(k string) (string, bool) { return "postgres:17", k == "DB_IMAGE" })
	if err != nil {
		t.Fatal(err)
	}
	if fp.Digest == base {
		t.Error("changing a declared env value did not change the fingerprint")
	}
}

func TestExecutableBitAffectsHash(t *testing.T) {
	dir := writeTree(t, baseFiles, nil)
	base := compute(t, dir, testContract())
	if err := os.Chmod(filepath.Join(dir, "main.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	if compute(t, dir, testContract()) == base {
		t.Error("changing the executable bit did not change the fingerprint")
	}
}

func TestFailClosed(t *testing.T) {
	missingDep := clone(baseFiles)
	delete(missingDep, "go.sum")
	if _, err := Compute(writeTree(t, missingDep, nil), testContract(), fixedEnv); err == nil {
		t.Error("missing dependency file must fail")
	}

	empty := map[string]string{"README.md": "x", "go.sum": "x"}
	if _, err := Compute(writeTree(t, empty, nil), testContract(), fixedEnv); err == nil {
		t.Error("scope matching no source files must fail")
	}

	dir := writeTree(t, baseFiles, nil)
	if err := os.Symlink(filepath.Join(dir, "main.go"), filepath.Join(dir, "link.go")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if _, err := Compute(dir, testContract(), fixedEnv); err == nil {
		t.Error("symlink in scope must fail")
	}
}

func TestNewlineInPathFailsClosed(t *testing.T) {
	c := testContract()
	c.Scope.Source = []string{"x*", "y"}
	c.Scope.Dependencies = nil
	// Tree B forges tree A's "y" manifest line through a newline in a name.
	forged := "x\nsource " + sum([]byte("c2")) + " - y"
	dir := writeTree(t, map[string]string{forged: "c1"}, nil)
	if _, err := Compute(dir, c, fixedEnv); err == nil {
		t.Fatal("newline in path must fail")
	}
}

func clone(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func TestEmptyEqualsOmitted(t *testing.T) {
	dir := writeTree(t, baseFiles, nil)
	a, b := testContract(), testContract()
	a.Scope.Exclude, a.Environment.Toolchain = nil, nil
	b.Scope.Exclude, b.Environment.Toolchain = []string{}, map[string]string{}
	if compute(t, dir, a) != compute(t, dir, b) {
		t.Fatal("explicitly empty lists/maps must hash like omitted ones")
	}
}
