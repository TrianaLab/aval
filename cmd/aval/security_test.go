package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// False hits of #195: every security event aval detects reaches the exit
// code of run and verify, and the run record, while routine non-reuse (a
// below-minimum sibling) and an undecidable sibling (error) keep their
// meaning (invariants 3, 4).
func TestSecurityEventsExit5(t *testing.T) {
	untrusted := &evidence.VerificationError{Reason: "untrusted signer"}
	trusted := candidate{trust: evidence.TrustRunner}
	vf := candidate{verifyErr: untrusted}
	security := "aval: SECURITY: 1 attestation(s) for unit failed"
	cases := []struct {
		name      string
		lookupErr error
		cands     map[string]candidate
		reused    bool
		outcome   evidence.Outcome // of the report
		record    evidence.Outcome // of the run record
		runCode   int
		verify    int
		security  string // the last stderr line starts with it; empty: no SECURITY line
	}{
		{"verified + verification-failed", nil, map[string]candidate{"sha256:aa": trusted, "sha256:ff": vf},
			false, evidence.OutcomeVerified, evidence.OutcomeVerificationFailed, ExitVerificationFailed, ExitVerificationFailed, security},
		{"verified + below minimum", nil, map[string]candidate{"sha256:aa": trusted, "sha256:bb": {trust: evidence.TrustDeveloper}},
			true, evidence.OutcomeVerified, evidence.OutcomeVerified, ExitOK, ExitOK, ""},
		{"verified + error", nil, map[string]candidate{"sha256:aa": trusted, "sha256:bb": {fetchErr: errors.New("registry down")}},
			false, evidence.OutcomeError, evidence.OutcomeError, ExitOK, ExitError, ""},
		{"flood", fmt.Errorf("listing referrers: %w for fingerprint sha256:x", registry.ErrTooManyCandidates), nil,
			false, evidence.OutcomeVerificationFailed, evidence.OutcomeVerificationFailed, ExitVerificationFailed, ExitVerificationFailed, "aval: SECURITY: more than 64 attestations for unit"},
		{"verified failing + verification-failed", nil, map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner, failed: true}, "sha256:ff": vf},
			false, evidence.OutcomeVerified, evidence.OutcomeVerificationFailed, ExitVerificationFailed, ExitVerificationFailed, security},
		{"verification-failed + verification-failed", nil, map[string]candidate{"sha256:ee": vf, "sha256:ff": vf},
			false, evidence.OutcomeVerificationFailed, evidence.OutcomeVerificationFailed, ExitVerificationFailed, ExitVerificationFailed, "aval: SECURITY: 2 attestation(s) for unit failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeCandidates(t, tc.lookupErr, tc.cands)
			policyMin(evidence.TrustRunner)
			argv, ran := marker(t, 0)
			c := contractWith(t, "hermetic", argv...)
			dir := t.TempDir()
			report, rec := filepath.Join(dir, "report.json"), filepath.Join(dir, "record.json")
			code, stdout, stderr := runCLI("run", "-f", c, "--registry", "reg.example/r", "--policy", "p.yaml",
				"--result-file", filepath.Join(dir, "result.json"), "--report-file", report, "--stats-file", rec)
			if reused := strings.Contains(stdout, "\nreused "); code != tc.runCode || reused != tc.reused || ran() == tc.reused || !lastSecurity(stderr, tc.security) {
				t.Fatalf("run: exit %d reused %v, want %d %v; stdout %q stderr %q", code, reused, tc.runCode, tc.reused, stdout, stderr)
			}
			if r := readReport(t, report); r.Outcome != tc.outcome {
				t.Fatalf("run report outcome %q, want %q", r.Outcome, tc.outcome)
			}
			checkRecord(t, rec, tc.record, tc.reused, tc.security != "")

			code, stdout, stderr = runCLI("verify", "-f", c, "--registry", "reg.example/r", "--policy", "p.yaml", "-o", "json")
			r := readReportString(t, stdout)
			if code != tc.verify || r.Outcome != tc.outcome || !lastSecurity(stderr, tc.security) {
				t.Fatalf("verify: exit %d outcome %q, want %d %q; stderr %q", code, r.Outcome, tc.verify, tc.outcome, stderr)
			}
			if tc.outcome == evidence.OutcomeVerified && r.Reference == "" {
				t.Fatalf("verify: a verified report keeps its reference: %s", stdout)
			}
		})
	}
}

// checkRecord checks the run record at path and that aval stats counts it
// under verificationFailures exactly when it is a security event.
func checkRecord(t *testing.T, path string, outcome evidence.Outcome, reused, security bool) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := evidence.ParseRunRecord(raw)
	if err != nil || got.Outcome != outcome || got.Reused != reused {
		t.Fatalf("run record %+v %v, want outcome %q reused %v", got, err, outcome, reused)
	}
	var s evidence.RunStats
	count(&s, *got)
	if (s.VerificationFailures == 1) != security {
		t.Fatalf("stats %+v: verificationFailures %d, security event %v", s, s.VerificationFailures, security)
	}
}

// --shadow beside a rejected verification-failed candidate exits 5 and still
// detects a false hit of the verified result (#195).
func TestShadowSecurityKeepsFalseHit(t *testing.T) {
	fakeCandidates(t, nil, map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner}, "sha256:ff": {verifyErr: &evidence.VerificationError{Reason: "untrusted"}}})
	argv, ran := marker(t, 1)
	code, _, stderr := runCLI("run", "-f", contractWith(t, "hermetic", argv...), "--registry", "reg.example/r", "--policy", "p.yaml", "--shadow", "--result-file", filepath.Join(t.TempDir(), "r.json"))
	if code != ExitVerificationFailed || !ran() || !strings.Contains(stderr, "FALSE HIT") || !lastSecurity(stderr, "aval: SECURITY: 1 attestation(s)") {
		t.Fatalf("exit %d; stderr %q", code, stderr)
	}
}

// lastSecurity reports whether the last stderr line starts with prefix, or,
// with an empty prefix, whether stderr holds no SECURITY line.
func lastSecurity(stderr, prefix string) bool {
	if prefix == "" {
		return !strings.Contains(stderr, "SECURITY")
	}
	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	return strings.HasPrefix(lines[len(lines)-1], prefix)
}

func readReportString(t *testing.T, s string) evidence.Report {
	t.Helper()
	p := filepath.Join(t.TempDir(), "r.json")
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return readReport(t, p)
}
