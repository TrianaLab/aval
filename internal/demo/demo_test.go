package demo

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"runtime"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/pkg/evidence"
)

// exec runs line and fails the test unless it exits with code.
func exec(t *testing.T, d *Demo, line string, code int) (string, string) {
	t.Helper()
	var out, errw strings.Builder
	if got := d.Exec(line, &out, &errw); got != code {
		t.Fatalf("%s: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", line, got, code, out.String(), errw.String())
	}
	return out.String(), errw.String()
}

func mustContain(t *testing.T, line, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("%s: output lacks %q:\n%s", line, w, got)
		}
	}
}

// The story of the page: a miss runs and pushes, the next run reuses, an
// input outside the scope keeps the fingerprint and one inside misses, and a
// tampered attestation is a verification failure (exit 5) that is never
// reused (invariant 4).
func TestStory(t *testing.T) {
	d := New()
	out, _ := exec(t, d, "aval run", exitOK)
	mustContain(t, "first run", out, "· miss greet ", "  reason    "+DefaultRepo+": no attestation found for fingerprint", "✓ greet passed (exit 0) in 4ms\n", "✓ pushed "+DefaultRepo+"@sha256:", "greet passed in 4ms; published for the next run with these inputs.\n")
	out, _ = exec(t, d, "aval run", exitOK)
	mustContain(t, "second run", out, "↺ verified greet", "  signer    key ", ", trust level developer", "↺ reused passed result\ngreet: reused a verified passed result, saved 4ms; nothing ran.\n")
	if strings.Contains(out, "run greet") || strings.ContainsRune(out, '\x1b') {
		t.Fatal("a verified result must not run the test")
	}

	out, _ = exec(t, d, "aval verify -o json", exitOK)
	var r evidence.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.Outcome != evidence.OutcomeVerified || r.Signer.TrustLevel != evidence.TrustDeveloper || r.Predicate.Runner.Identity != "browser" {
		t.Fatalf("verify report %+v, %v", r, err)
	}
	fp := r.Fingerprint

	exec(t, d, "edit README.md", exitOK)
	out, _ = exec(t, d, "aval run --explain", exitOK)
	mustContain(t, "out of scope edit", out, "aval-fingerprint/v1\n", "source ", " - greet.go\n", "↺ verified greet "+fp[:15]+"\n", "↺ reused passed result")
	exec(t, d, "edit aval.yaml", exitOK) // a comment: the canonical contract is the same
	out, _ = exec(t, d, "aval verify --output=text", exitOK)
	mustContain(t, "comment in aval.yaml", out, "verified greet "+fp)

	exec(t, d, "edit greet.go", exitOK)
	out, _ = exec(t, d, "aval verify", exitMiss)
	mustContain(t, "in scope edit", out, "miss greet")
	if strings.Contains(out, fp) {
		t.Fatal("an edited source must change the fingerprint")
	}
	exec(t, d, "aval run", exitOK)

	out, _ = exec(t, d, "tamper", exitOK)
	mustContain(t, "tamper", out, "tampered with 1 attestation(s)")
	out, errw := exec(t, d, "aval verify", exitVerificationFailed)
	mustContain(t, "verify tampered", out, "verification-failed greet", "reason: attestation verification failed: signature: ", "(trust policy policy.yaml)")
	mustContain(t, "verify tampered", errw, "aval: SECURITY: 1 attestation(s) for greet failed signature or policy verification")
	out, errw = exec(t, d, "aval run", exitVerificationFailed)
	mustContain(t, "run tampered", out, "✗ verification-failed greet", "· run greet: go test ./...\n", "· greet passed (exit 0) in 4ms\n", "· pushed ", "✗ SECURITY: an attestation for greet failed verification")
	if strings.Contains(out, "✓") {
		t.Fatalf("run tampered: a success line after a verification failure: %q", out)
	}
	if lines := strings.Split(strings.TrimSpace(errw), "\n"); len(lines) != 2 || lines[0] != "✗ "+strings.TrimPrefix(lines[1], "aval: ") || !strings.HasPrefix(lines[1], "aval: SECURITY: ") {
		t.Fatalf("run tampered: stderr %q, want the SECURITY line when found and last", errw)
	}
	// The tampered result stays beside the fresh one: exit 5 (#195).
	out, errw = exec(t, d, "aval verify", exitVerificationFailed)
	mustContain(t, "verify beside tampered", out, "verified greet", "rejected: "+DefaultRepo+"@sha256:")
	mustContain(t, "verify beside tampered", errw, "aval: SECURITY: 1 attestation(s)")

	out, _ = exec(t, d, "reset", exitOK)
	mustContain(t, "reset", out, "reset: original files, empty registry, new throwaway key")
	out, _ = exec(t, d, "aval verify", exitMiss)
	mustContain(t, "after reset", out, "miss greet "+r.Fingerprint)
}

