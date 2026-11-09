package cliout

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// Run writes the plain output of `aval run`, for the CLI and the browser
// demo: the outcome-first report lines, the runner output framed by --- run
// and --- end, and one closing summary line. Warnings go to Err.
type Run struct{ Out, Err io.Writer }

// Decision writes the lookup decision r for a contract of class: a note on
// the class unless the reason already names it, then the report lines.
func (w Run) Decision(r evidence.Report, class contract.Cacheability) {
	if !strings.Contains(r.Reason, "cacheability "+string(class)) {
		switch class {
		case contract.NonDeterministic:
			fmt.Fprintf(w.Out, "cacheability %s: never reusable\n", class)
		case contract.ExternalDependencies:
			fmt.Fprintf(w.Out, "cacheability %s: not reusable by default (the policy may allow it)\n", class)
		}
	}
	_ = WriteReport(w.Out, "text", r) // text never fails, like the other lines
}

// ReuseOff warns that cause turned reuse off. The test still runs.
func (w Run) ReuseOff(cause string) {
	fmt.Fprintf(w.Err, "aval: warning: reuse off: %s\n", OneLine(cause))
}

// Start opens the frame around the runner output, which passes through
// untouched.
func (w Run) Start(test string, argv []string) {
	fmt.Fprintf(w.Out, "--- run %s: %s\n", test, OneLine(strings.Join(argv, " ")))
}

// End closes the frame and writes the ran line.
func (w Run) End(test string, result evidence.Result, exit int, took time.Duration) {
	fmt.Fprintf(w.Out, "--- end %s: %s (exit %d) in %s\n", test, result, exit, duration(took))
	fmt.Fprintf(w.Out, "ran %s: %s (exit %d)\n", test, result, exit)
}

// Reused says a verified result was reused instead of running the test.
func (w Run) Reused(result evidence.Result) { fmt.Fprintf(w.Out, "reused %s result\n", result) }

// Pushed names the published attestation.
func (w Run) Pushed(ref string) { fmt.Fprintf(w.Out, "pushed %s\n", ref) }

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
func (w Run) Summary(s Summary) {
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
