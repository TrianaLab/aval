package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Command groups in `aval --help`.
var groups = []*cobra.Group{
	{ID: "core", Title: "Core:"},
	{ID: "publishing", Title: "Publishing:"},
	{ID: "statistics", Title: "Statistics:"},
}

// commandHelp is each command's group (empty: "Additional Commands") and
// Example, kept in one place so `aval --help` reads as one document.
var commandHelp = map[string]struct{ group, example string }{
	"init": {"core", `  # Write aval.yaml, aval.key, aval.pub and policy.yaml
  aval init --name unit --source 'src/**' --source go.mod --cacheability hermetic --key aval.key -- go test ./...`},
	"run": {"core", `  # Reuse a trusted result, or run the test
  aval run --registry ghcr.io/acme/orders-aval --policy policy.yaml

  # Run, then sign and publish the result keyless
  aval run --registry ghcr.io/acme/orders-aval --push`},
	"verify": {"core", `  aval verify --registry ghcr.io/acme/orders-aval --policy policy.yaml -o json`},
	"push":   {"publishing", `  aval push --registry ghcr.io/acme/orders-aval --key aval.key`},
	"explain": {"core", `  # What the fingerprint covers, digest last
  aval explain

  # Only the digest
  aval explain --digest`},
	"keygen": {"publishing", `  aval keygen        # aval.key and aval.pub
  aval keygen ci     # ci.key and ci.pub`},
	"record":     {"", `  aval stats publish --registry ghcr.io/acme/orders-aval record.json`},
	"stats":      {"statistics", `  aval stats --registry ghcr.io/acme/orders-aval --since 30d`},
	"version":    {"", `  aval version`},
	"upgrade":    {"", `  aval upgrade --check`},
	"help":       {"", `  aval help run`},
	"completion": {"", `  source <(aval completion bash)`},
}

// guide adds the command groups, the examples and did-you-mean suggestions
// for commands and flags to root, once every command is added.
func guide(root *cobra.Command) {
	root.SuggestionsMinimumDistance = 2
	root.AddGroup(groups...)
	root.Args = func(c *cobra.Command, args []string) error {
		if len(args) > 0 {
			return unknownCommand(c, args[0])
		}
		return nil
	}
	root.SetFlagErrorFunc(flagError)
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	for _, c := range root.Commands() {
		if c.Name() == "help" {
			show := c.Run
			c.Run = nil
			// cobra's help prints the root help for a typo and exits 0.
			c.RunE = func(h *cobra.Command, args []string) error {
				if target, rest, err := root.Find(args); err == nil && len(rest) > 0 {
					return unknownCommand(target, rest[0])
				}
				show(h, args)
				return nil
			}
		}
		h := commandHelp[c.Name()]
		c.GroupID, c.Example = h.group, h.example
	}
}

// unknownCommand is the usage error for a command name c does not have,
// with the closest subcommand if one is near.
func unknownCommand(c *cobra.Command, name string) error {
	msg := fmt.Sprintf("unknown command %q", name)
	if s := c.SuggestionsFor(name); len(s) > 0 {
		msg += fmt.Sprintf("; did you mean %q?", s[0])
	}
	return usageError{errors.New(msg)}
}

// flagError adds the closest visible flag of c (Levenshtein distance 2 or
// less) to an unknown-flag error, then where to look.
func flagError(c *cobra.Command, err error) error {
	msg := err.Error()
	if name, ok := strings.CutPrefix(msg, "unknown flag: --"); ok {
		best, dist := "", 3
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if d := levenshtein(name, f.Name); !f.Hidden && d < dist {
				best, dist = f.Name, d
			}
		})
		if best != "" {
			return usageError{fmt.Errorf("%s; did you mean --%s? see %s --help", msg, best, c.CommandPath())}
		}
	}
	return usageError{fmt.Errorf("%s; see %s --help", msg, c.CommandPath())}
}

// levenshtein is the edit distance between a and b.
func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := []int{i}
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur = append(cur, min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost))
		}
		prev = cur
	}
	return prev[len(b)]
}
