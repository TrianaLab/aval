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

// Tests start from an environment without AVAL_REGISTRY and AVAL_POLICY: a
// developer shell may export them.
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
		unset     bool // the variables are absent, not empty
		want      string
		fromEnv   bool
	}{
		{"flag beats env", "flag.example/r", "env.example/r", false, "flag.example/r", false},
		{"env without flag", "", "env.example/r", false, "env.example/r", true},
		{"flag without env", "flag.example/r", "", true, "flag.example/r", false},
		{"empty env is unset", "", "", false, "", false},
		{"neither", "", "", true, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{registryEnv: tc.env, policyEnv: tc.env}
			if tc.unset {
				env = map[string]string{}
			}
			g := &globalFlags{registry: tc.flag, policy: tc.flag, usesPolicy: true}
			if err := g.resolve(func(k string) string { return env[k] }); err != nil {
				t.Fatal(err)
			}
			fromEnv := func(k string) bool { return g.source[k] == "env "+k }
			if g.registry != tc.want || g.policy != tc.want || fromEnv(registryEnv) != tc.fromEnv || fromEnv(policyEnv) != tc.fromEnv {
				t.Fatalf("registry %q policy %q source %v; want %q from env %v", g.registry, g.policy, g.source, tc.want, tc.fromEnv)
			}
		})
	}
}

// The registry: --registry > AVAL_REGISTRY > the policy's registry > none
// (#209), and the config: line names the source that won. Only a command
// that uses the policy falls back to it.
func TestRegistryPrecedenceWithPolicy(t *testing.T) {
	cases := []struct {
		name, flag, env, policyReg string
		usesPolicy                 bool
		want, config               string
	}{
		{"flag", "flag.example/r", "env.example/r", "pol.example/r", true, "flag.example/r", ""},
		{"env", "", "env.example/r", "pol.example/r", true, "env.example/r", "config: registry=env.example/r (env AVAL_REGISTRY) policy=p.yaml (flag)"},
		{"policy", "", "", "pol.example/r", true, "pol.example/r", "config: registry=pol.example/r (policy p.yaml) policy=p.yaml (flag)"},
		{"none", "", "", "", true, "", ""},
		{"policy not used", "", "", "pol.example/r", false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := loadPolicy
			t.Cleanup(func() { loadPolicy = l })
			loadPolicy = func(p string) (*policy.Policy, error) {
				if p != "p.yaml" {
					return nil, fmt.Errorf("policy %q used", p)
				}
				return &policy.Policy{Registry: tc.policyReg}, nil
			}
			g := &globalFlags{registry: tc.flag, usesPolicy: tc.usesPolicy}
			if tc.usesPolicy {
				g.policy = "p.yaml"
			}
			if err := g.resolve(func(k string) string { return map[string]string{registryEnv: tc.env}[k] }); err != nil {
				t.Fatal(err)
			}
			if g.registry != tc.want || g.configLine() != tc.config {
				t.Fatalf("registry %q, config %q; want %q, %q", g.registry, g.configLine(), tc.want, tc.config)
			}
		})
	}
}

