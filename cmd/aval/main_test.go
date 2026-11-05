package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
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
		{"explain then run", []string{"run", "-f", hermetic, "--explain", "--result", filepath.Join(t.TempDir(), "r.json")}, ExitOK, "ran unit: passed", ""},
		{"run without registry runs the test", []string{"run", "-f", hermetic, "--result", filepath.Join(t.TempDir(), "r.json")}, ExitOK, "miss unit sha256:", ""},
		{"non-deterministic", []string{"run", "-f", nondet, "--result", filepath.Join(t.TempDir(), "r.json")}, ExitOK, "cacheability non-deterministic: not reusable by default", ""},
		{"missing contract", []string{"run", "-f", missing}, ExitUsage, "", "aval:"},
		{"unprovable inputs", []string{"run", "-f", noSource, "--fingerprint-only"}, ExitError, "", "matched no files"},
		{"unprovable inputs explained", []string{"run", "-f", noSource, "--explain"}, ExitError, "", "matched no files"},
		{"push no registry", []string{"push", "-f", hermetic}, ExitUsage, "", "--registry is required"},
		{"push missing contract", []string{"push", "-f", missing, "--registry", "r"}, ExitUsage, "", "aval:"},
		{"push without a recorded result", []string{"push", "-f", hermetic, "--registry", "r", "--result", filepath.Join(t.TempDir(), "none.json")}, ExitError, "", "recorded by 'aval run'"},
		{"verify bad output", []string{"verify", "-o", "yaml"}, ExitUsage, "", "--output must be text or json"},
		{"verify no registry", []string{"verify", "-f", hermetic}, ExitUsage, "", "--registry is required"},
		{"verify missing contract", []string{"verify", "-f", missing, "--registry", "r"}, ExitUsage, "", "aval:"},
		{"verify no policy", []string{"verify", "-f", hermetic, "--registry", "r"}, ExitMiss, "miss unit sha256:", "no trust policy"},
		{"verify unreadable policy", []string{"verify", "-f", hermetic, "--registry", "r", "--policy", "p.yaml"}, ExitError, "error unit sha256:", "p.yaml"},
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

// fakeVerify replaces the milestone-2 seams for one test with a single
// candidate.
func fakeVerify(t *testing.T, lookupErr, fetchErr, verifyErr error) {
	t.Helper()
	fakeCandidates(t, lookupErr, map[string]candidate{"sha256:feed": {fetchErr: fetchErr, verifyErr: verifyErr, trust: evidence.TrustRunner}})
}

type candidate struct {
	fetchErr, verifyErr error
	trust               evidence.TrustLevel
	failed              bool   // verifies with result failed instead of passed
	cacheability        string // signed class; default hermetic
}

