package cliout

import (
	"strings"
	"testing"
	"time"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// The plain run lines, byte for byte. The whole flow per outcome is in the
// goldens under testdata, written by cmd/aval TestPlainRunGoldens.
func TestRunLines(t *testing.T) {
	var out, errw strings.Builder
	w := Run{Out: &out, Err: &errw}
	miss := evidence.Report{Outcome: evidence.OutcomeMiss, TestID: "unit", Fingerprint: "sha256:ab", Reason: "reuse off: no registry"}
	w.Decision(miss, contract.ExternalDependencies)
	w.Decision(miss, contract.NonDeterministic)
	w.Decision(miss, contract.Hermetic)
	miss.Reason = "cacheability non-deterministic is never reusable"
	w.Decision(miss, contract.NonDeterministic) // the reason says it
	miss.Reason = "reg.example/r: cacheability external-dependencies"
	w.Decision(miss, contract.ExternalDependencies) // remote text cannot hide the note
	w.ReuseOff("registry down\x1b[2J")
	w.Start("unit", []string{"sh", "-c", "go test\n./..."})
	w.End("unit", evidence.ResultFailed, 1, 1234*time.Millisecond)
	w.Reused(evidence.ResultPassed)
	w.Pushed("reg.example/r@sha256:bb")
	w.Summary(Summary{Test: "unit", Result: evidence.ResultPassed, Took: 42 * time.Millisecond, Pushed: true, ReuseOff: true, Security: true})
	w.Summary(Summary{Test: "unit", Reused: true, Result: evidence.ResultFailed, Saved: 90 * time.Second})
	w.Summary(Summary{Test: "unit", Reused: true, Result: evidence.ResultPassed})

	want := "cacheability external-dependencies: not reusable by default (the policy may allow it)\n" +
		"miss unit sha256:ab\nreason: reuse off: no registry\n" +
		"cacheability non-deterministic: never reusable\n" +
		"miss unit sha256:ab\nreason: reuse off: no registry\n" +
		"miss unit sha256:ab\nreason: reuse off: no registry\n" +
		"miss unit sha256:ab\nreason: cacheability non-deterministic is never reusable\n" +
		"cacheability external-dependencies: not reusable by default (the policy may allow it)\n" +
		"miss unit sha256:ab\nreason: reg.example/r: cacheability external-dependencies\n" +
		"--- run unit: sh -c go test\\n./...\n" +
		"--- end unit: failed (exit 1) in 1.2s\nran unit: failed (exit 1)\n" +
		"reused passed result\npushed reg.example/r@sha256:bb\n" +
		"summary: unit ran: passed in 42ms; result pushed; reuse was off; SECURITY\n" +
		"summary: unit reused: failed; saved 1m30s\n" +
		"summary: unit reused: passed\n"
	if out.String() != want {
		t.Fatalf("stdout\n%s\nwant\n%s", out.String(), want)
	}
	if want := "aval: warning: reuse off: registry down\\x1b[2J\n"; errw.String() != want {
		t.Fatalf("stderr %q, want %q", errw.String(), want)
	}
}

func TestTrustModelURL(t *testing.T) {
	defer func(v string) { Version = v }(Version)
	for v, want := range map[string]string{"dev": "https://aval.run/latest/trust-model/", "0.6.0": "https://aval.run/0.6.0/trust-model/", "v0.6.0": "https://aval.run/0.6.0/trust-model/", "0.6.0-snapshot+abc": "https://aval.run/latest/trust-model/"} {
		if Version = v; TrustModelURL() != want {
			t.Errorf("version %s: %s, want %s", v, TrustModelURL(), want)
		}
	}
}
