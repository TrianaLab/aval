package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/attest"
	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// contractWith writes a contract of class whose runner.command is argv.
func contractWith(t *testing.T, class string, argv ...string) string {
	t.Helper()
	p := writeContract(t, class)
	q, _ := json.Marshal(argv)
	body, _ := os.ReadFile(p)
	if err := os.WriteFile(p, []byte(strings.Replace(string(body), `["true"]`, string(q), 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// marker is a command that leaves proof that the test ran, then exits code.
// ran reports whether it ran since the last call.
func marker(t *testing.T, code int) (argv []string, ran func() bool) {
	t.Helper()
	m := filepath.Join(t.TempDir(), "ran")
	return []string{"sh", "-c", fmt.Sprintf("touch %q; exit %d", m, code)}, func() bool {
		return os.Remove(m) == nil
	}
}

// noLookup fails the test if the registry is asked.
func noLookup(t *testing.T) {
	t.Helper()
	lk := lookup
	t.Cleanup(func() { lookup = lk })
	lookup = func(context.Context, string, string) ([]registry.Descriptor, error) {
		t.Fatal("registry looked up")
		return nil, nil
	}
}

func readReport(t *testing.T, path string) evidence.Report {
	t.Helper()
	var r evidence.Report
	raw, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(raw, &r)
	}
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Every lookup outcome through aval run: only a verified result is reused;
// every other outcome runs the test and is reported, and a verification
// failure exits 5 after the run whatever the test result (invariants 3, 4).
func TestRunOutcomes(t *testing.T) {
	untrusted := &evidence.VerificationError{Reason: "untrusted signer"}
	cases := []struct {
		name     string
		cands    map[string]candidate
		lookup   error
		testExit int
		shadow   bool
		code     int
		ran      bool
		outcome  evidence.Outcome
		stdout   string
		stderr   string
	}{
		{"miss runs", nil, fmt.Errorf("x: %w", evidence.ErrNotFound), 0, false, ExitOK, true, evidence.OutcomeMiss, "ran unit: passed", ""},
		{"miss runs and fails", nil, fmt.Errorf("x: %w", evidence.ErrNotFound), 1, false, ExitTestFailed, true, evidence.OutcomeMiss, "ran unit: failed (exit 1)", ""},
		{"registry error runs", nil, errors.New("registry down"), 0, false, ExitOK, true, evidence.OutcomeError, "reason: registry down", ""},
		{"verified pass is reused", map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner}}, nil, 1, false, ExitOK, false, evidence.OutcomeVerified, "reused passed result r@sha256:aa", ""},
		{"verified failure is reused as a failure", map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner, failed: true}}, nil, 0, false, ExitTestFailed, false, evidence.OutcomeVerified, "reused failed result r@sha256:aa", "reused result failed"},
		{"verification failure runs and exits 5", map[string]candidate{"sha256:aa": {verifyErr: untrusted}}, nil, 0, false, ExitVerificationFailed, true, evidence.OutcomeVerificationFailed, "ran unit: passed", "SECURITY"},
		{"verification failure outranks a failed test", map[string]candidate{"sha256:aa": {verifyErr: untrusted}}, nil, 1, false, ExitVerificationFailed, true, evidence.OutcomeVerificationFailed, "ran unit: failed", "SECURITY"},
		{"signed cacheability differs from the contract", map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner, cacheability: "external-dependencies"}}, nil, 0, false, ExitVerificationFailed, true, evidence.OutcomeVerificationFailed, "cacheability", "SECURITY"},
		{"reuse beside an untrusted sibling still warns", map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner}, "sha256:ff": {verifyErr: untrusted}}, nil, 1, false, ExitOK, false, evidence.OutcomeVerified, "rejected: r@sha256:ff verification-failed", "warning: rejected attestation r@sha256:ff"},
		{"shadow agrees", map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner}}, nil, 0, true, ExitOK, true, evidence.OutcomeVerified, "shadow: reuse of r@sha256:aa would have been correct (passed)", ""},
		{"shadow agrees on a failure", map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner, failed: true}}, nil, 1, true, ExitTestFailed, true, evidence.OutcomeVerified, "would have been correct (failed)", ""},
		{"shadow false hit: reused pass, real failure", map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner}}, nil, 1, true, ExitError, true, evidence.OutcomeVerified, "ran unit: failed", "FALSE HIT"},
		{"shadow false hit: reused failure, real pass", map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner, failed: true}}, nil, 0, true, ExitError, true, evidence.OutcomeVerified, "ran unit: passed", "FALSE HIT"},
		{"shadow verification failure exits 5", map[string]candidate{"sha256:aa": {verifyErr: untrusted}}, nil, 0, true, ExitVerificationFailed, true, evidence.OutcomeVerificationFailed, "", "SECURITY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeCandidates(t, tc.lookup, tc.cands)
			argv, ran := marker(t, tc.testExit)
			dir := t.TempDir()
			report := filepath.Join(dir, "report.json")
			args := []string{"run", "-f", contractWith(t, "hermetic", argv...), "--registry", "r", "--policy", "p.yaml", "--result", filepath.Join(dir, "result.json"), "--report", report}
			if tc.shadow {
				args = append(args, "--shadow")
			}
			code, stdout, stderr := runCLI(args...)
			if didRun := ran(); code != tc.code || didRun != tc.ran || !strings.Contains(stdout, tc.stdout) || !strings.Contains(stderr, tc.stderr) {
				t.Fatalf("exit %d ran %v, want %d %v; stdout %q stderr %q", code, didRun, tc.code, tc.ran, stdout, stderr)
			}
			if r := readReport(t, report); r.Outcome != tc.outcome {
				t.Fatalf("report outcome %q, want %q", r.Outcome, tc.outcome)
			}
			_, err := os.Stat(filepath.Join(dir, "result.json"))
			if recorded := err == nil; recorded != tc.ran {
				t.Fatalf("result recorded %v, test ran %v", recorded, tc.ran)
			}
		})
	}
}

