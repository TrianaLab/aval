// Package cliout writes what the aval CLI prints that the browser demo
// (internal/demo) prints too, so both say it in the same words.
package cliout

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/TrianaLab/aval/pkg/evidence"
)

// WriteReport writes r as JSON (format "json") or as the text lines of
// `aval verify` and `aval run`.
func WriteReport(w io.Writer, format string, r evidence.Report) error {
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	fmt.Fprintf(w, "%s %s %s\n", r.Outcome, r.TestID, r.Fingerprint)
	if s := r.Signer; s != nil {
		who := "key " + s.KeyID
		if s.KeyID == "" {
			who = fmt.Sprintf("%s (issuer %s)", s.Subject, s.Issuer)
		}
		fmt.Fprintf(w, "signer: %s, trust level %s\n", OneLine(who), s.TrustLevel)
	}
	if r.Reference != "" {
		fmt.Fprintf(w, "reference: %s\n", r.Reference)
	}
	if r.Reason != "" {
		fmt.Fprintf(w, "reason: %s\n", OneLine(r.Reason))
	}
	for _, rj := range r.Rejected {
		if rj.Reference == r.Reference && rj.Reason == r.Reason {
			continue // printed above as the reference and reason
		}
		fmt.Fprintf(w, "rejected: %s %s: %s\n", rj.Reference, rj.Outcome, OneLine(rj.Reason))
	}
	return nil
}

// Version is the aval version, set by the CLI, so links point at the docs
// of the version that printed them.
var Version = "dev"

// TrustModelURL explains verification failures and how to respond to them,
// in the docs of Version (latest for a dev build).
func TrustModelURL() string {
	v := strings.TrimPrefix(Version, "v")
	if v == "dev" {
		v = "latest"
	}
	return "https://aval.run/" + v + "/trust-model/"
}

// SecurityLine is the SECURITY line for n attestations of testID that
// failed signature or policy verification (invariant 4).
func SecurityLine(n int, testID string) string {
	return fmt.Sprintf("aval: SECURITY: %d attestation(s) for %s failed signature or policy verification; treat it as a security event, see %s", n, testID, TrustModelURL())
}

// Security prints SecurityLine.
func Security(w io.Writer, n int, testID string) { fmt.Fprintln(w, SecurityLine(n, testID)) }

// OneLine replaces newlines, control and other non-printable characters,
// and invalid UTF-8, with Go escapes (\n, \x1b, \u2028); everything else,
// quotes and backslashes included, stays as is. Reasons can carry text from
// an untrusted attestation; escaped, it cannot start a line of its own in a
// log, where a CI system might read it as a command, or move a terminal
// cursor.
func OneLine(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == utf8.RuneError && !strings.HasPrefix(s[i:], "\uFFFD"):
			fmt.Fprintf(&b, `\x%02x`, s[i]) // an invalid byte
		case strconv.IsPrint(r):
			b.WriteRune(r)
		default:
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
		}
	}
	return b.String()
}
