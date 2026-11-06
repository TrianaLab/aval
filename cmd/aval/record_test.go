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
	"time"

	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// recordedAt is the fixed clock fakeRecord installs; records store it in UTC.
var recordedAt = time.Date(2026, 11, 6, 12, 30, 0, 0, time.UTC)

// fakeRecord replaces publishing run records and the clock; it keeps what was published.
func fakeRecord(t *testing.T, err error) *[]evidence.RunRecord {
	t.Helper()
	p, n := pushRecord, now
	t.Cleanup(func() { pushRecord, now = p, n })
	now = func() time.Time { return recordedAt.In(time.FixedZone("CET", 3600)) }
	var got []evidence.RunRecord
	pushRecord = func(_ context.Context, repo string, rec evidence.RunRecord) (registry.Descriptor, error) {
		got = append(got, rec)
		return registry.Descriptor{Repository: repo, Digest: "sha256:cc"}, err
	}
	return &got
}

func readRecord(t *testing.T, path string) evidence.RunRecord {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := evidence.ParseRunRecord(raw)
	if err != nil {
		t.Fatal(err)
	}
	return *rec
}

// --record publishes one record per run for every outcome, and a failure to
// publish it is a warning that never changes the exit code.
func TestRunRecordsEveryOutcome(t *testing.T) {
	untrusted := &evidence.VerificationError{Reason: "untrusted signer"}
	cases := []struct {
		name     string
		cands    map[string]candidate
		lookup   error
		missing  bool // runner.command cannot start
		testExit int
		shadow   bool
		code     int
		outcome  evidence.Outcome
		reused   bool
		result   evidence.Result
	}{
		{"miss", nil, fmt.Errorf("x: %w", evidence.ErrNotFound), false, 1, false, ExitTestFailed, evidence.OutcomeMiss, false, evidence.ResultFailed},
		{"error", nil, errors.New("registry down"), false, 0, false, ExitOK, evidence.OutcomeError, false, evidence.ResultPassed},
		{"verification failure", map[string]candidate{"sha256:aa": {verifyErr: untrusted}}, nil, false, 0, false, ExitVerificationFailed, evidence.OutcomeVerificationFailed, false, evidence.ResultPassed},
		{"reused", map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner}}, nil, false, 1, false, ExitOK, evidence.OutcomeVerified, true, evidence.ResultPassed},
		{"reused failure", map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner, failed: true}}, nil, false, 0, false, ExitTestFailed, evidence.OutcomeVerified, true, evidence.ResultFailed},
		{"shadow", map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner}}, nil, false, 0, true, ExitOK, evidence.OutcomeVerified, false, evidence.ResultPassed},
		{"command cannot start", nil, fmt.Errorf("x: %w", evidence.ErrNotFound), true, 0, false, ExitError, evidence.OutcomeMiss, false, ""},
	}
	for _, tc := range cases {
		for _, pushErr := range []error{nil, errors.New("401 unauthorized")} {
			t.Run(fmt.Sprintf("%s/pushErr=%v", tc.name, pushErr), func(t *testing.T) {
				fakeCandidates(t, tc.lookup, tc.cands)
				got := fakeRecord(t, pushErr)
				argv, ran := command(t, tc.testExit, tc.missing)
				dir := t.TempDir()
				recFile := filepath.Join(dir, "record.json")
				args := []string{"run", "-f", contractWith(t, "hermetic", argv...), "--registry", "registry.example/acme/stats", "--policy", "p.yaml",
					"--result", filepath.Join(dir, "result.json"), "--record", "--record-file", recFile, "--ci-run-url", "https://ci.example/1"}
				if tc.shadow {
					args = append(args, "--shadow")
				}
				code, stdout, stderr := runCLI(args...)
				if code != tc.code || ran() != (!tc.reused && !tc.missing) {
					t.Fatalf("exit %d, want %d; stdout %q stderr %q", code, tc.code, stdout, stderr)
				}
				if len(*got) != 1 {
					t.Fatalf("published %d records, want 1", len(*got))
				}
				rec := (*got)[0]
				want := evidence.RunRecord{
					Version: evidence.RunRecordVersion, TestID: "unit", Fingerprint: rec.Fingerprint, Outcome: tc.outcome, Reused: tc.reused, Result: tc.result,
					TestDurationMs: rec.TestDurationMs, AvalVersion: version, RecordedAt: recordedAt, CIRunURL: "https://ci.example/1",
				}
				if rec != want || rec != readRecord(t, recFile) || !strings.HasPrefix(rec.Fingerprint, "sha256:") || (tc.reused && rec.TestDurationMs != 0) {
					t.Fatalf("published %+v, wrote %+v, want %+v", rec, readRecord(t, recFile), want)
				}
				if line := map[bool]string{true: "recorded registry.example/acme/stats@sha256:cc", false: "warning: run record not published"}[pushErr == nil]; !strings.Contains(stdout+stderr, line) {
					t.Fatalf("stdout %q stderr %q, want %q", stdout, stderr, line)
				}
			})
		}
	}
}

