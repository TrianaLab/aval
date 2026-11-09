package main

import (
	"strings"
	"testing"
)

// Typos get a suggestion and exit 2; help and behaviour stay otherwise unchanged.
func TestSuggestions(t *testing.T) {
	cases := []struct {
		args   []string
		code   int
		stderr string
	}{
		{[]string{"rn"}, ExitUsage, "aval: unknown command \"rn\"; did you mean \"run\"?\n"},
		{[]string{"help", "rn"}, ExitUsage, "aval: unknown command \"rn\"; did you mean \"run\"?\n"},
		{[]string{"nope"}, ExitUsage, "aval: unknown command \"nope\"\n"},
		{[]string{"help", "nope"}, ExitUsage, "aval: unknown command \"nope\"\n"},
		{[]string{"help", "verify", "x"}, ExitUsage, "aval: unknown command \"x\"\n"},
		{[]string{"help", "verify"}, ExitOK, ""},
		{[]string{"help"}, ExitOK, ""},
		{[]string{"run", "--registy", "r"}, ExitUsage, "aval: unknown flag: --registy; did you mean --registry? see aval run --help\n"},
		{[]string{"verify", "--polcy", "p"}, ExitUsage, "aval: unknown flag: --polcy; did you mean --policy? see aval verify --help\n"},
		{[]string{"run", "--zzzzzzzz"}, ExitUsage, "aval: unknown flag: --zzzzzzzz; see aval run --help\n"},
		{[]string{"run", "-Z"}, ExitUsage, "aval: unknown shorthand flag: 'Z' in -Z; see aval run --help\n"},
		{[]string{"--bogus"}, ExitUsage, "aval: unknown flag: --bogus; see aval --help\n"},
	}
	for _, tc := range cases {
		code, _, stderr := runCLI(tc.args...)
		if code != tc.code || stderr != tc.stderr {
			t.Errorf("aval %q: exit %d, stderr %q; want exit %d, stderr %q", tc.args, code, stderr, tc.code, tc.stderr)
		}
	}
}

func TestEveryCommandHasAnExample(t *testing.T) {
	root := newRootCmd()
	for _, c := range root.Commands() {
		if c.Example == "" {
			t.Errorf("aval %s has no Example", c.Name())
		}
	}
}

func TestRootHelpGroupsCommands(t *testing.T) {
	_, stdout, _ := runCLI("--help")
	for _, g := range []string{"Core:\n  explain", "Publishing:\n  keygen ", "\n  push ", "Statistics:\n  stats"} {
		if !strings.Contains(stdout, g) {
			t.Errorf("aval --help lacks %q:\n%s", g, stdout)
		}
	}
}

func TestLevenshtein(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{{"", "abc", 3}, {"abc", "", 3}, {"registy", "registry", 1}, {"kitten", "sitting", 3}, {"same", "same", 0}} {
		if got := levenshtein(tc.a, tc.b); got != tc.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
