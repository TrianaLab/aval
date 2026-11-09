package main

import (
	"fmt"
	"io"
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

// globalFlags are the persistent root flags every command shares.
type globalFlags struct {
	file, registry, policy string
	timeout                time.Duration
	fromEnv                map[string]bool // the variables a value came from
}

func (g *globalFlags) bind(root *cobra.Command) {
	f := root.PersistentFlags()
	f.StringVarP(&g.file, "contract", "f", contract.DefaultFile, "path to the test contract")
	f.StringVar(&g.registry, "registry", "", "OCI repository for attestations and run records, e.g. "+repoExample+" (default: $"+registryEnv+")")
	f.StringVar(&g.policy, "policy", "", "trust policy file; without it nothing is reused (default: $"+policyEnv+")")
	f.DurationVar(&g.timeout, "registry-timeout", time.Minute, "deadline for this command's registry calls")
}

// resolve applies flag > env > unset to --registry and --policy. An empty
// value counts as unset. It reads nothing else: no file, no contract.
func (g *globalFlags) resolve(getenv func(string) string) {
	g.fromEnv = map[string]bool{}
	for name, v := range map[string]*string{registryEnv: &g.registry, policyEnv: &g.policy} {
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