// command is a marker exiting code, or with missing one that cannot start.
func command(t *testing.T, code int, missing bool) ([]string, func() bool) {
	if missing {
		return []string{"/nonexistent/aval-test"}, func() bool { return false }
	}
	return marker(t, code)
}

// Durations: the real run's for a run, the reused attestation's as saved.
func TestRunRecordDurations(t *testing.T) {
	fakeCandidates(t, nil, map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner}})
	v := verifyEnvelope
	verifyEnvelope = func(ctx context.Context, env *evidence.Envelope, o evidence.VerifyOptions) (*evidence.Verification, error) {
		r, err := v(ctx, env, o)
		r.Predicate.FinishedAt = r.Predicate.StartedAt.Add(90_000_000_000)
		return r, err
	}
	got := fakeRecord(t, nil)
	c := contractWith(t, "hermetic", "sleep", "0.05")
	if code, _, stderr := runCLI("run", "-f", c, "--registry", "registry.example/acme/stats", "--policy", "p.yaml", "--record"); code != ExitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if code, _, stderr := runCLI("run", "-f", c, "--registry", "registry.example/acme/stats", "--policy", "p.yaml", "--record", "--shadow", "--result", filepath.Join(t.TempDir(), "r.json")); code != ExitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if r := (*got)[0]; !r.Reused || r.SavedDurationMs != 90_000 || r.TestDurationMs != 0 {
		t.Fatalf("reused record %+v", r)
	}
	if r := (*got)[1]; r.Reused || r.SavedDurationMs != 0 || r.TestDurationMs < 50 {
		t.Fatalf("shadow record %+v", r)
	}
}

// Recording is opt-in, needs a registry, and a record file that cannot be
// written is a warning, never a different exit code.
func TestRunRecordOptions(t *testing.T) {
	got := fakeRecord(t, nil)
	c := writeContract(t, "hermetic")
	result := filepath.Join(t.TempDir(), "r.json")
	if code, _, _ := runCLI("run", "-f", c, "--result", result); code != ExitOK || len(*got) != 0 {
		t.Fatalf("without --record: exit %d, %d records", code, len(*got))
	}
	if code, _, stderr := runCLI("run", "-f", c, "--record"); code != ExitUsage || !strings.Contains(stderr, "--record requires --registry") {
		t.Fatalf("--record without --registry: exit %d, stderr %q", code, stderr)
	}
	under := filepath.Join(result, "x.json") // its directory is a file
	if code, _, stderr := runCLI("run", "-f", c, "--result", result, "--record-file", under); code != ExitOK || !strings.Contains(stderr, "warning: run record not written") || len(*got) != 0 {
		t.Fatalf("unwritable record file: exit %d, stderr %q", code, stderr)
	}
}