// Nothing is looked up when reuse is impossible or disabled; the test runs
// and the report says why (invariants 3, 7).
func TestRunWithoutLookup(t *testing.T) {
	cases := []struct {
		name, class, reason string
		extra               []string
	}{
		{"non-deterministic is never reusable", "non-deterministic", "never reusable", []string{"--registry", "r", "--policy", "p.yaml"}},
		{"no reuse", "hermetic", "--no-reuse", []string{"--registry", "r", "--policy", "p.yaml", "--no-reuse"}},
		{"no registry", "hermetic", "no registry", nil},
		{"no policy", "hermetic", "no trust policy", []string{"--registry", "r"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			noLookup(t)
			argv, ran := marker(t, 0)
			report := filepath.Join(t.TempDir(), "report.json")
			args := append([]string{"run", "-f", contractWith(t, tc.class, argv...), "--result", filepath.Join(t.TempDir(), "r.json"), "--report", report}, tc.extra...)
			if code, stdout, stderr := runCLI(args...); code != ExitOK || !ran() {
				t.Fatalf("exit %d; stdout %q stderr %q", code, stdout, stderr)
			}
			if r := readReport(t, report); r.Outcome != evidence.OutcomeMiss || !strings.Contains(r.Reason, tc.reason) {
				t.Fatalf("report %+v", r)
			}
		})
	}
}

// aval verify never looks up a non-deterministic test either.
func TestVerifyNonDeterministicIsMissWithoutLookup(t *testing.T) {
	noLookup(t)
	code, stdout, _ := runCLI("verify", "-f", writeContract(t, "non-deterministic"), "--registry", "r", "--policy", "p.yaml")
	if code != ExitMiss || !strings.Contains(stdout, "never reusable") {
		t.Fatalf("exit %d, stdout %q", code, stdout)
	}
}

