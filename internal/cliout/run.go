package cliout

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// Run writes the output of `aval run`, for the CLI and the browser demo.
// Plain (CI logs, pipes): the outcome-first report lines, the runner output
// framed by --- run and --- end, and one closing summary line. Styled (a
// terminal, style.go): the same steps as glyph lines and a closing sentence.
// Warnings go to Err.
type Run struct {
	Out, Err io.Writer
	Styled   bool // a human at a terminal
	Color    bool // styled with ANSI colour (not under NO_COLOR, not in the demo)
	Security bool // an attestation failed verification: nothing green, no ✓ from here on
}

// Decision writes the lookup decision r for a contract of class: a note on
// the class unless the reason already starts with it, then the report
// lines. Prefix, not substring: a reason can quote remote text, which must
// not hide the note.
func (w Run) Decision(r evidence.Report, class contract.Cacheability) {
	if !strings.HasPrefix(r.Reason, "cacheability "+string(class)) {
		switch class {
		case contract.NonDeterministic:
			w.line(glyphNeutral, sgrDim, fmt.Sprintf("cacheability %s: never reusable", class))
		case contract.ExternalDependencies:
			w.line(glyphNeutral, sgrDim, fmt.Sprintf("cacheability %s: not reusable by default (the policy may allow it)", class))
		}
	}
	if w.Styled {
		w.styledReport(r)
		return
	}
	_ = WriteReport(w.Out, "text", r) // text never fails, like the other lines
}

// line writes text as it is in plain mode, or as a styled step.
func (w Run) line(glyph, sgr, text string) {
	if w.Styled {
		w.step(sgr, glyph, text)
		return
	}
	fmt.Fprintln(w.Out, text)
}

// Note writes a line about the run's setup, such as config:, dimmed when
// styled. An empty text writes nothing.
func (w Run) Note(text string) {
	if text != "" {
		w.line(glyphNeutral, sgrDim, text)
	}
}

// SecurityBlock writes line, a SecurityLine or FloodLine, to Err when the
// failure is found; styled, behind ✗ in bold red. The command repeats the
// plain line as the last line of stderr in every mode.
func (w Run) SecurityBlock(line string) {
	if w.Styled {
		line = w.paint(sgrBold+sgrRed, glyphFailed+" "+strings.TrimPrefix(line, "aval: "))
	}
	fmt.Fprintln(w.Err, line)
}

// ReuseOff warns that cause turned reuse off. The test still runs.
func (w Run) ReuseOff(cause string) {
	if w.Styled {
		fmt.Fprintln(w.Err, w.paint(sgrYellow, glyphWarn+" reuse off: "+OneLine(cause)))
		return
	}
	fmt.Fprintf(w.Err, "aval: warning: reuse off: %s\n", OneLine(cause))
}

// Start opens the frame around the runner output, which passes through
// untouched.
func (w Run) Start(test string, argv []string) {
	if w.Styled {
		w.step(sgrDim, glyphNeutral, fmt.Sprintf("run %s: %s", OneLine(test), OneLine(strings.Join(argv, " "))))
		return
	}
	fmt.Fprintf(w.Out, "--- run %s: %s\n", test, OneLine(strings.Join(argv, " ")))
}

// End closes the frame and writes the ran line.
func (w Run) End(test string, result evidence.Result, exit int, took time.Duration) {
	if w.Styled {
		text := fmt.Sprintf("%s %s (exit %d) in %s", OneLine(test), result, exit, duration(took))
		if result != evidence.ResultPassed {
			w.step(sgrRed, glyphFailed, text)
			return
		}
		w.done(glyphDone, sgrGreen, text)
		return
	}
	fmt.Fprintf(w.Out, "--- end %s: %s (exit %d) in %s\n", test, result, exit, duration(took))
	fmt.Fprintf(w.Out, "ran %s: %s (exit %d)\n", test, result, exit)
}

// Reused says a verified result was reused instead of running the test.
func (w Run) Reused(result evidence.Result) {
	text := fmt.Sprintf("reused %s result", result)
	switch {
	case !w.Styled:
		fmt.Fprintln(w.Out, text)
	case result != evidence.ResultPassed:
		w.step(sgrRed, glyphFailed, text)
	default:
		w.done(glyphReused, sgrCyan, text)
	}
}

// Pushed names the published attestation.
func (w Run) Pushed(ref string) {
	if w.Styled {
		w.done(glyphDone, sgrGreen, "pushed "+short(ref))
		return
	}
	fmt.Fprintf(w.Out, "pushed %s\n", ref)
}

// Summary is what the closing line of a run says.
type Summary struct {
	Test     string
	Reused   bool            // the verified result was reused, the test did not run
	Result   evidence.Result // of the run, or of the reused result
	Took     time.Duration   // the run
	Saved    time.Duration   // the reused run, from its predicate; display only
	Pushed   bool            // the result was published
	ReuseOff bool            // no registry or policy, --no-reuse, or an error
	Security bool            // an attestation failed verification
}

// Summary writes the closing line:
// summary: <test> <ran|reused>: <result>[ in D][; saved D][; result pushed][; reuse was off][; SECURITY]
// Styled, it is one sentence; after a security event it says only that.
func (w Run) Summary(s Summary) {
	switch {
	case w.Styled && s.Security:
		w.step(sgrBold+sgrRed, glyphFailed, "SECURITY: an attestation for "+OneLine(s.Test)+" failed verification; treat it as a security event, not a flaky test.")
		return
	case w.Styled:
		fmt.Fprintln(w.Out, w.paint(sgrBold, styledSummary(s)))
		return
	}
	b := fmt.Sprintf("summary: %s ran: %s in %s", s.Test, s.Result, duration(s.Took))
	if s.Reused {
		b = fmt.Sprintf("summary: %s reused: %s", s.Test, s.Result)
		if s.Saved > 0 {
			b += "; saved " + duration(s.Saved)
		}
	}
	for _, x := range []struct {
		on   bool
		text string
	}{{s.Pushed, "result pushed"}, {s.ReuseOff, "reuse was off"}, {s.Security, "SECURITY"}} {
		if x.on {
			b += "; " + x.text
		}
	}
	fmt.Fprintln(w.Out, b)
}

// duration rounds d for display: milliseconds under a second, else tenths.
func duration(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(100 * time.Millisecond).String()
}
