package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/TrianaLab/aval/pkg/evidence"
)

// probeContract writes a contract whose toolchain declares tool: want and
// probes it with probe; argv is runner.command.
func probeContract(t *testing.T, toolchain string, probe []string, argv ...string) string {
	t.Helper()
	p := contractWith(t, "hermetic", argv...)
	q, _ := json.Marshal(probe)
	body, _ := os.ReadFile(p)
	env := fmt.Sprintf("environment:\n  toolchain: %s\n  probe: {tool: %s}\ncacheability:", toolchain, q)
	if err := os.WriteFile(p, []byte(strings.Replace(string(body), "cacheability:", env, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHasVersion(t *testing.T) {
	for _, tc := range []struct {
		want, out string
		ok        bool
	}{
		{"3.13", "Python 3.13.1", true},
		{"3.13", "Python 3.13", true},
		{"3.1", "Python 3.13.1", false}, // prefix trap
		{"3.13", "Python 3.130", false},
		{"13", "Python 3.13.1", false},
		{"1.25", "go version go1.25.3 darwin/arm64", true},
		{"20", "v20.11.0", true},
		{"", "Python 3.13", false},
		{"3.13", "", false},
	} {
		if got := hasVersion(tc.out, tc.want); got != tc.ok {
			t.Errorf("hasVersion(%q, %q) = %v", tc.out, tc.want, got)
		}
	}
}

// A matching probe lets the run go on; its output lands in the predicate
// under probe.<key>, and a toolchain key named os or arch never replaces the
// real values.
func TestProbeMatchRecordsObserved(t *testing.T) {
	c := probeContract(t, `{tool: "3.13", os: plan9, arch: z80}`, []string{"sh", "-c", "echo Python 3.13.1"}, "true")
	res := filepath.Join(t.TempDir(), "r.json")
	if code, stdout, stderr := runCLI("run", "-f", c, "--result", res); code != ExitOK {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	raw, err := os.ReadFile(res)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evidence.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	env := p.Execution.Environment
	if env["probe.tool"] != "Python 3.13.1" || env["tool"] != "3.13" || env["os"] != runtime.GOOS || env["arch"] != runtime.GOARCH {
		t.Fatalf("environment %v", env)
	}
}

// Every probe failure: no lookup, the test runs, nothing is recorded or
// published, exit 1, and the reason names the probe, declared and observed.
func TestProbeFailureRunsAndPublishesNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		probe []string
		want  string
	}{
		"mismatch":    {[]string{"sh", "-c", "echo Python 3.12.4"}, `declared "3.13", observed "Python 3.12.4"`},
		"prefix trap": {[]string{"sh", "-c", "echo Python 3.130"}, `observed "Python 3.130"`},
		"not found":   {[]string{"aval-no-such-probe"}, "aval-no-such-probe"},
		"non-zero":    {[]string{"sh", "-c", "echo Python 3.13.1; exit 2"}, "exit status 2"},
		"timeout":     {[]string{"sleep", "5"}, "timed out after 1s"},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "timeout" {
				old := probeTimeout
				t.Cleanup(func() { probeTimeout = old })
				probeTimeout = time.Second
			}
			noLookup(t)
			signed := fakePublish(t, nil, nil)
			argv, ran := marker(t, 0)
			c := probeContract(t, `{tool: "3.13"}`, tc.probe, argv...)
			dir := t.TempDir()
			res, rep := filepath.Join(dir, "r.json"), filepath.Join(dir, "rep.json")
			code, stdout, stderr := runCLI("run", "-f", c, "--registry", "reg.example/r", "--push", "--result", res, "--report", rep)
			r := readReport(t, rep)
			if code != ExitError || !ran() || len(*signed) != 0 || r.Outcome != evidence.OutcomeError ||
				!strings.Contains(r.Reason, "environment.probe tool") || !strings.Contains(r.Reason, tc.want) || !strings.Contains(stderr, "result not recorded") {
				t.Fatalf("exit %d, ran %v, signed %d, report %+v\nstdout %q\nstderr %q", code, ran(), len(*signed), r, stdout, stderr)
			}
			if _, err := os.Stat(res); !os.IsNotExist(err) {
				t.Fatalf("result recorded: %v", err)
			}
		})
	}
}

// A consumer never executes contract-supplied commands: verify, push and
// --fingerprint-only never run a probe.
func TestProbeNeverRunsOutsideRun(t *testing.T) {
	m := filepath.Join(t.TempDir(), "probed")
	c := probeContract(t, `{tool: "3.13"}`, []string{"touch", m}, "true")
	fakeCandidates(t, evidence.ErrNotFound, nil)
	key, _ := devKey(t, t.TempDir(), "k")
	for _, args := range [][]string{
		{"run", "-f", c, "--fingerprint-only"},
		{"run", "-f", c, "--fingerprint-only", "--explain"},
		{"verify", "-f", c, "--registry", "reg.example/r"},
		{"push", "-f", c, "--registry", "reg.example/r", "--key", key, "--result", filepath.Join(t.TempDir(), "none.json")},
	} {
		if code, _, stderr := runCLI(args...); code == ExitUsage {
			t.Fatalf("%v: usage error %q", args, stderr)
		}
		if _, err := os.Stat(m); !os.IsNotExist(err) {
			t.Fatalf("%v ran the probe", args)
		}
	}
	// The marker works: run does execute it.
	runCLI("run", "-f", c, "--result", filepath.Join(t.TempDir(), "r.json"))
	if _, err := os.Stat(m); err != nil {
		t.Fatalf("run did not probe: %v", err)
	}
}