// fakeCandidates makes lookup return the given candidates, sorted by digest,
// each fetching and verifying as described.
func fakeCandidates(t *testing.T, lookupErr error, cands map[string]candidate) {
	t.Helper()
	l, lk, f, v := loadPolicy, lookup, fetch, verifyEnvelope
	t.Cleanup(func() { loadPolicy, lookup, fetch, verifyEnvelope = l, lk, f, v })
	loadPolicy = func(string) (*policy.Policy, error) { return &policy.Policy{}, nil }
	lookup = func(_ context.Context, repo, fp string) ([]registry.Descriptor, error) {
		if lookupErr != nil {
			return nil, lookupErr
		}
		var ds []registry.Descriptor
		for _, dg := range slices.Sorted(maps.Keys(cands)) {
			ds = append(ds, registry.Descriptor{Repository: repo, Digest: dg})
		}
		return ds, nil
	}
	fetch = func(_ context.Context, d registry.Descriptor) (*registry.Attestation, error) {
		if err := cands[d.Digest].fetchErr; err != nil {
			return nil, err
		}
		// The digest travels in the envelope so verify knows the candidate.
		return &registry.Attestation{Envelope: &evidence.Envelope{Payload: []byte(d.Digest)}}, nil
	}
	verifyEnvelope = func(_ context.Context, env *evidence.Envelope, o evidence.VerifyOptions) (*evidence.Verification, error) {
		c := cands[string(env.Payload)]
		if c.verifyErr != nil {
			return nil, c.verifyErr
		}
		p := &evidence.Predicate{Test: evidence.Test{ID: o.ExpectedTestID, Cacheability: cmp.Or(c.cacheability, "hermetic")}, Result: evidence.ResultPassed}
		if c.failed {
			p.Result = evidence.ResultFailed
		}
		return &evidence.Verification{Predicate: p, Signer: evidence.Identity{Issuer: "https://issuer.example", TrustLevel: c.trust}}, nil
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

func TestTextReportListsRejected(t *testing.T) {
	var b strings.Builder
	r := evidence.Report{Outcome: evidence.OutcomeVerified, Rejected: []evidence.Rejection{{Reference: "r@sha256:ff", Outcome: evidence.OutcomeVerificationFailed, Reason: "untrusted"}}}
	if err := writeReport(&b, "text", r); err != nil || !strings.Contains(b.String(), "rejected: r@sha256:ff verification-failed: untrusted\n") {
		t.Fatalf("text report %q, %v", b.String(), err)
	}
}

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

// Several attestations per fingerprint: the policy picks the verified one with
// the highest trust; rejected candidates are listed in the report and on
// stderr, and a verification failure is never hidden behind a plain error or
// a verified sibling (invariant 4). A candidate that could not be evaluated
// blocks reuse of any sibling (invariant 3).
func TestVerifyPicksAmongCandidates(t *testing.T) {
	untrusted := &evidence.VerificationError{Reason: "untrusted signer"}
	down := errors.New("registry down")
	// What registry.Fetch returns for a malformed or tampered manifest.
	malformed := fmt.Errorf("registry.Fetch r@sha256:aa: %w", &evidence.VerificationError{Reason: "malformed attestation: not an Aval attestation"})
	cases := []struct {
		name    string
		lookup  error
		cands   map[string]candidate
		code    int
		outcome evidence.Outcome
		ref     string
		result  evidence.Result
		// rejected is the outcome of each rejected candidate, in lookup order.
		rejected []evidence.Outcome
	}{
		{"highest trust wins", nil, map[string]candidate{
			"sha256:aa": {trust: evidence.TrustDeveloper},
			"sha256:bb": {trust: evidence.TrustIsolated},
			"sha256:cc": {trust: evidence.TrustRunner},
		}, ExitOK, evidence.OutcomeVerified, "r@sha256:bb", evidence.ResultPassed, nil},
		{"tie keeps lookup order", nil, map[string]candidate{
			"sha256:aa": {trust: evidence.TrustRunner},
			"sha256:bb": {trust: evidence.TrustRunner},
		}, ExitOK, evidence.OutcomeVerified, "r@sha256:aa", evidence.ResultPassed, nil},
		{"tie prefers a failed result", nil, map[string]candidate{
			"sha256:aa": {trust: evidence.TrustRunner},
			"sha256:bb": {trust: evidence.TrustRunner, failed: true},
			"sha256:cc": {trust: evidence.TrustRunner},
		}, ExitOK, evidence.OutcomeVerified, "r@sha256:bb", evidence.ResultFailed, nil},
		{"higher trust pass beats lower trust fail", nil, map[string]candidate{
			"sha256:aa": {trust: evidence.TrustDeveloper, failed: true},
			"sha256:bb": {trust: evidence.TrustIsolated},
		}, ExitOK, evidence.OutcomeVerified, "r@sha256:bb", evidence.ResultPassed, nil},
		{"later untrusted push does not replace a trusted result", nil, map[string]candidate{
			"sha256:aa": {trust: evidence.TrustRunner},
			"sha256:ff": {verifyErr: untrusted},
		}, ExitOK, evidence.OutcomeVerified, "r@sha256:aa", evidence.ResultPassed, []evidence.Outcome{evidence.OutcomeVerificationFailed}},
		{"error on a sibling fails closed", nil, map[string]candidate{
			"sha256:aa": {fetchErr: down},
			"sha256:bb": {trust: evidence.TrustRunner},
		}, ExitError, evidence.OutcomeError, "r@sha256:aa", "", []evidence.Outcome{evidence.OutcomeError}},
		{"unreachable trusted failure does not let a pass through", nil, map[string]candidate{
			"sha256:aa": {trust: evidence.TrustRunner},
			"sha256:bb": {fetchErr: down},
		}, ExitError, evidence.OutcomeError, "r@sha256:bb", "", []evidence.Outcome{evidence.OutcomeError}},
		{"verify error on a sibling fails closed", nil, map[string]candidate{
			"sha256:aa": {trust: evidence.TrustIsolated},
			"sha256:bb": {verifyErr: down},
		}, ExitError, evidence.OutcomeError, "r@sha256:bb", "", []evidence.Outcome{evidence.OutcomeError}},
		{"verification failure outranks a sibling error beside a verified one", nil, map[string]candidate{
			"sha256:aa": {fetchErr: down},
			"sha256:bb": {trust: evidence.TrustRunner},
			"sha256:cc": {verifyErr: untrusted},
		}, ExitVerificationFailed, evidence.OutcomeVerificationFailed, "r@sha256:cc", "", []evidence.Outcome{evidence.OutcomeError, evidence.OutcomeVerificationFailed}},
		{"untrusted push does not become a hit", nil, map[string]candidate{
			"sha256:aa": {fetchErr: down},
			"sha256:ff": {verifyErr: untrusted},
		}, ExitVerificationFailed, evidence.OutcomeVerificationFailed, "r@sha256:ff", "", []evidence.Outcome{evidence.OutcomeError, evidence.OutcomeVerificationFailed}},
		{"verification failure outranks an earlier error", nil, map[string]candidate{
			"sha256:aa": {fetchErr: down},
			"sha256:bb": {verifyErr: untrusted},
			"sha256:cc": {verifyErr: down},
		}, ExitVerificationFailed, evidence.OutcomeVerificationFailed, "r@sha256:bb", "", []evidence.Outcome{evidence.OutcomeError, evidence.OutcomeVerificationFailed, evidence.OutcomeError}},
		{"only errors", nil, map[string]candidate{
			"sha256:aa": {fetchErr: down},
		}, ExitError, evidence.OutcomeError, "r@sha256:aa", "", []evidence.Outcome{evidence.OutcomeError}},
		{"only malformed candidates are a verification failure, not a miss", nil, map[string]candidate{
			"sha256:aa": {fetchErr: malformed},
			"sha256:bb": {fetchErr: malformed},
		}, ExitVerificationFailed, evidence.OutcomeVerificationFailed, "r@sha256:aa", "", []evidence.Outcome{evidence.OutcomeVerificationFailed, evidence.OutcomeVerificationFailed}},
		{"malformed candidate is reported beside a verified one", nil, map[string]candidate{
			"sha256:aa": {fetchErr: malformed},
			"sha256:bb": {trust: evidence.TrustRunner},
		}, ExitOK, evidence.OutcomeVerified, "r@sha256:bb", evidence.ResultPassed, []evidence.Outcome{evidence.OutcomeVerificationFailed}},
		{"empty answer is a miss", nil, map[string]candidate{}, ExitMiss, evidence.OutcomeMiss, "", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeCandidates(t, tc.lookup, tc.cands)
			code, stdout, stderr := runCLI("verify", "-f", writeContract(t, "hermetic"), "--registry", "r", "--policy", "p.yaml", "-o", "json")
			var r evidence.Report
			if err := json.Unmarshal([]byte(stdout), &r); err != nil {
				t.Fatal(err)
			}
			if code != tc.code || r.Outcome != tc.outcome || r.Reference != tc.ref || r.Result != tc.result {
				t.Fatalf("exit %d outcome %q ref %q result %q; want %d %q %q %q", code, r.Outcome, r.Reference, r.Result, tc.code, tc.outcome, tc.ref, tc.result)
			}
			var got []evidence.Outcome
			for _, rj := range r.Rejected {
				if !strings.HasPrefix(rj.Reference, "r@sha256:") || rj.Reason == "" {
					t.Fatalf("incomplete rejection %+v", rj)
				}
				got = append(got, rj.Outcome)
			}
			if !slices.Equal(got, tc.rejected) {
				t.Fatalf("rejected outcomes %v, want %v; report %s", got, tc.rejected, stdout)
			}
			if n := strings.Count(stderr, "warning: rejected attestation"); n != len(tc.rejected) {
				t.Fatalf("%d rejection warnings, want %d; stderr %q", n, len(tc.rejected), stderr)
			}
		})
	}
}
