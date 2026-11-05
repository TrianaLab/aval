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
	if _, _, err := hashFile(filepath.Join(t.TempDir(), "gone.go")); err == nil {
		t.Error("file removed after listing must fail")
	}
	f := newFixture(t)
	f.chmod("main.go", 0)
	if _, err := f.compute(); err == nil {
		t.Error("unreadable file in scope must fail")
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
