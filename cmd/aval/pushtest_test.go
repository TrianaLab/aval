package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/pkg/evidence"
)

// Issue #210: aval push --test publishes only a result and a report for
// that test and the inputs push recomputes from the checkout; it skips a
// reused result, refuses a report that records a security event with exit 5
// and never pushes a non-deterministic test. Nothing is signed or written
// unless the row says so.
func TestPushTestBindsReport(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{
		".git/HEAD": "ref: refs/heads/main\n",
		"in.txt":    "in\n",
		"aval.yaml": `apiVersion: aval.trianalab.io/v1
kind: TestSet
defaults: {scope: {source: [in.txt]}, cacheability: hermetic}
tests:
- {metadata: {name: a}, runner: {command: ["true"]}, environment: {env: [AVAL_T210]}}
- {metadata: {name: b}, runner: {command: ["true"]}}
- {metadata: {name: nd}, runner: {command: ["true"]}, cacheability: non-deterministic}
`,
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	t.Setenv("AVAL_T210", "1")
	for _, n := range []string{"a", "b", "nd"} {
		if code, _, stderr := runCLI("run", "--test", n); code != ExitOK {
			t.Fatalf("run %s: exit %d, stderr %q", n, code, stderr)
		}
	}
	fpA := func(env string) string {
		t.Setenv("AVAL_T210", env)
		_, _, fp, err := loadAndFingerprint(context.Background(), "aval.yaml", "a")
		if err != nil {
			t.Fatal(err)
		}
		return fp.Digest
	}
	// A report that matches the changed env (written by hand), beside the
	// result recorded with the old value.
	changed := reportJSON(t, evidence.Report{Version: evidence.ReportVersion, Outcome: evidence.OutcomeMiss, TestID: "a", Fingerprint: fpA("2")})
	fp := fpA("1")
	report := func(outcome evidence.Outcome, ref string, rejected ...evidence.Outcome) string {
		r := evidence.Report{Version: evidence.ReportVersion, Outcome: outcome, TestID: "a", Fingerprint: fp, Reference: ref}
		for _, o := range rejected {
			r.Rejected = append(r.Rejected, evidence.Rejection{Reference: "reg.example/r@sha256:cc", Outcome: o, Reason: "x"})
		}
		return reportJSON(t, r)
	}
	b := filepath.Join(".aval", "b")
	cases := []struct {
		name   string
		env    string // AVAL_T210 at push time
		args   []string
		code   int
		out    string // in stdout when code is 0, else in stderr
		pushed bool
	}{
		{"own result and report", "1", []string{"--test", "a"}, ExitOK, "pushed reg.example/r@sha256:bb", true},
		{"result of another test", "1", []string{"--test", "a", "--result-file", filepath.Join(b, "result.json")}, ExitError, `recorded result is for test "b"`, false},
		{"report of another test", "1", []string{"--test", "a", "--report-file", filepath.Join(b, "report.json")}, ExitError, `report is for test "b"`, false},
		{"result and report of another test", "1", []string{"--test", "a", "--result-file", filepath.Join(b, "result.json"), "--report-file", filepath.Join(b, "report.json")}, ExitError, `report is for test "b"`, false},
		// False hit: a declared env value changed between run and push.
		{"declared env changed", "2", []string{"--test", "a"}, ExitError, "run the test again", false},
		{"declared env changed, fresh report", "2", []string{"--test", "a", "--report-file", changed}, ExitError, "recorded result is for test", false},
		{"declared env unset", "", []string{"--test", "a"}, ExitError, "run the test again", false},
		{"reused", "1", []string{"--test", "a", "--report-file", report(evidence.OutcomeVerified, "reg.example/r@sha256:aa")}, ExitOK, "nothing to push: a reused a verified result", false},
		{"verification-failed", "1", []string{"--test", "a", "--report-file", report(evidence.OutcomeVerificationFailed, "")}, ExitVerificationFailed, "failed verification: nothing pushed", false},
		{"verified beside a rejected attestation", "1", []string{"--test", "a", "--report-file", report(evidence.OutcomeVerified, "reg.example/r@sha256:aa", evidence.OutcomeVerificationFailed)}, ExitVerificationFailed, "records 1 attestation(s) for a", false},
		{"miss beside a rejected attestation", "1", []string{"--test", "a", "--report-file", report(evidence.OutcomeMiss, "", evidence.OutcomeMiss, evidence.OutcomeVerificationFailed)}, ExitVerificationFailed, "failed verification", false},
		{"rejected below the minimum is routine", "1", []string{"--test", "a", "--report-file", report(evidence.OutcomeMiss, "", evidence.OutcomeMiss)}, ExitOK, "pushed", true},
		{"non-deterministic", "1", []string{"--test", "nd"}, ExitOK, "nothing to push: nd is non-deterministic, never reusable", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			signed := fakePublish(t, nil, nil)
			writes := countPublish(t)
			t.Setenv("AVAL_T210", tc.env)
			if tc.env == "" {
				if err := os.Unsetenv("AVAL_T210"); err != nil {
					t.Fatal(err)
				}
			}
			code, stdout, stderr := runCLI(append([]string{"push", "--registry", "reg.example/r"}, tc.args...)...)
			got := map[bool]string{true: stdout, false: stderr}[code == ExitOK]
			n := map[bool]int{true: 1}[tc.pushed]
			if code != tc.code || !strings.Contains(got, tc.out) || len(*signed) != n || *writes != n {
				t.Fatalf("exit %d, stdout %q, stderr %q, signed %d, writes %d", code, stdout, stderr, len(*signed), *writes)
			}
		})
	}
}
