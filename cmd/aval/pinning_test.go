package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/policy"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// mutableContract writes a contract of class whose scope holds an unpinned
// requirement (requirements.txt matches the *.txt scope), with argv as
// runner.command and allow as extra scope lines.
func mutableContract(t *testing.T, class, allow string, argv ...string) string {
	t.Helper()
	c := contractWith(t, class, argv...)
	if err := os.WriteFile(filepath.Join(filepath.Dir(c), "requirements.txt"), []byte("flask\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(c)
	if err := os.WriteFile(c, []byte(strings.Replace(string(body), `source: ["*.txt"]`, `source: ["*.txt"]`+allow, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	return c
}

const finding = "requirements.txt:1 (python) flask"

// A hermetic or controlled-environment contract with an unacknowledged
// mutable reference is a miss without a lookup: verify exits 4, run runs the
// test and does not publish it (invariant 3). Never a security event.
func TestMutableReferenceIsAMiss(t *testing.T) {
	for _, class := range []string{"hermetic", "controlled-environment"} {
		t.Run(class, func(t *testing.T) {
			noLookup(t)
			c := mutableContract(t, class, "", "true")
			code, stdout, stderr := runCLI("verify", "-f", c, "--registry", "reg.example/r", "--policy", "p.yaml", "-o", "json")
			var r evidence.Report
			if err := json.Unmarshal([]byte(stdout), &r); err != nil || code != ExitMiss || r.Outcome != evidence.OutcomeMiss ||
				!strings.Contains(r.Reason, "mutable references in the inputs (1)") || !strings.Contains(r.Reason, finding) || strings.Contains(stderr, "SECURITY") {
				t.Fatalf("verify: exit %d, report %+v, stderr %q", code, r, stderr)
			}

			argv, ran := marker(t, 0)
			c = mutableContract(t, class, "", argv...)
			dir := t.TempDir()
			report := filepath.Join(dir, "report.json")
			code, stdout, stderr = runCLI("run", "-f", c, "--registry", "reg.example/r", "--policy", "p.yaml", "--result", filepath.Join(dir, "r.json"), "--report", report)
			if code != ExitOK || !ran() || !strings.Contains(stdout, "mutable: "+finding) || readReport(t, report).Outcome != evidence.OutcomeMiss {
				t.Fatalf("run: exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
		})
	}
}

func TestMutableReferenceIsNotPublished(t *testing.T) {
	signed := fakePublish(t, nil, nil)
	c := mutableContract(t, "hermetic", "", "true")
	result := filepath.Join(t.TempDir(), "r.json")
	code, stdout, stderr := runCLI("run", "-f", c, "--registry", "reg.example/r", "--push", "--result", result)
	if code != ExitError || !strings.Contains(stdout, "ran unit: passed") || !strings.Contains(stderr, "result not published: mutable references") || len(*signed) != 0 {
		t.Fatalf("run --push: exit %d, stdout %q, stderr %q, signed %d", code, stdout, stderr, len(*signed))
	}
	t.Setenv(idTokenEnv, "token") // run unset it
	code, _, stderr = runCLI("push", "-f", c, "--registry", "reg.example/r", "--result", result)
	if code != ExitError || !strings.Contains(stderr, "result not published: mutable references") || len(*signed) != 0 {
		t.Fatalf("push: exit %d, stderr %q", code, stderr)
	}
}

// scope.allowMutable is the only way to accept a reference: acknowledged,
// the lookup goes ahead and a verified result is reused.
func TestAllowMutableAcknowledges(t *testing.T) {
	fakeCandidates(t, nil, map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner}})
	c := mutableContract(t, "hermetic", "\n  allowMutable: [{ref: flask, reason: vendored wheel cache}]", "true")
	code, stdout, stderr := runCLI("run", "-f", c, "--registry", "reg.example/r", "--policy", "p.yaml", "--result", filepath.Join(t.TempDir(), "r.json"))
	if code != ExitOK || !strings.Contains(stdout, "mutable: "+finding) || !strings.Contains(stdout, "(allowed: vendored wheel cache)") || !strings.Contains(stdout, "reused passed result") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// external-dependencies reports findings as information and follows the
// policy as before; non-deterministic stays never reusable.
func TestMutableReferenceInformational(t *testing.T) {
	fakeCandidates(t, nil, map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner, cacheability: "external-dependencies"}})
	loadPolicy = func(string) (*policy.Policy, error) {
		return &policy.Policy{AllowedCacheability: []string{"external-dependencies"}}, nil
	}
	c := mutableContract(t, "external-dependencies", "", "true")
	code, stdout, stderr := runCLI("run", "-f", c, "--registry", "reg.example/r", "--policy", "p.yaml", "--result", filepath.Join(t.TempDir(), "r.json"))
	if code != ExitOK || !strings.Contains(stdout, "mutable: "+finding) || !strings.Contains(stdout, "reused passed result") {
		t.Fatalf("external-dependencies: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	noLookup(t)
	code, stdout, _ = runCLI("verify", "-f", mutableContract(t, "non-deterministic", "", "true"), "--registry", "reg.example/r", "--policy", "p.yaml")
	if code != ExitMiss || !strings.Contains(stdout, "never reusable") {
		t.Fatalf("non-deterministic: exit %d, stdout %q", code, stdout)
	}
}

// --fingerprint-only keeps stdout to the digest and lists findings on
// stderr; --explain lists them on stdout after the manifest.
func TestFingerprintOnlyListsFindings(t *testing.T) {
	c := mutableContract(t, "hermetic", "", "true")
	code, stdout, stderr := runCLI("run", "--fingerprint-only", "-f", c)
	if code != ExitOK || strings.Count(stdout, "\n") != 1 || !strings.HasPrefix(stdout, "sha256:") || !strings.Contains(stderr, "mutable: "+finding) {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	code, stdout, _ = runCLI("run", "--explain", "-f", c, "--result", filepath.Join(t.TempDir(), "r.json"))
	if code != ExitOK || !strings.Contains(stdout, "source ") || !strings.Contains(stdout, "mutable: "+finding) {
		t.Fatalf("explain: exit %d, stdout %q", code, stdout)
	}
	if got := exitCode(fmt.Errorf("%w (1)", errMutable)); got != ExitMiss {
		t.Fatalf("exitCode(errMutable) = %d, want %d", got, ExitMiss)
	}
}
