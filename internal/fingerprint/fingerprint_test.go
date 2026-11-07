package fingerprint

import (
	"bytes"
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

var fixedEnv LookupEnv = func(k string) (string, bool) {
	if k == "DB_IMAGE" {
		return "postgres:16", true
	}
	return "", false
}

func TestHexStripsAlgorithm(t *testing.T) {
	if got := (Fingerprint{Digest: "sha256:abc"}).Hex(); got != "abc" {
		t.Fatalf("Hex() = %q", got)
	}
}

func TestNilEnvUsesProcessEnvironment(t *testing.T) {
	f := newFixture(t)
	t.Setenv("DB_IMAGE", "postgres:16")
	t.Setenv("MODE", "") // restores MODE after the test
	if err := os.Unsetenv("MODE"); err != nil {
		t.Fatal(err)
	}
	got, err := Compute(f.dir, f.c, nil)
	want, err2 := f.compute()
	if err != nil || err2 != nil {
		t.Fatal(err, err2)
	}
	if got.Digest != want.Digest {
		t.Fatal("nil env must resolve variables from the process environment")
	}
}

func TestUnreadableInputsFailClosed(t *testing.T) {
	if _, err := Compute(filepath.Join(t.TempDir(), "missing"), testContract(), fixedEnv); err == nil {
		t.Error("missing contract root must fail")
	}
	if _, _, err := (&hasher{buf: make([]byte, 512)}).hashFile(os.DirFS(t.TempDir()), "gone.go"); err == nil {
		t.Error("file removed after listing must fail")
	}
	f := newFixture(t)
	f.chmod("main.go", 0)
	if _, err := f.compute(); err == nil {
		t.Error("unreadable file in scope must fail")
	}
	// An unreadable directory could hide scoped files: the walk must fail,
	// not skip it.
	f = newFixture(t)
	f.chmod("pkg", 0)
	t.Cleanup(func() { _ = os.Chmod(f.path("pkg"), 0o755) })
	if _, err := f.compute(); err == nil {
		t.Error("unreadable directory must fail")
	}
}

// Audit F3: the walk only enters directories an include glob can match, so
// an unreadable directory outside the scope (a root-owned volume, a cache)
// does not break the fingerprint, and does not change it.
func TestWalkSkipsDirectoriesOutsideScope(t *testing.T) {
	f := newFixture(t)
	f.c.Scope = contract.Scope{Source: []string{"pkg/**"}, Dependencies: []string{"go.sum"}}
	want, err := f.compute()
	if err != nil {
		t.Fatal(err)
	}
	f.write("pgdata/base/1", "row")
	f.chmod("pgdata", 0)
	t.Cleanup(func() { _ = os.Chmod(f.path("pgdata"), 0o755) })
	got, err := f.compute()
	if err != nil {
		t.Fatalf("unreadable directory outside the scope: %v", err)
	}
	if got.Manifest != want.Manifest {
		t.Errorf("manifest changed:\n%s\nwant\n%s", got.Manifest, want.Manifest)
	}
	// The same directory inside the scope still fails closed.
	f.c.Scope.Source = append(f.c.Scope.Source, "pgdata/**")
	if _, err := f.compute(); err == nil {
		t.Error("unreadable directory in scope must fail")
	}
}

// A contract directory that is itself a symlink fingerprints like its
// target: only symlinks inside the root are rejected (#111).
func TestSymlinkedRootFingerprintsLikeTarget(t *testing.T) {
	f := newFixture(t)
	want, err := f.compute()
	if err != nil {
		t.Fatal(err)
	}
	f.symlink(f.dir, "../"+filepath.Base(f.dir)+"-link")
	link := f.dir + "-link"
	t.Cleanup(func() { _ = os.Remove(link) })
	f.dir = link
	got, err := f.compute()
	if err != nil {
		t.Fatal(err)
	}
	if got.Manifest != want.Manifest {
		t.Errorf("manifest changed:\n%s\nwant\n%s", got.Manifest, want.Manifest)
	}
}

// canonicalContract discards the json.Marshal error; guard against it ever
// collapsing to empty or identical bytes for distinct contracts.
func TestCanonicalContractNonEmptyAndDistinct(t *testing.T) {
	a, b := testContract(), testContract()
	b.Metadata.Name = a.Metadata.Name + "-other"
	ca, cb := canonicalContract(a), canonicalContract(b)
	if len(ca) == 0 {
		t.Fatal("canonical contract bytes must not be empty")
	}
	if bytes.Equal(ca, cb) {
		t.Error("contracts differing only in name must give different canonical bytes")
	}
}
