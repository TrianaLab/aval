package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TrianaLab/aval/internal/attest"
	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// Issue #67: --push that could not sign fails with exit 2 before the test
// runs, for run --push and push alike; the hint names the key format.
func TestPushPreconditionsFailFast(t *testing.T) {
	s := sign
	t.Cleanup(func() { sign = s })
	sign = func(context.Context, *evidence.Statement, attest.SignOptions) (*attest.Signed, error) {
		t.Fatal("signed despite a failed precondition")
		return nil, nil
	}
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	p384DER, _ := x509.MarshalPKCS8PrivateKey(p384)
	block := func(typ string, b []byte) []byte { return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: b}) }
	cases := []struct {
		name, key, stderr string
	}{
		{"no key and no token", "", "$" + idTokenEnv},
		{"unreadable key", filepath.Join(dir, "nope.pem"), "openssl ecparam -name prime256v1"},
		{"cosign key", write("cosign.key", block("ENCRYPTED SIGSTORE PRIVATE KEY", []byte("x"))), "P-256 private key in PEM"},
		{"old cosign key", write("cosign-old.key", block("ENCRYPTED COSIGN PRIVATE KEY", []byte("x"))), "openssl ecparam -name prime256v1"},
		{"not PEM", write("raw.key", []byte("junk")), "not a PEM file"},
		{"corrupt PEM", write("bad.key", block("PRIVATE KEY", []byte("x"))), "P-256"},
		{"P-384 key", write("p384.key", block("PRIVATE KEY", p384DER)), "not an ECDSA P-256 key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv, ran := marker(t, 0)
			c := contractWith(t, "hermetic", argv...)
			result := filepath.Join(t.TempDir(), "r.json")
			for _, args := range [][]string{
				{"run", "-f", c, "--registry", "reg.example/r", "--push", "--result", result},
				{"push", "-f", c, "--registry", "reg.example/r", "--result", result},
			} {
				if tc.key != "" {
					args = append(args, "--key", tc.key)
				}
				t.Setenv(idTokenEnv, "")
				if code, _, stderr := runCLI(args...); code != ExitUsage || ran() || !strings.Contains(stderr, tc.stderr) {
					t.Errorf("%s: exit %d ran %v, stderr %q", args[0], code, ran(), stderr)
				}
			}
		})
	}
}

// Issue #67: a --registry with a scheme, without a repository, or with a tag
// or digest exits 2 with an example before anything runs or is looked up.
func TestRegistryMustBeARepository(t *testing.T) {
	noLookup(t)
	argv, ran := marker(t, 0)
	c := contractWith(t, "hermetic", argv...)
	for _, repo := range []string{"https://ghcr.io/acme/orders-aval", "http://localhost:5000/x", "ghcr.io", "localhost:5000", "ghcr.io/acme/orders-aval:v1", "ghcr.io/acme/orders-aval@sha256:" + strings.Repeat("a", 64)} {
		for _, cmd := range []string{"run", "verify", "push"} {
			code, _, stderr := runCLI(cmd, "-f", c, "--registry", repo)
			if code != ExitUsage || ran() || !strings.Contains(stderr, "e.g. ghcr.io/acme/orders-aval") || !strings.Contains(stderr, "is not an OCI repository") {
				t.Errorf("%s --registry %s: exit %d ran %v, stderr %q", cmd, repo, code, ran(), stderr)
			}
		}
	}
}

