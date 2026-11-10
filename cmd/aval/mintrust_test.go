package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/pkg/evidence"
)

var allLevels = []evidence.TrustLevel{evidence.TrustDeveloper, evidence.TrustRunner, evidence.TrustIsolated}

// #214, false-hit table over every (policy minimum, --min-trust-level) pair
// and every signer level: the flag only raises the minimum. A value below
// the policy's exits 2 before any lookup, from the flag or the environment;
// otherwise a signer below the raised minimum is a miss (exit 4, never 5)
// and one at or above it verifies.
func TestMinTrustLevelPairs(t *testing.T) {
	type pair struct {
		pol, raise, signer evidence.TrustLevel
		viaEnv             bool
	}
	var cases []pair
	for _, pol := range allLevels {
		for _, raise := range allLevels {
			for _, signer := range allLevels {
				cases = append(cases, pair{pol, raise, signer, false}, pair{pol, raise, signer, true})
			}
		}
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("policy %s/raise %s/signer %s/env %v", tc.pol, tc.raise, tc.signer, tc.viaEnv), func(t *testing.T) {
			fakeCandidates(t, nil, map[string]candidate{"sha256:aa": {trust: tc.signer}})
			policyMin(tc.pol)
			args := []string{"verify", "-f", writeContract(t, "hermetic"), "--registry", "reg.example/r", "--policy", "p.yaml", "-o", "json"}
			if tc.viaEnv {
				t.Setenv(minTrustEnv, tc.raise.String())
			} else {
				args = append(args, "--min-trust-level", tc.raise.String())
			}
			code, stdout, stderr := runCLI(args...)
			want := ExitOK
			switch {
			case tc.raise < tc.pol:
				want = ExitUsage
			case tc.signer < tc.raise:
				want = ExitMiss
			}
			if code != want || strings.Contains(stderr, "SECURITY") || (want == ExitUsage) != strings.Contains(stderr, "can only raise it") {
				t.Fatalf("exit %d, want %d; stdout %q stderr %q", code, want, stdout, stderr)
			}
			if want != ExitUsage {
				checkNoTrustField(t, stdout)
			}
		})
	}
}

// checkNoTrustField fails if the verify report gained a top-level trust
// field: the report keeps its frozen shape.
func checkNoTrustField(t *testing.T, stdout string) {
	t.Helper()
	var r map[string]any
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatal(err)
	}
	for k := range r {
		if strings.Contains(strings.ToLower(k), "trust") {
			t.Fatalf("report gained %q: %s", k, stdout)
		}
	}
}

// With the minimum raised to runner, aval run does not reuse a developer-
// signed pass: a miss, the failing test runs, no SECURITY line.
func TestRunMinTrustLevelRaised(t *testing.T) {
	fakeCandidates(t, nil, map[string]candidate{"sha256:aa": {trust: evidence.TrustDeveloper}})
	policyMin(evidence.TrustDeveloper)
	argv, ran := marker(t, 1)
	dir := t.TempDir()
	report := filepath.Join(dir, "report.json")
	t.Setenv(minTrustEnv, "runner")
	code, stdout, stderr := runCLI("run", "-f", contractWith(t, "hermetic", argv...), "--registry", "reg.example/r", "--policy", "p.yaml", "--result-file", filepath.Join(dir, "result.json"), "--report-file", report)
	if code != ExitTestFailed || !ran() || strings.Contains(stderr, "SECURITY") || !strings.Contains(stdout+stderr, "min-trust-level=runner (env "+minTrustEnv+")") {
		t.Fatalf("exit %d ran %v; stdout %q stderr %q", code, ran(), stdout, stderr)
	}
	if r := readReport(t, report); r.Outcome != evidence.OutcomeMiss || len(r.Rejected) != 1 || !strings.Contains(r.Rejected[0].Reason, "policy requires at least runner") {
		t.Fatalf("report %+v", r)
	}
}

// An unknown level exits 2 on run and verify, with or without a policy,
// and the test does not run.
func TestMinTrustLevelUnknown(t *testing.T) {
	noLookup(t)
	argv, ran := marker(t, 0)
	c := contractWith(t, "hermetic", argv...)
	for _, args := range [][]string{
		{"run", "-f", c, "--min-trust-level", "admin"},
		{"run", "-f", c, "--min-trust-level", "Runner", "--policy", "p.yaml"},
		{"verify", "-f", c, "--registry", "reg.example/r", "--min-trust-level", "0"},
	} {
		if code, _, stderr := runCLI(args...); code != ExitUsage || !strings.Contains(stderr, "--min-trust-level") {
			t.Errorf("%v: exit %d stderr %q", args, code, stderr)
		}
	}
	t.Setenv(minTrustEnv, "root")
	if code, _, stderr := runCLI("run", "-f", c); code != ExitUsage || !strings.Contains(stderr, "must be developer, runner or isolated") {
		t.Errorf("env: exit %d stderr %q", code, stderr)
	}
	if ran() {
		t.Error("runner.command ran")
	}
}
