package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// countPublish wraps the faked publish and counts registry writes.
func countPublish(t *testing.T) *int {
	t.Helper()
	p := publish
	t.Cleanup(func() { publish = p })
	n := 0
	publish = func(ctx context.Context, repo, fp string, a registry.Attestation) (registry.Descriptor, error) {
		n++
		return p(ctx, repo, fp, a)
	}
	return &n
}

// pushWith runs aval push for c with the given result and report files.
func pushWith(t *testing.T, c, result, report string) (int, string, string) {
	t.Helper()
	t.Setenv(idTokenEnv, "token") // aval run unsets it
	return runCLI("push", "-f", c, "--registry", "reg.example/r", "--result-file", result, "--report-file", report)
}

// reportJSON writes v as JSON to a new file and returns its path.
func reportJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// fingerprintOf is the current fingerprint of contract c.
func fingerprintOf(t *testing.T, c string) string {
	t.Helper()
	_, _, fp, err := loadAndFingerprint(context.Background(), c, "")
	if err != nil {
		t.Fatal(err)
	}
	return fp.Digest
}

// Issue #201: after a run that reused a verified result, push publishes
// nothing and exits 0, with or without a result file left over from an
// earlier run for the same fingerprint (that would be a duplicate).
func TestPushSkipsReusedResult(t *testing.T) {
	signed := fakePublish(t, nil, nil)
	writes := countPublish(t)
	c := writeContract(t, "hermetic")
	dir := t.TempDir()
	stale := filepath.Join(dir, "stale.json")
	if code, _, stderr := runCLI("run", "-f", c, "--result-file", stale); code != ExitOK {
		t.Fatalf("first run: exit %d, stderr %q", code, stderr)
	}
	fakeCandidates(t, nil, map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner}})
	result, report := filepath.Join(dir, "res.json"), filepath.Join(dir, "rep.json")
	if code, _, stderr := runCLI("run", "-f", c, "--registry", "reg.example/r", "--policy", "p.yaml", "--result-file", result, "--report-file", report); code != ExitOK || readReport(t, report).Outcome != evidence.OutcomeVerified {
		t.Fatalf("verified run: exit %d, stderr %q", code, stderr)
	}
	if _, err := os.Stat(result); !os.IsNotExist(err) {
		t.Fatalf("a reused run recorded a result: %v", err)
	}
	for _, res := range []string{result, stale} {
		code, stdout, stderr := pushWith(t, c, res, report)
		if code != ExitOK || stdout != "nothing to push: unit reused a verified result (reg.example/r@sha256:aa)\n" || len(*signed) != 0 || *writes != 0 {
			t.Fatalf("%s: exit %d, stdout %q, stderr %q, signed %d, writes %d", res, code, stdout, stderr, len(*signed), *writes)
		}
	}
}

