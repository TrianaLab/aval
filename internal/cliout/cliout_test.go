package cliout

import (
	"errors"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/pkg/evidence"
)

// OneLine escapes only what could start a line or move the cursor; quotes
// and backslashes stay readable.
func TestOneLine(t *testing.T) {
	cases := map[string]string{
		`unknown command "version"`: `unknown command "version"`,
		`C:\path ünï 中`:             `C:\path ünï 中`,
		"a\nb\r\x1b[31m\t\x00":      `a\nb\r\x1b[31m\t\x00`,
		"line\u2028sep\u0085\u202e": `line\u2028sep\u0085\u202e`,
		"bad\xffutf8 \uFFFD":        `bad\xffutf8 ` + "\uFFFD",
	}
	for in, want := range cases {
		if got := OneLine(in); got != want {
			t.Errorf("OneLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTextReportListsRejected(t *testing.T) {
	var b strings.Builder
	r := evidence.Report{Outcome: evidence.OutcomeVerified, Rejected: []evidence.Rejection{{Reference: "reg.example/r@sha256:ff", Outcome: evidence.OutcomeVerificationFailed, Reason: "untrusted"}}}
	if err := WriteReport(&b, "text", r); err != nil || !strings.Contains(b.String(), "rejected: reg.example/r@sha256:ff verification-failed: untrusted\n") {
		t.Fatalf("text report %q, %v", b.String(), err)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestWriteReportPropagatesWriteErrors(t *testing.T) {
	if err := WriteReport(failWriter{}, "json", evidence.Report{}); err == nil {
		t.Fatal("json write error must be returned")
	}
}

func TestTextReport(t *testing.T) {
	r := evidence.Report{
		Outcome: evidence.OutcomeVerificationFailed, TestID: "unit", Fingerprint: "sha256:ab",
		Signer:    &evidence.Identity{Subject: "ci\nforged", Issuer: "https://issuer.example", TrustLevel: evidence.TrustRunner},
		Reference: "reg.example/r@sha256:ff", Reason: "bad\nsig",
		Rejected: []evidence.Rejection{{Reference: "reg.example/r@sha256:ff", Outcome: evidence.OutcomeVerificationFailed, Reason: "bad\nsig"}},
	}
	want := "verification-failed unit sha256:ab\nsigner: ci\\nforged (issuer https://issuer.example), trust level runner\n" +
		"reference: reg.example/r@sha256:ff\nreason: bad\\nsig\n"
	var b strings.Builder
	if err := WriteReport(&b, "text", r); err != nil || b.String() != want {
		t.Fatalf("got %q, want %q (%v)", b.String(), want, err)
	}
	b.Reset()
	r.Signer = &evidence.Identity{KeyID: "kid", TrustLevel: evidence.TrustDeveloper}
	if _ = WriteReport(&b, "text", r); !strings.Contains(b.String(), "signer: key kid, trust level developer\n") {
		t.Fatalf("key signer: %q", b.String())
	}
}

func TestSecurity(t *testing.T) {
	var b strings.Builder
	Security(&b, 2, "unit")
	if want := "aval: SECURITY: 2 attestation(s) for unit failed signature or policy verification; treat it as a security event, see " + TrustModelURL() + "\n"; b.String() != want {
		t.Fatalf("got %q", b.String())
	}
}

func TestFloodLine(t *testing.T) {
	got := FloodLine(errors.New("more than 64 attestations"), "unit")
	if want := "aval: SECURITY: more than 64 attestations for unit, none verified: possibly a flooding attempt that hides an untrusted one; the test runs, see " + TrustModelURL(); got != want {
		t.Fatalf("got %q", got)
	}
}

// Each problem gets an indented line of its own, escaped: a value from the
// file cannot start a line in a log.
func TestProblems(t *testing.T) {
	var b strings.Builder
	Problems(&b, "aval.yaml: invalid contract:", errors.Join(errors.New("line 2: scope.sources: unknown field; did you mean source?"), errors.New("forged\n::error::x \x1b[31m")))
	Problems(&b, "one:", errors.New("single"))
	want := "aval: aval.yaml: invalid contract:\n  line 2: scope.sources: unknown field; did you mean source?\n  forged\\n::error::x \\x1b[31m\naval: one:\n  single\n"
	if b.String() != want {
		t.Fatalf("got %q, want %q", b.String(), want)
	}
}