// The record file is Aval output: inside the contract's scope it is not an
// input, so writing it does not change the fingerprint.
func TestRecordFileIsNotAnInput(t *testing.T) {
	c := writeContract(t, "hermetic")
	recFile := filepath.Join(filepath.Dir(c), "record.txt") // matches scope *.txt
	var fps []string
	for range 2 {
		if code, _, stderr := runCLI("run", "-f", c, "--result", filepath.Join(t.TempDir(), "r.json"), "--record-file", recFile); code != ExitOK {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		fps = append(fps, readRecord(t, recFile).Fingerprint)
	}
	if fps[0] != fps[1] {
		t.Fatalf("the record file changed the fingerprint: %v", fps)
	}
}

// A file at --record-file that Aval did not write is never overwritten and
// never stops the run; inside the scope it stays an input. A file shaped like
// a run record but invalid (unknown outcome, unknown field) is such a file:
// taking it for Aval output would drop an input from the fingerprint.
func TestRecordFileNeverClobbers(t *testing.T) {
	valid := `{"version":"` + evidence.RunRecordVersion + `","testId":"unit","outcome":"miss","avalVersion":"dev","recordedAt":"2026-11-06T12:30:00Z"}`
	for name, body := range map[string]string{
		"not json":        "keep me",
		"unknown outcome": strings.Replace(valid, `"miss"`, `"reused"`, 1),
		"unknown field":   strings.Replace(valid, `{`, `{"x":1,`, 1),
		"trailing data":   valid + "{}",
	} {
		t.Run(name, func(t *testing.T) {
			c := writeContract(t, "hermetic")
			recFile := filepath.Join(filepath.Dir(c), "record.txt") // matches scope *.txt
			fp := func(content string) string {
				t.Helper()
				if err := os.WriteFile(recFile, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
				report := filepath.Join(t.TempDir(), "report.json")
				code, _, stderr := runCLI("run", "-f", c, "--result", filepath.Join(t.TempDir(), "r.json"), "--report", report, "--record-file", recFile)
				if code != ExitOK || !strings.Contains(stderr, "run record not written: "+recFile+" (--record-file) exists") {
					t.Fatalf("exit %d, stderr %q", code, stderr)
				}
				if got, _ := os.ReadFile(recFile); string(got) != content {
					t.Fatalf("record file overwritten: %q", got)
				}
				var r evidence.Report
				raw, _ := os.ReadFile(report)
				if err := json.Unmarshal(raw, &r); err != nil || r.Fingerprint == "" {
					t.Fatalf("report %s: %v", raw, err)
				}
				return r.Fingerprint
			}
			if fp(body) == fp(body+" ") {
				t.Fatal("the record file is in scope but not an input of the fingerprint")
			}
		})
	}
}

// aval record publishes the record aval run wrote, unchanged.
func TestRecordCommand(t *testing.T) {
	recFile := filepath.Join(t.TempDir(), "record.json")
	if code, _, stderr := runCLI("run", "-f", writeContract(t, "hermetic"), "--result", filepath.Join(t.TempDir(), "r.json"), "--record-file", recFile); code != ExitOK {
		t.Fatalf("run: exit %d, stderr %q", code, stderr)
	}
	got := fakeRecord(t, nil)
	code, stdout, stderr := runCLI("record", "--registry", "registry.example/acme/stats", "--record-file", recFile)
	if code != ExitOK || !strings.Contains(stdout, "recorded registry.example/acme/stats@sha256:cc") || len(*got) != 1 || (*got)[0] != readRecord(t, recFile) {
		t.Fatalf("exit %d, stdout %q, stderr %q, records %+v", code, stdout, stderr, *got)
	}

	invalid := filepath.Join(t.TempDir(), "invalid.json")
	raw, _ := json.Marshal(map[string]string{"version": "x"})
	if err := os.WriteFile(invalid, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		args    []string
		pushErr error
		code    int
		stderr  string
	}{
		{"no registry", []string{"--record-file", recFile}, nil, ExitUsage, "are required"},
		{"no file", []string{"--registry", "registry.example/acme/stats"}, nil, ExitUsage, "are required"},
		{"bad registry", []string{"--registry", "r", "--record-file", recFile}, nil, ExitUsage, "not an OCI repository"},
		{"bad timeout", []string{"--registry", "registry.example/acme/stats", "--record-file", recFile, "--registry-timeout", "0s"}, nil, ExitUsage, "--registry-timeout"},
		{"missing file", []string{"--registry", "registry.example/acme/stats", "--record-file", filepath.Join(t.TempDir(), "none.json")}, nil, ExitError, "reading run record"},
		{"invalid record", []string{"--registry", "registry.example/acme/stats", "--record-file", invalid}, nil, ExitError, "version"},
		{"push error", []string{"--registry", "registry.example/acme/stats", "--record-file", recFile}, errors.New("denied"), ExitError, "publishing run record: denied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeRecord(t, tc.pushErr)
			if code, _, stderr := runCLI(append([]string{"record"}, tc.args...)...); code != tc.code || !strings.Contains(stderr, tc.stderr) {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
		})
	}
}