// Issue #201, false hits: a report that does not belong to the current test
// and inputs, or cannot be read, stops push before anything is signed, even
// when the result itself matches.
func TestPushRejectsReport(t *testing.T) {
	signed := fakePublish(t, nil, nil)
	writes := countPublish(t)
	c := writeContract(t, "hermetic")
	result := filepath.Join(t.TempDir(), "res.json")
	if code, _, stderr := runCLI("run", "-f", c, "--result-file", result); code != ExitOK {
		t.Fatalf("run: exit %d, stderr %q", code, stderr)
	}
	other := otherContract(t)
	fp, otherFP := fingerprintOf(t, c), fingerprintOf(t, other)
	report := func(version, id, fp string) string {
		return reportJSON(t, evidence.Report{Version: version, Outcome: evidence.OutcomeMiss, TestID: id, Fingerprint: fp})
	}
	malformed := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(malformed, []byte(`{"version":`), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct{ report, want string }{
		"other fingerprint": {report(evidence.ReportVersion, "unit", otherFP), "run the test again"},
		"other test id":     {report(evidence.ReportVersion, "other", fp), "run the test again"},
		"other contract":    {report(evidence.ReportVersion, "other", otherFP), "run the test again"},
		"unknown version":   {report("aval.trianalab.io/verify-report/v2", "unit", fp), "version"},
		"malformed":         {malformed, "report " + malformed},
		"missing":           {filepath.Join(t.TempDir(), "none.json"), "reading the report written by 'aval run'"},
	}
	for name, tc := range cases {
		for _, outcome := range []evidence.Outcome{evidence.OutcomeMiss, evidence.OutcomeVerified} {
			p := tc.report
			r := readReportIfAny(p)
			if r == nil {
				if outcome == evidence.OutcomeVerified {
					continue // unreadable: the outcome cannot vary
				}
			} else {
				r.Outcome = outcome
				p = reportJSON(t, r)
			}
			code, stdout, stderr := pushWith(t, c, result, p)
			if code != ExitError || stdout != "" || !strings.Contains(stderr, tc.want) || len(*signed) != 0 || *writes != 0 {
				t.Errorf("%s (%s): exit %d, stdout %q, stderr %q, signed %d", name, outcome, code, stdout, stderr, len(*signed))
			}
		}
	}
}

// otherContract writes a contract with another name and other inputs.
func otherContract(t *testing.T) string {
	t.Helper()
	other := writeContract(t, "hermetic")
	body, err := os.ReadFile(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte(strings.Replace(string(body), "name: unit", "name: other", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(other), "in.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	return other
}

// Issue #201: a forged verified report for the current inputs paired with a
// result for other inputs can only skip: nothing is published.
func TestPushForgedVerifiedReport(t *testing.T) {
	signed := fakePublish(t, nil, nil)
	writes := countPublish(t)
	c, other := writeContract(t, "hermetic"), otherContract(t)
	fp := fingerprintOf(t, c)
	otherResult := filepath.Join(t.TempDir(), "res.json")
	if code, _, stderr := runCLI("run", "-f", other, "--result-file", otherResult); code != ExitOK {
		t.Fatalf("run: exit %d, stderr %q", code, stderr)
	}
	forged := reportJSON(t, evidence.Report{Version: evidence.ReportVersion, Outcome: evidence.OutcomeVerified, TestID: "unit", Fingerprint: fp, Reference: "reg.example/r@sha256:aa"})
	if code, _, stderr := pushWith(t, c, otherResult, forged); code > ExitError || len(*signed) != 0 || *writes != 0 {
		t.Fatalf("forged verified report: exit %d, stderr %q, signed %d", code, stderr, len(*signed))
	}
	// A verified report without a reference was not written by aval run.
	noRef := reportJSON(t, evidence.Report{Version: evidence.ReportVersion, Outcome: evidence.OutcomeVerified, TestID: "unit", Fingerprint: fp})
	if code, _, stderr := pushWith(t, c, otherResult, noRef); code != ExitError || !strings.Contains(stderr, "verified without a reference") || len(*signed) != 0 || *writes != 0 {
		t.Fatalf("verified report without reference: exit %d, stderr %q, signed %d", code, stderr, len(*signed))
	}
}

// readReportIfAny parses the report at p, or returns nil when p does not
// hold one.
func readReportIfAny(p string) *evidence.Report {
	raw, err := os.ReadFile(p)
	var r evidence.Report
	if err != nil || json.Unmarshal(raw, &r) != nil {
		return nil
	}
	return &r
}

// Issue #201: any outcome but verified keeps push publishing exactly what
// aval run --push publishes: passing, failing and non-deterministic results.
func TestPushPublishesAfterNonReuse(t *testing.T) {
	for _, outcome := range []evidence.Outcome{evidence.OutcomeMiss, evidence.OutcomeError, evidence.OutcomeVerificationFailed} {
		for _, tc := range []struct {
			class, command string
			code           int
			want           evidence.Result
		}{
			{"hermetic", "true", ExitOK, evidence.ResultPassed},
			{"hermetic", "false", ExitTestFailed, evidence.ResultFailed},
			{"non-deterministic", "true", ExitOK, evidence.ResultPassed},
		} {
			signed := fakePublish(t, nil, nil)
			c := contractWith(t, tc.class, tc.command)
			result := filepath.Join(t.TempDir(), "res.json")
			if code, _, stderr := runCLI("run", "-f", c, "--result-file", result); code != tc.code {
				t.Fatalf("run: exit %d, stderr %q", code, stderr)
			}
			// A field from a later additive report change is ignored.
			report := reportJSON(t, map[string]any{"version": evidence.ReportVersion, "outcome": outcome, "testId": "unit", "fingerprint": fingerprintOf(t, c), "future": "ignored"})
			code, stdout, stderr := pushWith(t, c, result, report)
			if code != ExitOK || !strings.Contains(stdout, "pushed reg.example/r@sha256:bb") || len(*signed) != 1 {
				t.Fatalf("%s %s %s: exit %d, stdout %q, stderr %q", outcome, tc.class, tc.command, code, stdout, stderr)
			}
			if p, err := evidence.Parse((*signed)[0].Predicate); err != nil || p.Result != tc.want || p.Test.Cacheability != tc.class {
				t.Fatalf("pushed %+v, %v", p, err)
			}
		}
	}
}

// Issue #201: a report kept inside the contract scope is an output, not an
// input, so pushing with it finds the fingerprint the run recorded.
func TestPushReportInScope(t *testing.T) {
	signed := fakePublish(t, nil, nil)
	c := writeContract(t, "hermetic")
	dir := filepath.Dir(c)
	result, report := filepath.Join(dir, "res.txt"), filepath.Join(dir, "rep.txt") // matched by scope.source
	if code, _, stderr := runCLI("run", "-f", c, "--result-file", result, "--report-file", report); code != ExitOK {
		t.Fatalf("run: exit %d, stderr %q", code, stderr)
	}
	if code, stdout, stderr := pushWith(t, c, result, report); code != ExitOK || len(*signed) != 1 {
		t.Fatalf("push: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}