// A fingerprint that cannot be computed means nothing can be looked up or
// recorded: the test runs, the reason is reported, and --push fails.
func TestRunFingerprintError(t *testing.T) {
	noLookup(t)
	argv, ran := marker(t, 0)
	c := contractWith(t, "hermetic", argv...)
	if err := os.Remove(filepath.Join(filepath.Dir(c), "in.txt")); err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(t.TempDir(), "r.json")
	code, stdout, _ := runCLI("run", "-f", c, "--registry", "r", "--policy", "p.yaml", "--result", result)
	if code != ExitOK || !ran() || !strings.Contains(stdout, "error unit") || !strings.Contains(stdout, "matched no files") {
		t.Fatalf("exit %d, stdout %q", code, stdout)
	}
	if _, err := os.Stat(result); err == nil {
		t.Fatal("result recorded without a fingerprint")
	}
	code, _, stderr := runCLI("run", "-f", c, "--registry", "r", "--push", "--result", result)
	if code != ExitError || !strings.Contains(stderr, "result not published") {
		t.Fatalf("--push: exit %d, stderr %q", code, stderr)
	}
}

func TestRunErrors(t *testing.T) {
	hermetic := writeContract(t, "hermetic")
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	under := filepath.Join(file, "x.json") // its directory is a file
	cases := []struct {
		name   string
		args   []string
		code   int
		stderr string
	}{
		{"push without registry", []string{"run", "-f", hermetic, "--push"}, ExitUsage, "--push requires --registry"},
		{"shadow and no-reuse", []string{"run", "-f", hermetic, "--shadow", "--no-reuse"}, ExitUsage, "exclude each other"},
		{"command not found", []string{"run", "-f", contractWith(t, "hermetic", "/nonexistent/aval-test"), "--result", filepath.Join(t.TempDir(), "r.json")}, ExitError, "running"},
		{"result not writable", []string{"run", "-f", hermetic, "--result", under}, ExitError, "recording result"},
		{"report not writable", []string{"run", "-f", hermetic, "--report", under}, ExitError, "writing report"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code, _, stderr := runCLI(tc.args...); code != tc.code || !strings.Contains(stderr, tc.stderr) {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
		})
	}
}

// A verification failure stays visible even when the test cannot start.
func TestRunVerificationFailureSurvivesStartError(t *testing.T) {
	fakeCandidates(t, nil, map[string]candidate{"sha256:aa": {verifyErr: &evidence.VerificationError{Reason: "untrusted"}}})
	code, _, stderr := runCLI("run", "-f", contractWith(t, "hermetic", "/nonexistent/aval-test"), "--registry", "r", "--policy", "p.yaml")
	if code != ExitVerificationFailed || !strings.Contains(stderr, "SECURITY") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}

// fakePublish replaces signing and publishing; it records what was signed.
func fakePublish(t *testing.T, signErr, pushErr error) *[]*evidence.Statement {
	t.Helper()
	s, p := sign, publish
	t.Cleanup(func() { sign, publish = s, p })
	var signed []*evidence.Statement
	sign = func(_ context.Context, st *evidence.Statement, o attest.SignOptions) (*attest.Signed, error) {
		if o.IDToken != "token" {
			t.Errorf("identity token %q not read from $%s", o.IDToken, idTokenEnv)
		}
		signed = append(signed, st)
		return &attest.Signed{Envelope: &evidence.Envelope{}}, signErr
	}
	publish = func(_ context.Context, repo, _ string, _ registry.Attestation) (registry.Descriptor, error) {
		return registry.Descriptor{Repository: repo, Digest: "sha256:bb"}, pushErr
	}
	t.Setenv(idTokenEnv, "token")
	return &signed
}

