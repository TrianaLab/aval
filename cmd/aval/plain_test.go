package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// The plain output of aval run for every outcome, with and without --push
// and --shadow, for a passing and a failing test, against goldens in
// internal/cliout/testdata (the renderer's package; -update rewrites them).
// Whatever the golden says, every case also holds the plain contract: no
// ESC byte, the outcome-first line, an error never reported as a miss, and a
// verification failure exits 5 with the SECURITY line last on stderr
// (invariant 4).
func TestPlainRunGoldens(t *testing.T) {
	for _, oc := range runOutcomes() {
		t.Run(oc.name, func(t *testing.T) {
			var got strings.Builder
			for _, push := range []bool{false, true} {
				for _, shadow := range []bool{false, true} {
					for _, failed := range []bool{false, true} {
						plainCase(t, &got, oc, push, shadow, failed)
					}
				}
			}
			checkGolden(t, "run-"+oc.name+".golden", got.String())
		})
	}
}

// runOutcomes are the lookup outcomes of the run goldens, plain and styled.
func runOutcomes() []plainOutcome {
	untrusted := &evidence.VerificationError{Reason: "untrusted signer \x1b[31m forged"}
	return []plainOutcome{
		{"verified", evidence.OutcomeVerified, "hermetic", func(t *testing.T, failed bool) {
			fakeCandidates(t, nil, map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner, failed: failed, took: 1500 * time.Millisecond}})
		}},
		{"miss", evidence.OutcomeMiss, "hermetic", func(t *testing.T, _ bool) {
			fakeCandidates(t, fmt.Errorf("reg.example/r: %w", evidence.ErrNotFound), nil)
		}},
		{"error", evidence.OutcomeError, "hermetic", func(t *testing.T, _ bool) { fakeCandidates(t, errors.New("registry down"), nil) }},
		{"verification-failed", evidence.OutcomeVerificationFailed, "hermetic", func(t *testing.T, _ bool) {
			fakeCandidates(t, nil, map[string]candidate{"sha256:ff": {verifyErr: untrusted}})
		}},
		// Verified beside a rejected forged candidate: SECURITY, not reused,
		// the test runs and exits 5 (#195).
		{"verified-with-forged", evidence.OutcomeVerified, "hermetic", func(t *testing.T, failed bool) {
			fakeCandidates(t, nil, map[string]candidate{
				"sha256:aa": {trust: evidence.TrustRunner, failed: failed, took: 1500 * time.Millisecond},
				"sha256:ff": {verifyErr: untrusted},
			})
		}},
		// Flooding (#135, #195): nothing verified, a verification failure, the test runs.
		{"flood", evidence.OutcomeVerificationFailed, "hermetic", func(t *testing.T, _ bool) {
			fakeCandidates(t, fmt.Errorf("listing referrers: %w", registry.ErrTooManyCandidates), nil)
		}},
		{"non-deterministic", evidence.OutcomeMiss, "non-deterministic", func(t *testing.T, _ bool) { noLookup(t) }},
		{"reuse-off", evidence.OutcomeMiss, "hermetic", func(t *testing.T, _ bool) { noLookup(t) }}, // plainCase drops --policy
	}
}

// checkGolden compares got with the golden name in internal/cliout/testdata
// (the renderer's package; -update rewrites it).
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	golden := filepath.Join("..", "..", "internal", "cliout", "testdata", name)
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil || string(want) != got {
		t.Fatalf("output differs from %s (go test ./cmd/aval -run 'RunGoldens' -update); err %v, got:\n%s", golden, err, got)
	}
}

// The SECURITY line is the last stderr line whatever else failed after it
// (here the push), also beside a verified sibling, which is not reused.
func TestSecurityLineIsLast(t *testing.T) {
	untrusted := &evidence.VerificationError{Reason: "untrusted signer"}
	cases := []struct {
		name  string
		cands map[string]candidate
		code  int
		err   string
	}{
		{"push fails after a verification failure", map[string]candidate{"sha256:ff": {verifyErr: untrusted}}, ExitVerificationFailed, "aval: publishing: registry gone\n"},
		{"push fails beside an untrusted sibling", map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner}, "sha256:ff": {verifyErr: untrusted}}, ExitVerificationFailed, "aval: publishing: registry gone\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeCandidates(t, nil, tc.cands)
			fakePublish(t, nil, errors.New("registry gone"))
			dir := t.TempDir()
			code, stdout, stderr := runCLI("run", "-f", contractWith(t, "hermetic", "true"), "--registry", "reg.example/r", "--policy", "p.yaml", "--push", "--result-file", filepath.Join(dir, "r.json"))
			security := "aval: SECURITY: 1 attestation(s) for unit failed signature or policy verification; treat it as a security event, see https://aval.run/latest/trust-model/\n"
			if code != tc.code || stderr != security+tc.err+security || !strings.HasSuffix(stdout, "; SECURITY\n") {
				t.Fatalf("exit %d; stdout %q stderr %q", code, stdout, stderr)
			}
		})
	}
}

