package main

import (
	"io"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// outputEnv set to plain forces plain output, like --plain.
const outputEnv = "AVAL_OUTPUT"

// outputStyle is the one place that picks how aval run prints. Styled only
// for a human: stdout is a terminal, TERM is not dumb, and neither
// AVAL_OUTPUT=plain nor --plain asks for the plain lines CI reads. Colour
// unless NO_COLOR is set (https://no-color.org); NO_COLOR drops colour only.
func outputStyle(tty, plain bool, getenv func(string) string) (styled, color bool) {
	styled = tty && !plain && getenv("TERM") != "dumb" && getenv(outputEnv) != "plain"
	return styled, styled && getenv("NO_COLOR") == ""
}

// isTTY reports whether w is a terminal. ponytail: a seam so tests can
// drive the styled path without a pty.
var isTTY = func(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// plainFlag is the root's --plain as cmd sees it.
func plainFlag(cmd *cobra.Command) bool {
	plain, _ := cmd.Flags().GetBool("plain") // ponytail: bound on the root, cannot fail
	return plain
}