// A policy registry with credentials exits 2 before anything runs, even
// when a flag names the registry, and never prints the secret. The test is
// not run.
func TestPolicyRegistryCredentialsExit2(t *testing.T) {
	dir := t.TempDir()
	pol := filepath.Join(dir, "policy.yaml")
	body := "apiVersion: aval.trianalab.io/v1\nkind: Policy\nminTrustLevel: runner\nregistry: user:s3cret@ghcr.io/acme/x\nidentities:\n  - issuer: https://issuer.example\n    subjectRegexp: ci\n    trustLevel: runner\n"
	if err := os.WriteFile(pol, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	argv, ran := marker(t, 0)
	c := contractWith(t, "hermetic", argv...)
	for _, args := range [][]string{
		{"run", "-f", c, "--policy", pol, "--result-file", filepath.Join(dir, "r.json")},
		{"run", "-f", c, "--policy", pol, "--registry", "reg.example/r", "--result-file", filepath.Join(dir, "r.json")},
		{"verify", "-f", c, "--policy", pol},
	} {
		code, stdout, stderr := runCLI(args...)
		if code != ExitUsage || strings.Contains(stdout+stderr, "s3cret") || !strings.Contains(stderr, "credentials") {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
	if ran() {
		t.Error("runner.command ran")
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
		policyReg  string
	}{
		{"neither: reuse off", nil, nil, "reuse off: no registry (set AVAL_REGISTRY or --registry)", "", ""},
		{"empty env is unset", []string{"", ""}, nil, "reuse off: no registry", "", ""},
		{"env registry, no policy", []string{"reg.example/env", ""}, nil, "reuse off: no trust policy (set AVAL_POLICY or --policy)", "config: registry=reg.example/env (env AVAL_REGISTRY)\n", ""},
		{"env both", []string{"reg.example/env", "p.yaml"}, nil, "verified", "config: registry=reg.example/env (env AVAL_REGISTRY) policy=p.yaml (env AVAL_POLICY)\n", ""},
		{"flag beats env", []string{"bad registry", "missing.yaml"}, []string{"--registry", "reg.example/r", "--policy", "p.yaml"}, "verified", "", ""},
		{"flag registry, env policy", []string{"", "p.yaml"}, []string{"--registry", "reg.example/r"}, "verified", "config: registry=reg.example/r (flag) policy=p.yaml (env AVAL_POLICY)\n", ""},
		{"policy registry", []string{"", "p.yaml"}, nil, "verified", "config: registry=pol.example/r (policy p.yaml) policy=p.yaml (env AVAL_POLICY)\n", "pol.example/r"},
		{"env beats policy registry", []string{"reg.example/env", "p.yaml"}, nil, "verified", "config: registry=reg.example/env (env AVAL_REGISTRY) policy=p.yaml (env AVAL_POLICY)\n", "pol.example/r"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeCandidates(t, nil, map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner}})
			fake := loadPolicy // fakeCandidates restores the real one
			loadPolicy = func(p string) (*policy.Policy, error) {
				if p != "p.yaml" {
					return nil, fmt.Errorf("policy %q used", p)
				}
				pol, err := fake(p)
				pol.Registry = tc.policyReg
				return pol, err
			}
			if tc.env != nil {
				t.Setenv(registryEnv, tc.env[0])
				t.Setenv(policyEnv, tc.env[1])
			}
			argv, _ := marker(t, 0)
			report := filepath.Join(t.TempDir(), "report.json")
			args := append([]string{"run", "-f", contractWith(t, "hermetic", argv...), "--result-file", filepath.Join(t.TempDir(), "r.json"), "--report-file", report}, tc.flags...)
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
		args := []string{"run", "-f", contractWith(t, "hermetic", argv...), "--registry", "reg.example/r", "--result-file", filepath.Join(t.TempDir(), "r.json"), "--report-file", report}
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
	for output, ext := range map[string]string{"text": "txt", "json": "json"} {
		// Generated once by the verify command before #163 (epic/13-cli).
		golden, err := os.ReadFile("testdata/verify-no-policy.golden." + ext)
		if err != nil {
			t.Fatal(err)
		}
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
		if eout != string(golden) {
			t.Fatalf("%s report:\n%s\nwant:\n%s", output, eout, golden)
		}
	}
}

// Commands that do not use the policy neither take --policy nor show
// AVAL_POLICY: a config: line names only settings the command uses.
func TestPolicyOnlyWhereUsed(t *testing.T) {
	t.Setenv(policyEnv, "p.yaml")
	for _, cmd := range []string{"stats", "record", "push"} {
		_, _, stderr := runCLI(cmd, "--registry", "reg.example/r")
		if strings.Contains(stderr, "config:") || strings.Contains(stderr, "p.yaml") {
			t.Errorf("%s: stderr %q", cmd, stderr)
		}
		if code, _, stderr := runCLI(cmd, "--policy", "p.yaml"); code != ExitUsage || !strings.Contains(stderr, "unknown flag: --policy") {
			t.Errorf("%s --policy: exit %d, stderr %q", cmd, code, stderr)
		}
	}
	for _, cmd := range []string{"version", "upgrade"} {
		if code, _, _ := runCLI(cmd, "--registry", "reg.example/r"); code != ExitUsage {
			t.Errorf("%s --registry: exit %d, want usage", cmd, code)
		}
	}
}

// runner.command sees neither the registry nor the trust policy: a nested
// aval in the test must not reuse or publish against the operator's
// settings.
func TestChildEnvWithholdsTrustSettings(t *testing.T) {
	t.Setenv(registryEnv, "reg.example/r")
	t.Setenv(policyEnv, "p.yaml")
	t.Setenv(idTokenEnv, "token")
	for _, kv := range childEnv("") {
		if k, _, _ := strings.Cut(kv, "="); k == registryEnv || k == policyEnv || k == idTokenEnv {
			t.Errorf("child env has %s", kv)
		}
	}
}