// Issue #67 (follow-up from #78), false hit: Aval skips its own output files
// when hashing, so an output path that names an input would drop that input
// from the fingerprint and let a changed input reuse an old result. Such a
// path exits 2 before the test runs, and the input is left untouched.
func TestOutputThatIsAnInputIsRejected(t *testing.T) {
	noLookup(t)
	argv, ran := marker(t, 0)
	c := contractWith(t, "hermetic", argv...)
	key, _ := devKey(t, t.TempDir(), "k")
	in := filepath.Join(filepath.Dir(c), "in.txt") // matched by scope.source
	for _, args := range [][]string{
		{"run", "-f", c, "--result", in},
		{"run", "-f", c, "--report", in},
		{"run", "-f", c, "--fingerprint-only", "--result", in},
		{"push", "-f", c, "--registry", "reg.example/r", "--key", key, "--result", in},
	} {
		code, _, stderr := runCLI(args...)
		if code != ExitUsage || ran() || !strings.Contains(stderr, "is an input of the test") {
			t.Errorf("%v: exit %d ran %v, stderr %q", args, code, ran(), stderr)
		}
		if b, _ := os.ReadFile(in); string(b) != "x" {
			t.Fatalf("%v overwrote the input: %q", args, b)
		}
	}
}

func TestInScope(t *testing.T) {
	dir := t.TempDir()
	c := &contract.Contract{Scope: contract.Scope{Source: []string{"**"}, Exclude: []string{"out/**"}}}
	cases := []struct {
		path string
		want bool
	}{
		{"r.json", true},
		{"sub/r.json", true},
		{"out", true}, // out/** excludes the files under out, not a file named out
		{"out/r.json", false},
		{".aval/r.json", false},
		{"sub/.aval/r.json", false},
		{".git/r.json", false},
		{".aval", true}, // a file named .aval is not a skipped directory
		{"../r.json", false},
	}
	for _, tc := range cases {
		if got := inScope(dir, c, filepath.Join(dir, tc.path)); got != tc.want {
			t.Errorf("inScope(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
	deps := &contract.Contract{Scope: contract.Scope{Source: []string{"src/**"}, Exclude: []string{"deps/**"}, Dependencies: []string{"deps/go.sum"}}}
	if !inScope(dir, deps, filepath.Join(dir, "deps", "go.sum")) || inScope(dir, deps, filepath.Join(dir, "deps", "other")) || inScope(dir, deps, "") {
		t.Error("dependencies are inputs even when source excludes them; nothing else is")
	}
}

// What sits at an in-scope output path decides: nothing, or Aval's own result
// or report, is an output; anything else is an input (fail closed).
func TestIsInput(t *testing.T) {
	dir := t.TempDir()
	c := &contract.Contract{Scope: contract.Scope{Source: []string{"**"}}}
	report, _ := json.Marshal(evidence.Report{Version: evidence.ReportVersion, Outcome: evidence.OutcomeMiss})
	pred := &evidence.Predicate{
		SchemaVersion: evidence.SchemaVersion,
		Subject:       evidence.Subject{Name: "unit", Digest: map[string]string{"sha256": strings.Repeat("a", 64)}},
		Test:          evidence.Test{ID: "unit", Cacheability: "hermetic"},
		Result:        evidence.ResultPassed,
		Execution:     evidence.Execution{Fingerprint: "sha256:" + strings.Repeat("a", 64), FingerprintVersion: "aval-fingerprint/v1"},
		Runner:        evidence.Runner{Identity: "host"},
		StartedAt:     time.Unix(1, 0).UTC(),
		FinishedAt:    time.Unix(2, 0).UTC(),
	}
	if err := pred.Validate(); err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(pred)
	files := map[string][]byte{"src.go": []byte("package x"), "result.json": result, "report.json": report, "other-report.json": []byte(`{"version":"v0"}`)}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("src.go", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		"missing.json":      false,
		"result.json":       false,
		"report.json":       false,
		"src.go":            true,
		"other-report.json": true,
		"link":              true,
		"sub":               true,
	} {
		if got := isInput(dir, c, filepath.Join(dir, name)); got != want {
			t.Errorf("isInput(%q) = %v, want %v", name, got, want)
		}
	}
	unreadable := filepath.Join(dir, "unreadable")
	if err := os.WriteFile(unreadable, nil, 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(unreadable); err != nil && !isInput(dir, c, unreadable) {
		t.Error("an unreadable file is an input")
	}
	if isInput(dir, &contract.Contract{Scope: contract.Scope{Source: []string{"*.go"}}}, filepath.Join(dir, "result.json")) {
		t.Error("an out-of-scope path is never an input")
	}
}