// aval run records the result and aval push publishes exactly that result,
// and only while the inputs still match it.
func TestRunThenPush(t *testing.T) {
	signed := fakePublish(t, nil, nil)
	c := contractWith(t, "hermetic", "false")
	result := filepath.Join(t.TempDir(), ".aval", "result.json")
	if code, _, stderr := runCLI("run", "-f", c, "--result", result); code != ExitTestFailed {
		t.Fatalf("run: exit %d, stderr %q", code, stderr)
	}
	code, stdout, stderr := runCLI("push", "-f", c, "--registry", "r", "--result", result)
	if code != ExitOK || !strings.Contains(stdout, "pushed r@sha256:bb") || len(*signed) != 1 {
		t.Fatalf("push: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	p, err := evidence.Parse((*signed)[0].Predicate)
	if err != nil {
		t.Fatal(err)
	}
	_, fp, _ := loadAndFingerprint(c)
	if p.Result != evidence.ResultFailed || p.Execution.ExitCode != 1 || p.Execution.Fingerprint != fp.Digest || p.Test.ID != "unit" || p.Test.Cacheability != "hermetic" {
		t.Fatalf("pushed predicate %+v", p)
	}

}

// A recorded result that does not apply to the current inputs, or cannot be
// signed, is never published.
func TestPushRejects(t *testing.T) {
	fakePublish(t, nil, nil)
	c := writeContract(t, "hermetic")
	result := filepath.Join(t.TempDir(), "result.json")
	if code, _, stderr := runCLI("run", "-f", c, "--result", result); code != ExitOK {
		t.Fatalf("run: exit %d, stderr %q", code, stderr)
	}
	// Signing fails: nothing is published.
	fakePublish(t, errors.New("no key"), nil)
	if code, _, stderr := runCLI("push", "-f", c, "--registry", "r", "--result", result); code != ExitError || !strings.Contains(stderr, "signing: no key") {
		t.Fatalf("sign error: exit %d, stderr %q", code, stderr)
	}

	// Inputs changed since the run: the recorded result no longer applies.
	if err := os.WriteFile(filepath.Join(filepath.Dir(c), "in.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCLI("push", "-f", c, "--registry", "r", "--result", result); code != ExitError || !strings.Contains(stderr, "run the test again") {
		t.Fatalf("stale result: exit %d, stderr %q", code, stderr)
	}
	// Not a predicate at all.
	if err := os.WriteFile(result, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCLI("push", "-f", c, "--registry", "r", "--result", result); code != ExitError || !strings.Contains(stderr, "recorded result") {
		t.Fatalf("invalid result: exit %d, stderr %q", code, stderr)
	}
}

func TestRunPush(t *testing.T) {
	cases := []struct {
		name             string
		signErr, pushErr error
		code             int
		out              string
	}{
		{"published", nil, nil, ExitOK, "pushed r@sha256:bb"},
		{"sign error", errors.New("no key"), nil, ExitError, "signing: no key"},
		{"publish error", nil, errors.New("denied"), ExitError, "publishing: denied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakePublish(t, tc.signErr, tc.pushErr)
			code, stdout, stderr := runCLI("run", "-f", writeContract(t, "hermetic"), "--registry", "r", "--push", "--result", filepath.Join(t.TempDir(), "r.json"))
			if code != tc.code || !strings.Contains(stdout+stderr, tc.out) {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
		})
	}
}

// End to end with real signing, the real policy file and verifier; only the
// registry is in memory. A result published by run --push is reused by the
// next run, which does not execute the test.
func TestRunPushThenReuse(t *testing.T) {
	dir := t.TempDir()
	keyFile, pub := devKey(t, dir, "dev")
	if err := os.WriteFile(filepath.Join(dir, "dev.pub"), pub, 0o600); err != nil {
		t.Fatal(err)
	}
	pol := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(pol, []byte("apiVersion: aval.dev/v1alpha1\nkind: Policy\nminTrustLevel: developer\nidentities:\n  - publicKey: dev.pub\n    trustLevel: developer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stored *registry.Attestation
	p := publish
	t.Cleanup(func() { publish = p })
	publish = func(_ context.Context, repo, _ string, a registry.Attestation) (registry.Descriptor, error) {
		stored = &a
		return registry.Descriptor{Repository: repo, Digest: "sha256:aa"}, nil
	}
	argv, ran := marker(t, 0)
	c := contractWith(t, "hermetic", argv...)
	if code, _, stderr := runCLI("run", "-f", c, "--registry", "r", "--push", "--key", keyFile, "--result", filepath.Join(dir, "r.json")); code != ExitOK || !ran() || stored == nil {
		t.Fatalf("run --push: exit %d, stderr %q", code, stderr)
	}
	serve(t, stored)
	code, stdout, stderr := runCLI("run", "-f", c, "--registry", "r", "--policy", pol, "--result", filepath.Join(dir, "r.json"))
	if code != ExitOK || ran() || !strings.Contains(stdout, "reused passed result r@sha256:aa") {
		t.Fatalf("reuse: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}
