package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/pkg/evidence"
)

// The selection matrix: styled only on a terminal that is not dumb, without
// AVAL_OUTPUT=plain or --plain; NO_COLOR drops colour, never the style.
func TestOutputStyle(t *testing.T) {
	for _, tty := range []bool{false, true} {
		for _, term := range []string{"xterm-256color", "dumb", ""} {
			for _, noColor := range []string{"", "1"} {
				for _, output := range []string{"", "plain", "PLAIN", "auto"} {
					for _, plain := range []bool{false, true} {
						env := map[string]string{"TERM": term, "NO_COLOR": noColor, outputEnv: output}
						styled, color := outputStyle(tty, plain, func(k string) string { return env[k] })
						wantStyled := tty && term != "dumb" && !strings.EqualFold(output, "plain") && !plain
						if styled != wantStyled || color != (wantStyled && noColor == "" && runtime.GOOS != "windows") {
							t.Errorf("tty=%v TERM=%q NO_COLOR=%q %s=%q --plain=%v: styled %v color %v", tty, term, noColor, outputEnv, output, plain, styled, color)
						}
					}
				}
			}
		}
	}
}

func TestIsTTY(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTTY(f) || isTTY(&bytes.Buffer{}) {
		t.Fatal("a file or a buffer is not a terminal")
	}
}

// onTTY makes run see a terminal on stdout, with a clean environment for
// the output mode.
func onTTY(t *testing.T, noColor string) {
	t.Helper()
	tty := isTTY
	t.Cleanup(func() { isTTY = tty })
	isTTY = func(io.Writer) bool { return true }
	t.Setenv("TERM", "xterm-256color")
	t.Setenv(outputEnv, "")
	t.Setenv("NO_COLOR", noColor)
}

var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

// Styled output for every outcome, with colour (raw golden) and with the
// colour stripped (readable golden). Under NO_COLOR it is that stripped text
// exactly, without a single ESC byte. In every styled mode a security event
// (a verification failure, a rejected forged sibling, a flood) shows
// SECURITY and ✗, prints nothing green and no ✓, and ends stderr with the
// plain `aval: SECURITY:` line; a verification failure exits 5 (invariant 4).
func TestStyledRunGoldens(t *testing.T) {
	for _, oc := range runOutcomes() {
		t.Run(oc.name, func(t *testing.T) {
			var color, noColor strings.Builder
			for _, push := range []bool{false, true} {
				for _, shadow := range []bool{false, true} {
					for _, failed := range []bool{false, true} {
						onTTY(t, "")
						styledCase(t, &color, oc, push, shadow, failed)
						onTTY(t, "1")
						styledCase(t, &noColor, oc, push, shadow, failed)
					}
				}
			}
			checkGolden(t, "run-"+oc.name+".color.golden", color.String())
			checkGolden(t, "run-"+oc.name+".styled.golden", sgr.ReplaceAllString(color.String(), ""))
			if strings.ContainsRune(noColor.String(), '\x1b') || noColor.String() != sgr.ReplaceAllString(color.String(), "") {
				t.Fatalf("NO_COLOR output is not the colour output without ANSI:\n%s", noColor.String())
			}
		})
	}
}

// styledCase runs one styled case and checks the security rules.
func styledCase(t *testing.T, got *strings.Builder, oc plainOutcome, push, shadow, failed bool) {
	t.Helper()
	name, code, stdout, stderr, r := runOutcome(t, got, oc, push, shadow, failed)
	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	switch {
	case r.Outcome != oc.outcome:
		t.Errorf("%s: outcome %q, want %q", name, r.Outcome, oc.outcome)
	case strings.Contains(stdout, "summary:") || strings.Contains(stdout, "--- run"):
		t.Errorf("%s: plain lines in styled output %q", name, stdout)
	case !security(r, stderr):
	case !strings.HasPrefix(lines[len(lines)-1], "aval: SECURITY: "),
		r.Outcome == evidence.OutcomeVerificationFailed && code != ExitVerificationFailed:
		t.Errorf("%s: exit %d, last stderr line %q", name, code, lines[len(lines)-1])
	case strings.Contains(stdout+stderr, "\x1b[32m") || strings.Contains(stdout, "✓"):
		t.Errorf("%s: green or ✓ after a verification failure: %q", name, stdout)
	case !strings.Contains(stdout, "✗ SECURITY") || !strings.Contains(stderr, "✗ SECURITY"):
		t.Errorf("%s: no ✗ SECURITY: stdout %q stderr %q", name, stdout, stderr)
	}
}

// --plain, AVAL_OUTPUT=plain and TERM=dumb keep the plain lines on a
// terminal; the config: line is a dimmed step when styled.
func TestPlainOnTTY(t *testing.T) {
	c := contractWith(t, "hermetic", "true")
	result := filepath.Join(t.TempDir(), "r.json")
	for name, set := range map[string]func(){
		"--plain":            func() {},
		outputEnv + "=plain": func() { t.Setenv(outputEnv, "plain") },
		"TERM=dumb":          func() { t.Setenv("TERM", "dumb") },
	} {
		onTTY(t, "")
		set()
		args := []string{"run", "-f", c, "--result-file", result}
		if name == "--plain" {
			args = append(args, "--plain")
		}
		_, stdout, _ := runCLI(args...)
		if strings.ContainsRune(stdout, '\x1b') || !strings.Contains(stdout, "\nsummary: unit ran: passed") {
			t.Errorf("%s: not plain: %q", name, stdout)
		}
	}
	onTTY(t, "1")
	t.Setenv(registryEnv, "reg.example/r")
	noLookup(t)
	_, stdout, _ := runCLI("run", "-f", c, "--result-file", result)
	if !strings.HasPrefix(stdout, "· config: registry=reg.example/r (env AVAL_REGISTRY)\n") {
		t.Fatalf("styled config line: %q", stdout)
	}
}
