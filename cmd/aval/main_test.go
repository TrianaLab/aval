package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/policy"
	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/pkg/evidence"
)

func TestExitCode(t *testing.T) {
	cases := map[error]int{
		nil:                                ExitOK,
		usageError{errors.New("bad flag")}: ExitUsage,
		fmt.Errorf("x: %w", evidence.ErrNotFound):        ExitMiss,
		&evidence.VerificationError{Reason: "untrusted"}: ExitVerificationFailed,
		fmt.Errorf("x: %w", evidence.ErrNotImplemented):  ExitNotImplemented,
		errors.New("boom"): ExitError,
	}
	for err, want := range cases {
		if got := exitCode(err); got != want {
			t.Errorf("exitCode(%v) = %d, want %d", err, got, want)
		}
	}
	if ExitMiss == ExitVerificationFailed {
		t.Fatal("miss and verification failure must have distinct exit codes")
	}
}

func TestStrayArgsAreUsageErrors(t *testing.T) {
	for _, args := range [][]string{{"bogus"}, {"run", "extra"}, {"verify", "extra"}} {
		root := newRootCmd()
		root.SetArgs(args)
		if got := exitCode(root.Execute()); got != ExitUsage {
			t.Errorf("aval %v: exit %d, want %d", args, got, ExitUsage)
		}
	}
}

const testContract = `apiVersion: aval.dev/v1alpha1
kind: Test
metadata:
  name: unit
runner:
  command: ["true"]
scope:
  source: ["*.txt"]
cacheability: CLASS
`

