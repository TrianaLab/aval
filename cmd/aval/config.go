package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/TrianaLab/aval/internal/cliout"
	"github.com/TrianaLab/aval/internal/contract"
)

// Environment fallbacks for --registry and --policy. They are the only
// sources besides the flags: no config file and no aval.yaml field, because
// the tree under test must not choose what it is trusted against (#137,
// tests/architecture TestTrustSource).
const (
	registryEnv = "AVAL_REGISTRY"
	policyEnv   = "AVAL_POLICY"
)

// globalFlags are the flags several commands share. Each command binds only
// the ones it uses, so no command accepts a flag it ignores.
type globalFlags struct {
	file, registry, policy string
	timeout                time.Duration
	usesPolicy             bool            // --policy is bound: resolve and show it
	fromEnv                map[string]bool // the variables a value came from
}

// bind adds --registry and --registry-timeout to cmd, plus -f/--contract and
// --policy when it uses them, and resolves the environment before RunE.
func (g *globalFlags) bind(cmd *cobra.Command, withContract, withPolicy bool) {
	f := cmd.Flags()
	if withContract {
		f.StringVarP(&g.file, "contract", "f", contract.DefaultFile, "path to the test contract")
	}
	f.StringVar(&g.registry, "registry", "", "OCI repository for attestations and run records, e.g. "+repoExample+" (default: $"+registryEnv+")")
	if withPolicy {
		f.StringVar(&g.policy, "policy", "", "trust policy file; without it nothing is reused (default: $"+policyEnv+"; an empty flag also falls back to it)")
	}
	f.DurationVar(&g.timeout, "registry-timeout", time.Minute, "deadline for this command's registry calls")
	g.usesPolicy = withPolicy
	cmd.PreRun = func(*cobra.Command, []string) { g.resolve(os.Getenv) }
}

// resolve applies flag > env > unset to --registry and --policy. An empty
// value counts as unset. It reads nothing else: no file, no contract.
func (g *globalFlags) resolve(getenv func(string) string) {
	g.fromEnv = map[string]bool{}
	vars := map[string]*string{registryEnv: &g.registry}
	if g.usesPolicy {
		vars[policyEnv] = &g.policy
	}
	for name, v := range vars {
		if *v == "" {
			if *v = getenv(name); *v != "" {
				g.fromEnv[name] = true
			}
		}
	}
}

// printConfig prints one config: line when a value came from the
// environment, so a run never uses a setting it does not show. A nil g
// prints nothing.
func (g *globalFlags) printConfig(w io.Writer) {
	if g == nil || len(g.fromEnv) == 0 {
		return
	}
	var parts []string
	for _, v := range []struct{ name, env, val string }{{"registry", registryEnv, g.registry}, {"policy", policyEnv, g.policy}} {
		src := "flag"
		if g.fromEnv[v.env] {
			src = "env " + v.env
		}
		if v.val != "" {
			parts = append(parts, fmt.Sprintf("%s=%s (%s)", v.name, cliout.OneLine(v.val), src))
		}
	}
	fmt.Fprintln(w, "config: "+strings.Join(parts, " "))
}
