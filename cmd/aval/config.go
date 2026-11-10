package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/TrianaLab/aval/internal/cliout"
	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/policy"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// Environment fallbacks for --registry and --policy. Besides the flags, they
// and the trust policy's registry (#209) are the only sources: no config file
// and no aval.yaml field, because the tree under test must not choose what it
// is trusted against (#137, tests/architecture TestTrustSource).
const (
	registryEnv = "AVAL_REGISTRY"
	policyEnv   = "AVAL_POLICY"
	// minTrustEnv backs --min-trust-level (#214). It can only raise the
	// policy's minTrustLevel, so it cannot widen what the policy trusts.
	minTrustEnv = "AVAL_MIN_TRUST_LEVEL"
)

// globalFlags are the flags several commands share. Each command binds only
// the ones it uses, so no command accepts a flag it ignores.
type globalFlags struct {
	file, registry, policy string
	minTrust               string              // --min-trust-level as given
	minLevel               evidence.TrustLevel // minTrust parsed; 0 when unset
	timeout                time.Duration
	usesPolicy             bool              // --policy is bound: resolve and show it
	source                 map[string]string // "registry"/"policy" -> where a value not from a flag came from
}

// bind adds --registry and --registry-timeout to cmd, plus -f/--contract and
// --policy when it uses them, and resolves the environment before RunE.
func (g *globalFlags) bind(cmd *cobra.Command, withContract, withPolicy bool) {
	f := cmd.Flags()
	if withContract {
		f.StringVarP(&g.file, "contract", "f", contract.DefaultFile, "path to the test contract")
	}
	registryDefault := "$" + registryEnv
	if withPolicy {
		registryDefault += ", then registry in the trust policy"
	}
	f.StringVar(&g.registry, "registry", "", "OCI repository for attestations and run records, e.g. "+repoExample+" (default: "+registryDefault+")")
	if withPolicy {
		f.StringVar(&g.policy, "policy", "", "trust policy file; without it nothing is reused (default: $"+policyEnv+"; an empty flag also falls back to it)")
		f.StringVar(&g.minTrust, "min-trust-level", "", "raise the trust policy's minTrustLevel to developer, runner or isolated; a value below it exits 2 (default: $"+minTrustEnv+")")
	}
	f.DurationVar(&g.timeout, "registry-timeout", time.Minute, "deadline for this command's registry calls")
	g.usesPolicy = withPolicy
	cmd.PreRunE = func(*cobra.Command, []string) error { return g.resolve(os.Getenv) }
}

// resolve applies flag > env > unset to --registry and --policy, then, for a
// command that uses the policy, falls back to the policy's registry. An empty
// value counts as unset. It reads nothing else: no other file, no contract.
// A policy whose registry holds credentials is a usage error; any other
// policy error is left to the lookup, which reports it as before.
func (g *globalFlags) resolve(getenv func(string) string) error {
	g.source = map[string]string{}
	vars := map[string]*string{registryEnv: &g.registry}
	if g.usesPolicy {
		vars[policyEnv] = &g.policy
		vars[minTrustEnv] = &g.minTrust
	}
	for name, v := range vars {
		if *v == "" {
			if *v = getenv(name); *v != "" {
				g.source[name] = "env " + name
			}
		}
	}
	if g.minTrust != "" {
		var ok bool
		if g.minLevel, ok = policy.ParseTrustLevel(g.minTrust); !ok {
			return usageError{fmt.Errorf("--min-trust-level: %q must be developer, runner or isolated", g.minTrust)}
		}
	}
	if g.policy == "" {
		return nil
	}
	pol, err := loadPolicy(g.policy)
	if errors.Is(err, policy.ErrRegistryCredentials) {
		return usageError{err}
	}
	if err != nil {
		return nil
	}
	// Only raising: a lower minimum would trust what the policy refuses.
	if g.minLevel != 0 && g.minLevel < pol.MinTrustLevel {
		return usageError{fmt.Errorf("--min-trust-level %s is below minTrustLevel %s of the trust policy %s; it can only raise it", g.minLevel, pol.MinTrustLevel, cliout.OneLine(g.policy))}
	}
	if g.registry == "" && pol.Registry != "" {
		g.registry, g.source[registryEnv] = pol.Registry, "policy "+cliout.OneLine(g.policy)
	}
	return nil
}

// printConfig prints one config: line when a value did not come from a flag,
// so a run never uses a setting it does not show. A nil g prints nothing.
func (g *globalFlags) printConfig(w io.Writer) {
	if l := g.configLine(); l != "" {
		fmt.Fprintln(w, l)
	}
}

// configLine is the config: line, empty when every value came from a flag or
// g is nil.
func (g *globalFlags) configLine() string {
	if g == nil || len(g.source) == 0 {
		return ""
	}
	var parts []string
	for _, v := range []struct{ name, env, val string }{{"registry", registryEnv, g.registry}, {"policy", policyEnv, g.policy}, {"min-trust-level", minTrustEnv, g.minTrust}} {
		if v.val != "" {
			parts = append(parts, fmt.Sprintf("%s=%s (%s)", v.name, cliout.OneLine(v.val), cmp.Or(g.source[v.env], "flag")))
		}
	}
	return "config: " + strings.Join(parts, " ")
}