// Flooding the fingerprint (#135) is a security event in `aval run` too: no
// reuse-off warning, SECURITY in the summary and as the last stderr line,
// and exit 5 after the test ran (#195).
func TestRunFloodIsSecurity(t *testing.T) {
	fakeCandidates(t, fmt.Errorf("listing referrers: %w", registry.ErrTooManyCandidates), nil)
	code, stdout, stderr := runCLI("run", "-f", contractWith(t, "hermetic", "true"), "--registry", "reg.example/r", "--policy", "p.yaml", "--result-file", filepath.Join(t.TempDir(), "r.json"))
	flood := "aval: SECURITY: more than 64 attestations for unit, none verified: possibly a flooding attempt that hides an untrusted one; the test runs, see https://aval.run/latest/trust-model/\n"
	if code != ExitVerificationFailed || stderr != flood+flood || !regexp.MustCompile(`\nsummary: unit ran: passed in [^;]+; SECURITY\n$`).MatchString(stdout) {
		t.Fatalf("exit %d; stdout %q stderr %q", code, stdout, stderr)
	}
}

// Inputs that cannot be fingerprinted turn reuse off with a warning; the
// test runs and the exit code stays the test's (invariant 3).
func TestFingerprintErrorWarns(t *testing.T) {
	c := writeContract(t, "hermetic")
	if err := os.Remove(filepath.Join(filepath.Dir(c), "in.txt")); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCLI("run", "-f", c, "--result-file", filepath.Join(t.TempDir(), "r.json"))
	if code != ExitOK || !strings.HasPrefix(stderr, "aval: warning: reuse off: fingerprint: ") || !strings.Contains(stdout, "\nsummary: unit ran: passed in ") || !strings.HasSuffix(stdout, "; reuse was off\n") {
		t.Fatalf("exit %d; stdout %q stderr %q", code, stdout, stderr)
	}
}

// plainOutcome is a lookup outcome of TestPlainRunGoldens: setup fakes the
// registry for a passing or failing result.
type plainOutcome struct {
	name    string
	outcome evidence.Outcome
	class   string
	setup   func(t *testing.T, failed bool)
}

// plainCase runs one case of TestPlainRunGoldens, appends its output to got
// and checks the plain contract.
func plainCase(t *testing.T, got *strings.Builder, oc plainOutcome, push, shadow, failed bool) {
	t.Helper()
	name, code, stdout, stderr, r := runOutcome(t, got, oc, push, shadow, failed)
	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	switch {
	case strings.ContainsRune(stdout+stderr, '\x1b'):
		t.Errorf("%s: ESC byte in plain output", name)
	case r.Outcome != oc.outcome:
		t.Errorf("%s: outcome %q, want %q", name, r.Outcome, oc.outcome)
	case !strings.Contains("\n"+stdout, fmt.Sprintf("\n%s unit %s\n", r.Outcome, r.Fingerprint)):
		t.Errorf("%s: no outcome-first line in %q", name, stdout)
	case security(r, stderr) && !strings.HasPrefix(lines[len(lines)-1], "aval: SECURITY: "),
		r.Outcome == evidence.OutcomeVerificationFailed && code != ExitVerificationFailed:
		t.Errorf("%s: exit %d, last stderr line %q", name, code, lines[len(lines)-1])
	}
}

// security reports whether a run with report r and stderr met a security
// event: a verification failure, a rejected forged sibling or a flood.
func security(r evidence.Report, stderr string) bool {
	return r.Outcome == evidence.OutcomeVerificationFailed || strings.Contains(stderr, "aval: SECURITY: ")
}

// runOutcome runs one case of the run goldens and appends its output, with
// durations as D, to got. It returns the case name, the exit code, stdout,
// stderr and the report file.
func runOutcome(t *testing.T, got *strings.Builder, oc plainOutcome, push, shadow, failed bool) (name string, code int, stdout, stderr string, r evidence.Report) {
	t.Helper()
	took := regexp.MustCompile(` in [0-9.]+(ns|µs|ms|s)\b`)
	oc.setup(t, failed)
	fakePublish(t, nil, nil)
	exit := map[bool]int{false: 0, true: 1}[failed]
	dir := t.TempDir()
	report := filepath.Join(dir, "report.json")
	args := []string{"run", "-f", contractWith(t, oc.class, "sh", "-c", fmt.Sprintf("echo test output; exit %d", exit)),
		"--registry", "reg.example/r", "--result-file", filepath.Join(dir, "result.json"), "--report-file", report}
	if oc.name != "reuse-off" {
		args = append(args, "--policy", "p.yaml")
	}
	if push {
		args = append(args, "--push")
	}
	if shadow {
		args = append(args, "--shadow")
	}
	code, stdout, stderr = runCLI(args...)
	name = fmt.Sprintf("push=%v shadow=%v failed=%v", push, shadow, failed)
	fmt.Fprintf(got, "=== %s\nexit %d\n--- stdout\n%s--- stderr\n%s", name, code, took.ReplaceAllString(stdout, " in D"), stderr)
	return name, code, stdout, stderr, readReport(t, report)
}
