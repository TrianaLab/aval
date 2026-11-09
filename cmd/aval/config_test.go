package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/policy"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// Tests start from an environment without AVAL_REGISTRY and AVAL_POLICY:
// CI runs them under `aval run` with both set.
func TestMain(m *testing.M) {
	_ = os.Unsetenv(registryEnv)
	_ = os.Unsetenv(policyEnv)
	os.Exit(m.Run())
}

// flag > env > unset, and an empty value counts as unset.
func TestResolvePrecedence(t *testing.T) {
	cases := []struct {
		name      string
		flag, env string
		want      string
		fromEnv   bool
	}{
		{"flag beats env", "flag.example/r", "env.example/r", "flag.example/r", false},
		{"env without flag", "", "env.example/r", "env.example/r", true},
		{"flag without env", "flag.example/r", "", "flag.example/r", false},
		{"empty env is unset", "", "", "", false},
		{"neither", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{registryEnv: tc.env, policyEnv: tc.env}
			g := &globalFlags{registry: tc.flag, policy: tc.flag}
			g.resolve(func(k string) string { return env[k] })
			if g.registry != tc.want || g.policy != tc.want || g.fromEnv[registryEnv] != tc.fromEnv || g.fromEnv[policyEnv] != tc.fromEnv {
				t.Fatalf("registry %q policy %q fromEnv %v; want %q from env %v", g.registry, g.policy, g.fromEnv, tc.want, tc.fromEnv)
			}
		})
	}
}

// Through the CLI: what each combination does to a run. Neither set (or an
// empty variable) leaves reuse off with today's outcome, a miss that runs
// the test; a value from the environment is shown once on a config: line.
func TestRunTakesRegistryAndPolicyFromEnv(t *testing.T) {
	cases := []struct {
		name       string
		env, flags []string
		reason     string
		config     string
	}{
		{"neither: reuse off", nil, nil, "no registry configured", ""},
		{"empty env is unset", []string{"", ""}, nil, "no registry configured", ""},
		{"env registry, no policy", []string{"reg.example/env", ""}, nil, "no trust policy configured", "config: registry=reg.example/env (env AVAL_REGISTRY)\n"},
		{"env both", []string{"reg.example/env", "p.yaml"}, nil, "verified", "config: registry=reg.example/env (env AVAL_REGISTRY) policy=p.yaml (env AVAL_POLICY)\n"},
		{"flag beats env", []string{"bad registry", "missing.yaml"}, []string{"--registry", "reg.example/r", "--policy", "p.yaml"}, "verified", ""},
		{"flag registry, env policy", []string{"", "p.yaml"}, []string{"--registry", "reg.example/r"}, "verified", "config: registry=reg.example/r (flag) policy=p.yaml (env AVAL_POLICY)\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeCandidates(t, nil, map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner}})
			fake := loadPolicy // fakeCandidates restores the real one
			loadPolicy = func(p string) (*policy.Policy, error) {
				if p != "p.yaml" {
					return nil, fmt.Errorf("policy %q used", p)
				}
				return fake(p)
			}
			if tc.env != nil {
				t.Setenv(registryEnv, tc.env[0])
				t.Setenv(policyEnv, tc.env[1])
			}
			argv, _ := marker(t, 0)
			report := filepath.Join(t.TempDir(), "report.json")
			args := append([]string{"run", "-f", contractWith(t, "hermetic", argv...), "--result", filepath.Join(t.TempDir(), "r.json"), "--report", report}, tc.flags...)
			code, stdout, stderr := runCLI(args...)
			r := readReport(t, report)
			if code != ExitOK || !strings.Contains(string(r.Outcome)+" "+r.Reason, tc.reason) {
				t.Fatalf("exit %d, report %+v; stdout %q stderr %q", code, r, stdout, stderr)
			}
			if got := configLine(stdout); got != tc.config {
				t.Fatalf("config line %q, want %q", got, tc.config)
			}
		})
	}
}

// configLine is every config: line in out.
func configLine(out string) string {
	var b strings.Builder
	for _, l := range strings.SplitAfter(out, "\n") {
		if strings.HasPrefix(l, "config: ") {
			b.WriteString(l)
		}
	}
	return b.String()
}

// False hit: AVAL_POLICY naming an unreadable file never enables reuse. A
// verified candidate is there to be reused; the test still runs, and exit
// code, outcome and reason match the same path passed as --policy.
func TestUnreadableEnvPolicyNeverReuses(t *testing.T) {
	type result struct {
		code    int
		ran     bool
		outcome evidence.Outcome
		reason  string
	}
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	try := func(env bool) result {
		fakeCandidates(t, nil, map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner}})
		loadPolicy = policy.Load // the real loader: the file cannot be read
		argv, ran := marker(t, 0)
		report := filepath.Join(t.TempDir(), "report.json")
		args := []string{"run", "-f", contractWith(t, "hermetic", argv...), "--registry", "reg.example/r", "--result", filepath.Join(t.TempDir(), "r.json"), "--report", report}
		if env {
			t.Setenv(policyEnv, missing)
		} else {
			args = append(args, "--policy", missing)
		}
		code, _, _ := runCLI(args...)
		r := readReport(t, report)
		return result{code, ran(), r.Outcome, r.Reason}
	}
	flag, env := try(false), try(true)
	if env != flag || !env.ran || env.outcome != evidence.OutcomeError || env.code != ExitOK {
		t.Fatalf("env %+v, flag %+v", env, flag)
	}
}

// Exit codes and verify-report/v1 are byte-identical whether the registry and
// policy come from flags or the environment, and match the golden report.
func TestVerifyReportSameFromEnv(t *testing.T) {
	c := writeContract(t, "hermetic")
	golden, err := os.ReadFile("testdata/verify-no-policy.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{"text", "json"} {
		fc, fout, _ := runCLI("verify", "-f", c, "--registry", "reg.example/r", "-o", output)
		t.Setenv(registryEnv, "reg.example/r")
		ec, eout, estderr := runCLI("verify", "-f", c, "-o", output)
		t.Setenv(registryEnv, "")
		if ec != fc || ec != ExitMiss || eout != fout {
			t.Fatalf("%s: env exit %d %q, flag exit %d %q", output, ec, eout, fc, fout)
		}
		if estderr != "config: registry=reg.example/r (env AVAL_REGISTRY)\n" {
			t.Fatalf("%s: stderr %q", output, estderr)
		}
		if output == "json" && eout != string(golden) {
			t.Fatalf("json report:\n%s\nwant:\n%s", eout, golden)
		}
	}
}
