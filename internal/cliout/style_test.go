package cliout

import (
	"strings"
	"testing"
	"time"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// The styled lines, without colour, byte for byte. The whole flow per
// outcome is in the goldens under testdata, written by cmd/aval
// TestStyledRunGoldens.
func TestStyledLines(t *testing.T) {
	var out, errw strings.Builder
	w := Run{Out: &out, Err: &errw, Styled: true, ErrStyled: true}
	fp := "sha256:" + strings.Repeat("ab", 32)
	w.Decision(evidence.Report{Outcome: evidence.OutcomeMiss, TestID: "unit", Fingerprint: fp, Reason: "reuse off: no registry"}, contract.ExternalDependencies)
	w.Decision(evidence.Report{Outcome: evidence.OutcomeError, TestID: "unit", Fingerprint: fp, Reason: "registry down"}, contract.Hermetic)
	w.Note("config: registry=r (env AVAL_REGISTRY)")
	w.Note("")
	w.ReuseOff("registry down")
	w.Start("unit", []string{"go", "test"})
	w.End("unit", evidence.ResultPassed, 0, 1234*time.Millisecond)
	w.End("unit", evidence.ResultFailed, 1, time.Second)
	w.Reused(evidence.ResultPassed)
	w.Reused(evidence.ResultFailed)
	w.Pushed("reg.example/r@" + fp)
	for _, s := range []Summary{
		{Test: "unit", Result: evidence.ResultPassed, Took: time.Second},
		{Test: "unit", Result: evidence.ResultPassed, Took: time.Second, Pushed: true},
		{Test: "unit", Result: evidence.ResultPassed, Took: time.Second, ReuseOff: true},
		{Test: "unit", Result: evidence.ResultFailed, Took: time.Second},
		{Test: "unit", Reused: true, Result: evidence.ResultPassed, Saved: 90 * time.Second},
		{Test: "unit", Reused: true, Result: evidence.ResultPassed},
		{Test: "unit", Result: evidence.ResultPassed, Took: time.Second, Pushed: true, NeverReusable: true},
		{Test: "unit", Result: evidence.ResultPassed, Took: time.Second, Verified: true},
		{Test: "unit", Result: evidence.ResultPassed, Took: time.Second, Security: true, Flood: true},
	} {
		w.Summary(s)
	}
	w.Security = true
	w.End("unit", evidence.ResultPassed, 0, time.Second)
	w.Reused(evidence.ResultPassed)
	w.Pushed("reg.example/r@" + fp)
	w.Summary(Summary{Test: "unit", Result: evidence.ResultPassed, Security: true})
	w.SecurityBlock(SecurityLine(1, "unit"))

	want := "· cacheability external-dependencies: not reusable by default (the policy may allow it)\n" +
		"⚠ miss unit sha256:abababab\n  reason    reuse off: no registry\n" +
		"⚠ error unit sha256:abababab\n  reason    registry down\n" +
		"· config: registry=r (env AVAL_REGISTRY)\n" +
		"· run unit: go test\n" +
		"✓ unit passed (exit 0) in 1.2s\n✗ unit failed (exit 1) in 1s\n" +
		"↺ reused passed result\n✗ reused failed result\n✓ pushed reg.example/r@sha256:abababab\n" +
		"unit passed in 1s; aval push publishes it for the next run.\n" +
		"unit passed in 1s; published for the next run with these inputs.\n" +
		"unit passed in 1s with reuse off; see the reason above.\n" +
		"unit failed in 1s; fix it, then aval run again.\n" +
		"unit: reused a verified passed result, saved 1m30s; nothing ran.\n" +
		"unit: reused a verified passed result; nothing ran.\n" +
		"unit passed in 1s; cacheability non-deterministic is never reused.\n" +
		"unit passed in 1s; a verified result exists, it was not reused.\n" +
		"✗ SECURITY: more attestations for unit than Aval lists, none verified; possibly a flooding attempt, the test ran.\n" +
		"· unit passed (exit 0) in 1s\n· reused passed result\n· pushed reg.example/r@sha256:abababab\n" +
		"✗ SECURITY: an attestation for unit failed verification; treat it as a security event, not a flaky test.\n"
	if out.String() != want {
		t.Fatalf("stdout\n%s\nwant\n%s", out.String(), want)
	}
	if want := "⚠ reuse off: registry down\n✗ " + strings.TrimPrefix(SecurityLine(1, "unit"), "aval: ") + "\n"; errw.String() != want {
		t.Fatalf("stderr %q, want %q", errw.String(), want)
	}
	if strings.ContainsRune(out.String()+errw.String(), '\x1b') {
		t.Fatal("ESC byte without colour")
	}
	// stderr that is not a terminal keeps the plain, greppable prefixes.
	errw.Reset()
	w.ErrStyled, w.Color = false, true
	w.ReuseOff("registry down")
	w.SecurityBlock(SecurityLine(1, "unit"))
	if want := "aval: warning: reuse off: registry down\n" + SecurityLine(1, "unit") + "\n"; errw.String() != want {
		t.Fatalf("plain stderr %q, want %q", errw.String(), want)
	}
}

// Colour wraps whole lines in SGR codes; a step without a colour gets none.
func TestStyledColor(t *testing.T) {
	var out strings.Builder
	w := Run{Out: &out, Styled: true, Color: true}
	w.step("", glyphNeutral, "plain")
	w.Pushed("r@sha256:" + strings.Repeat("0", 64))
	w.Decision(evidence.Report{Outcome: evidence.OutcomeVerified, TestID: "unit", Fingerprint: "sha256:ab"}, contract.Hermetic)
	if want := "· plain\n" + sgrGreen + "✓ pushed r@sha256:00000000" + sgrReset + "\n" + sgrCyan + "↺ verified unit sha256:ab" + sgrReset + "\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

// Attestation text with ESC and U+2028 cannot move the cursor or break the
// alignment of the labelled lines: each detail is one line, its value
// starts at the same column, and the only ESC bytes are Aval's own SGR codes.
func TestStyledReportAlignment(t *testing.T) {
	var out strings.Builder
	w := Run{Out: &out, Styled: true, Color: true}
	evil := "x\x1b[2J\x1b[1A\u2028\ny"
	w.Decision(evidence.Report{
		Outcome: evidence.OutcomeVerificationFailed, TestID: "unit" + evil, Fingerprint: "sha256:ab",
		Signer:    &evidence.Identity{Subject: evil, Issuer: evil},
		Reference: "reg.example/r@sha256:ff", Reason: evil,
		Rejected: []evidence.Rejection{{Reference: "reg.example/r@sha256:" + strings.Repeat("e", 64), Outcome: evidence.OutcomeVerificationFailed, Reason: evil}},
	}, contract.Hermetic)
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("%d lines, want 5:\n%s", len(lines), out.String())
	}
	for _, l := range lines {
		plain := strings.NewReplacer(sgrReset, "", sgrBold, "", sgrDim, "", sgrRed, "").Replace(l)
		if strings.ContainsAny(plain, "\x1b\u2028\n") {
			t.Fatalf("raw control in %q", l)
		}
		if strings.HasPrefix(plain, "  ") && (plain[11] != ' ' || plain[12] == ' ' || strings.TrimSpace(plain[2:11]) == "") {
			t.Fatalf("value not at column 12 in %q", plain)
		}
	}
	if !strings.Contains(out.String(), "REJECTED") || !strings.Contains(out.String(), strings.Repeat("e", 64)) || !strings.HasPrefix(lines[0], sgrBold+sgrRed+"✗ verification-failed") {
		t.Fatalf("no ✗ or REJECTED:\n%s", out.String())
	}
}
