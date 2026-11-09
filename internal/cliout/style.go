package cliout

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/TrianaLab/aval/pkg/evidence"
)

// Styled output (Epic 13, style A "Calm") is for a human at a terminal: one
// line per step behind a glyph, 8-hex digests, one closing sentence. Raw
// ANSI, no dependency. Without Color it is the same text without a single
// ESC byte (NO_COLOR, the browser demo); the glyphs and the words SECURITY
// and REJECTED carry the signal on their own.
const (
	sgrReset  = "\x1b[0m"
	sgrBold   = "\x1b[1m"
	sgrDim    = "\x1b[2m"
	sgrRed    = "\x1b[31m"
	sgrGreen  = "\x1b[32m"
	sgrYellow = "\x1b[33m"
	sgrCyan   = "\x1b[36m"
)

// The glyphs of style A.
const (
	glyphDone    = "✓"
	glyphReused  = "↺"
	glyphFailed  = "✗" // also SECURITY
	glyphWarn    = "⚠" // reuse is off
	glyphNeutral = "·"
)

// paint wraps s in sgr when colour is on.
func (w Run) paint(sgr, s string) string {
	if !w.Color || sgr == "" {
		return s
	}
	return sgr + s + sgrReset
}

// step writes one styled step line to stdout: a glyph and its text in sgr.
func (w Run) step(sgr, glyph, text string) {
	fmt.Fprintln(w.Out, w.paint(sgr, glyph+" "+text))
}

// done is a success step, shown dimmed and without ✓ once an attestation
// failed verification: nothing is green after a security event.
func (w Run) done(glyph, sgr, text string) {
	if w.Security {
		glyph, sgr = glyphNeutral, sgrDim
	}
	w.step(sgr, glyph, text)
}

// shortDigest matches a sha256 digest for display as 8 hex characters.
var shortDigest = regexp.MustCompile(`sha256:([0-9a-f]{8})[0-9a-f]*`)

// short shows every sha256 digest in s as 8 hex characters. Display only:
// plain output, JSON and the report file keep them whole.
func short(s string) string { return shortDigest.ReplaceAllString(s, "sha256:$1") }

// styledReport writes the decision r: the outcome line behind its glyph,
// then labelled, aligned detail lines. Every value went through OneLine,
// so attestation text cannot break the alignment or move the cursor.
func (w Run) styledReport(r evidence.Report) {
	glyph, sgr := glyphNeutral, ""
	switch {
	case r.Outcome == evidence.OutcomeVerificationFailed:
		glyph, sgr = glyphFailed, sgrBold+sgrRed
	case r.Outcome == evidence.OutcomeVerified:
		glyph, sgr = glyphReused, sgrCyan
	case r.Outcome == evidence.OutcomeError, strings.HasPrefix(r.Reason, "reuse off"):
		glyph, sgr = glyphWarn, sgrYellow
	}
	fp := strings.TrimPrefix(short(r.Fingerprint), "sha256:")
	w.step(sgr, glyph, fmt.Sprintf("%s %s %s", r.Outcome, OneLine(r.TestID), OneLine(fp)))
	for _, d := range details(r) {
		label, sgr := d.label, sgrDim
		if label == "rejected" {
			label, sgr = "REJECTED", sgrRed
		}
		fmt.Fprintf(w.Out, "  %s %s\n", w.paint(sgr, fmt.Sprintf("%-9s", label)), short(OneLine(d.value)))
	}
}

// styledSummary is the closing sentence: what happened and what to do next.
func styledSummary(s Summary) string {
	t := OneLine(s.Test)
	if s.Reused {
		saved := ""
		if s.Saved > 0 {
			saved = ", saved " + duration(s.Saved)
		}
		return fmt.Sprintf("%s: reused a verified %s result%s; nothing ran.", t, s.Result, saved)
	}
	b := fmt.Sprintf("%s %s in %s", t, s.Result, duration(s.Took))
	switch {
	case s.Pushed:
		return b + "; published for the next run with these inputs."
	case s.ReuseOff:
		return b + " with reuse off; see the reason above."
	case s.Result != evidence.ResultPassed:
		return b + "; fix it, then aval run again."
	}
	return b + "; aval push publishes it for the next run."
}
