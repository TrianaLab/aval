package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

var update = flag.Bool("update", false, "rewrite docs/cli-reference.md from the cobra command tree")

var cliReference = filepath.Join("..", "..", "docs", "cli-reference.md")

// renderCLIReference writes one section per command, in the order `aval
// --help` lists them, from the same cobra tree the binary executes.
func renderCLIReference() []byte {
	root := newRootCmd()
	// cobra adds these lazily in Execute; add them so the reference matches --help.
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	root.InitDefaultVersionFlag()
	var b bytes.Buffer
	b.WriteString("# CLI reference\n\n")
	b.WriteString("Generated from the cobra command tree by `make docs-cli`. Do not edit by hand:\n")
	b.WriteString("`make ci-docs` fails when this file and the CLI disagree.\n")
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		c.InitDefaultHelpFlag()
		desc := c.Long
		if desc == "" {
			desc = c.Short
		}
		b.WriteString("\n## " + c.CommandPath() + "\n\n" + strings.TrimSpace(desc) + "\n\n")
		b.WriteString("```text\n" + strings.TrimSpace(c.UsageString()) + "\n```\n")
		// ponytail: completion's per-shell children are cobra boilerplate; its usage block lists them.
		if c.Name() == "completion" {
			return
		}
		for _, sub := range c.Commands() {
			if sub.IsAvailableCommand() {
				walk(sub)
			}
		}
	}
	walk(root)
	return b.Bytes()
}

// TestCLIReference fails when docs/cli-reference.md drifts from the CLI.
// Regenerate with `make docs-cli`.
func TestCLIReference(t *testing.T) {
	got := renderCLIReference()
	if *update {
		if err := os.WriteFile(cliReference, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(cliReference)
	if err != nil {
		t.Fatalf("%v (run `make docs-cli`)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("docs/cli-reference.md is out of date: run `make docs-cli` and commit the result")
	}
}