func TestShellCommands(t *testing.T) {
	d := New()
	exec(t, d, "   ", exitOK)
	for _, line := range []string{"help", "aval", "aval help", "aval --help", "aval -h"} {
		out, _ := exec(t, d, line, exitOK)
		mustContain(t, line, out, "entirely in your browser", "AVAL_POLICY=policy.yaml", "are set for you", "Keyless (Sigstore) signing is not available")
	}
	out, _ := exec(t, d, "ls", exitOK)
	mustContain(t, "ls", out, "README.md  aval.yaml  go.mod  greet.go  greet_test.go  policy.yaml  throwaway.pub")
	out, _ = exec(t, d, "cat throwaway.pub", exitOK)
	mustContain(t, "cat", out, "BEGIN PUBLIC KEY")
	cases := map[string]string{
		"cat nope":         "cat: nope: no such file",
		"edit nope":        "edit: nope: no such file",
		"edit policy.yaml": "edit: policy.yaml is fixed in this demo",
		"tamper":           "tamper: nothing is published for the current inputs",
	}
	for line, want := range cases {
		_, errw := exec(t, d, line, exitError)
		mustContain(t, line, errw, want)
	}
	_, errw := exec(t, d, "rm -rf /", exitNotFound)
	mustContain(t, "rm", errw, "rm: command not found (type help)")
}

// aval explain prints run --explain's manifest under a header, the digest
// last, and looks nothing up: the registry stays empty.
func TestExplain(t *testing.T) {
	d := New()
	run, _ := exec(t, d, "aval run --explain", exitOK)
	d = New()
	out, _ := exec(t, d, "aval explain", exitOK)
	digest, _ := exec(t, d, "aval explain --digest", exitOK)
	manifest, _, _ := strings.Cut(run, "· miss greet ")
	if want := "fingerprint manifest for greet (aval-fingerprint/v1)\n" + manifest + "fingerprint " + digest; out != want {
		t.Fatalf("explain %q, want %q", out, want)
	}
	if !strings.HasPrefix(digest, "sha256:") || len(d.reg) != 0 {
		t.Fatalf("digest %q, registry %v", digest, d.reg)
	}
	d.files["aval.yaml"] = []byte("kind: Nope\n")
	_, errw := exec(t, d, "aval explain", exitUsage)
	mustContain(t, "bad contract", errw, "aval: aval.yaml: ")
}

// Usage errors carry the CLI's words (cobra's for flags) and exit 2.
func TestUsageErrors(t *testing.T) {
	cases := map[string]string{
		"aval run --foo":         "aval: unknown flag: --foo",
		"aval verify --explain":  "aval: unknown flag: --explain",
		"aval verify -x":         "aval: unknown shorthand flag: 'x' in -x",
		"aval verify -o":         "aval: flag needs an argument: 'o' in -o",
		"aval verify --output":   "aval: flag needs an argument: --output",
		"aval run extra":         `aval: unknown command "extra" for "aval run"`,
		"aval verify -o yaml":    `aval: --output must be text or json, got "yaml"`,
		"aval nope":              `aval: unknown command "nope"`,
		"aval push":              "aval: push is not in this demo",
		"aval stats publish x":   "aval: stats is not in this demo: it keeps no run records",
		"aval explain --explain": "aval: unknown flag: --explain",
		"aval run --digest":      "aval: unknown flag: --digest",
		"aval verify --plain":    "aval: unknown flag: --plain",
		"aval explain extra":     `aval: unknown command "extra" for "aval explain"`,
		"aval verify --output=x": `got "x"`,
	}
	d := New()
	for line, want := range cases {
		_, errw := exec(t, d, line, exitUsage)
		mustContain(t, line, errw, want)
	}
}

// A contract that does not parse is a usage error; a fingerprint that
// cannot be computed is an error, and nothing is looked up or run.
func TestFingerprintErrors(t *testing.T) {
	d := New()
	d.files["aval.yaml"] = []byte("kind: Nope\n")
	for _, line := range []string{"aval run", "aval verify", "tamper"} {
		_, errw := exec(t, d, line, exitUsage)
		mustContain(t, line, errw, "aval: aval.yaml: ")
	}
	// The browser has no environment: a declared variable is unset.
	d = New()
	d.files["aval.yaml"] = []byte(strings.Replace(project["aval.yaml"], "  toolchain:", "  env: [HOME]\n  toolchain:", 1))
	out, _ := exec(t, d, "aval run --explain", exitOK)
	mustContain(t, "env", out, "\nenv HOME unset\n")

	// A declared os is overridden by the real one in the pushed predicate.
	d = New()
	d.files["aval.yaml"] = []byte(strings.Replace(project["aval.yaml"], "toolchain: {go: \"1.27\"}", "toolchain: {go: \"1.27\", os: plan9}", 1))
	exec(t, d, "aval run", exitOK)
	out, _ = exec(t, d, "aval verify -o json", exitOK)
	var r evidence.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.Predicate.Execution.Environment["os"] != runtime.GOOS {
		t.Fatalf("verify report %+v, %v", r, err)
	}

	d = New()
	delete(d.files, "go.mod")
	for _, line := range []string{"aval run", "aval verify", "tamper"} {
		_, errw := exec(t, d, line, exitError)
		mustContain(t, line, errw, `aval: fingerprint: dependency glob "go.mod" matched no files`)
	}
}

// Files and KeyPEM hand the session's project and key to the acceptance
// test, which signs with the real CLI: the key matches throwaway.pub.
func TestFilesAndKey(t *testing.T) {
	d := New()
	f := d.Files()
	f["greet.go"] = nil
	if len(d.files["greet.go"]) == 0 {
		t.Fatal("Files must return a copy")
	}
	b, _ := pem.Decode(d.KeyPEM())
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := pem.Decode(d.files[pubFile])
	want, _ := x509.ParsePKIXPublicKey(pub.Bytes)
	if !k.(*ecdsa.PrivateKey).PublicKey.Equal(want) {
		t.Fatal("KeyPEM does not match throwaway.pub")
	}
}