// writeContract writes aval.yaml plus one in-scope file and returns its path.
func writeContract(t *testing.T, class string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "in.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "aval.yaml")
	if err := os.WriteFile(p, []byte(strings.Replace(testContract, "CLASS", class, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func runCLI(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestCLI(t *testing.T) {
	hermetic := writeContract(t, "hermetic")
	nondet := writeContract(t, "non-deterministic")
	missing := filepath.Join(t.TempDir(), "aval.yaml")
	noSource := writeContract(t, "hermetic")
	if err := os.Remove(filepath.Join(filepath.Dir(noSource), "in.txt")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{"help", nil, ExitOK, "Usage:", ""},
		{"bad flag", []string{"--bogus"}, ExitUsage, "", "aval: unknown flag"},
		{"fingerprint only", []string{"run", "-f", hermetic, "--fingerprint-only"}, ExitOK, "sha256:", ""},
		{"explain", []string{"run", "-f", hermetic, "--explain", "--fingerprint-only"}, ExitOK, "aval-fingerprint/v1\n", ""},
		{"run stub", []string{"run", "-f", hermetic}, ExitNotImplemented, "test unit fingerprint sha256:", "not implemented"},
		{"non-deterministic", []string{"run", "-f", nondet}, ExitNotImplemented, "cacheability non-deterministic: not reusable by default", ""},
		{"missing contract", []string{"run", "-f", missing}, ExitUsage, "", "aval:"},
		{"unprovable inputs", []string{"run", "-f", noSource}, ExitError, "", "matched no files"},
		{"push no registry", []string{"push", "-f", hermetic}, ExitUsage, "", "--registry is required"},
		{"push missing contract", []string{"push", "-f", missing, "--registry", "r"}, ExitUsage, "", "aval:"},
		{"push stub", []string{"push", "-f", hermetic, "--registry", "r"}, ExitNotImplemented, "", "not implemented"},
		{"verify bad output", []string{"verify", "-o", "yaml"}, ExitUsage, "", "--output must be text or json"},
		{"verify no registry", []string{"verify", "-f", hermetic}, ExitUsage, "", "--registry is required"},
		{"verify missing contract", []string{"verify", "-f", missing, "--registry", "r"}, ExitUsage, "", "aval:"},
		{"verify no policy", []string{"verify", "-f", hermetic, "--registry", "r"}, ExitMiss, "miss unit sha256:", "no trust policy"},
		{"verify policy stub", []string{"verify", "-f", hermetic, "--registry", "r", "--policy", "p.yaml"}, ExitNotImplemented, "error unit sha256:", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(tc.args...)
			if code != tc.code || !strings.Contains(stdout, tc.stdout) || !strings.Contains(stderr, tc.stderr) {
				t.Fatalf("aval %v: exit %d, stdout %q, stderr %q; want exit %d, stdout ~%q, stderr ~%q",
					tc.args, code, stdout, stderr, tc.code, tc.stdout, tc.stderr)
			}
		})
	}
}

func TestVerifyJSONReport(t *testing.T) {
	code, stdout, _ := runCLI("verify", "-f", writeContract(t, "hermetic"), "--registry", "r", "-o", "json")
	var r evidence.Report
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatal(err)
	}
	if code != ExitMiss || r.Version != evidence.ReportVersion || r.Outcome != evidence.OutcomeMiss || r.TestID != "unit" || r.Reason == "" {
		t.Fatalf("exit %d, report %+v", code, r)
	}
}

// fakeVerify replaces the milestone-2 seams for one test.
func fakeVerify(t *testing.T, lookupErr, fetchErr, verifyErr error) {
	t.Helper()
	l, lk, f, v := loadPolicy, lookup, fetch, verifyEnvelope
	t.Cleanup(func() { loadPolicy, lookup, fetch, verifyEnvelope = l, lk, f, v })
	loadPolicy = func(string) (*policy.Policy, error) { return &policy.Policy{}, nil }
	lookup = func(_ context.Context, repo, fp string) (registry.Descriptor, error) {
		return registry.Descriptor{Repository: repo, Digest: "sha256:feed"}, lookupErr
	}
	fetch = func(context.Context, registry.Descriptor) (*evidence.Envelope, error) {
		return &evidence.Envelope{}, fetchErr
	}
	verifyEnvelope = func(_ context.Context, _ *evidence.Envelope, o evidence.VerifyOptions) (*evidence.Verification, error) {
		if verifyErr != nil {
			return nil, verifyErr
		}
		p := &evidence.Predicate{Test: evidence.Test{ID: o.ExpectedTestID}, Result: evidence.ResultPassed}
		return &evidence.Verification{Predicate: p, Signer: evidence.Identity{Issuer: "https://issuer.example"}}, nil
	}
}

// Miss and verification failure must stay distinct all the way to the report
// and the exit code (invariant 4).
func TestVerifyOutcomes(t *testing.T) {
	cases := []struct {
		name                        string
		lookupErr, fetchErr, verErr error
		code                        int
		outcome                     evidence.Outcome
	}{
		{"verified", nil, nil, nil, ExitOK, evidence.OutcomeVerified},
		{"miss", fmt.Errorf("lookup: %w", evidence.ErrNotFound), nil, nil, ExitMiss, evidence.OutcomeMiss},
		{"fetch error", nil, errors.New("registry down"), nil, ExitError, evidence.OutcomeError},
		{"untrusted", nil, nil, &evidence.VerificationError{Reason: "untrusted signer"}, ExitVerificationFailed, evidence.OutcomeVerificationFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeVerify(t, tc.lookupErr, tc.fetchErr, tc.verErr)
			code, stdout, _ := runCLI("verify", "-f", writeContract(t, "hermetic"), "--registry", "ghcr.io/acme/x", "--policy", "p.yaml", "-o", "json")
			var r evidence.Report
			if err := json.Unmarshal([]byte(stdout), &r); err != nil {
				t.Fatal(err)
			}
			if code != tc.code || r.Outcome != tc.outcome {
				t.Fatalf("exit %d outcome %q; want %d %q", code, r.Outcome, tc.code, tc.outcome)
			}
			if tc.outcome == evidence.OutcomeVerified &&
				(r.Reference != "ghcr.io/acme/x@sha256:feed" || r.Result != evidence.ResultPassed || r.Signer == nil || r.Predicate.Test.ID != "unit") {
				t.Fatalf("verified report incomplete: %+v", r)
			}
		})
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestWriteReportPropagatesWriteErrors(t *testing.T) {
	if err := writeReport(failWriter{}, "json", evidence.Report{}); err == nil {
		t.Fatal("json write error must be returned")
	}
	root := newRootCmd()
	root.SetOut(failWriter{})
	root.SetArgs([]string{"verify", "-f", writeContract(t, "hermetic"), "--registry", "r", "-o", "json"})
	if err := root.Execute(); err == nil || errors.Is(err, evidence.ErrNotFound) {
		t.Fatalf("report write failure must win over the outcome, got %v", err)
	}
}
