// Command aval runs, publishes and verifies signed test execution results.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/fingerprint"
	"github.com/TrianaLab/aval/internal/policy"
	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// Exit codes are part of the CLI contract. A miss and a verification
// failure are deliberately different: a miss is routine, a verification
// failure means someone published a result we do not trust.
const (
	ExitOK                 = 0 // test passed, or a verified result was reused
	ExitError              = 1 // unexpected error (registry down, I/O, ...)
	ExitUsage              = 2 // bad flags or invalid aval.yaml
	ExitTestFailed         = 3 // the test ran and failed
	ExitMiss               = 4 // no attestation for this fingerprint
	ExitVerificationFailed = 5 // attestation exists but failed signature/policy: security event
	ExitNotImplemented     = 6 // feature lands in a later milestone
)

type usageError struct{ error }

// Package variables so tests can drive verify through every outcome while
// the real implementations are still stubs.
// TODO(milestone-2): replace these seams with injected dependencies once registry, policy and evidence are real.
var (
	loadPolicy     = policy.Load
	lookup         = registry.Lookup
	fetch          = registry.Fetch
	verifyEnvelope = evidence.Verify
)

// main stays one line: the coverage gate (make coverage-check) excludes
// exactly this line, so all logic belongs in run.
func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run executes the CLI with args and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	root := newRootCmd()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.Execute()
	if err != nil {
		fmt.Fprintln(stderr, "aval:", err)
	}
	return exitCode(err)
}

func exitCode(err error) int {
	var ue usageError
	switch {
	case err == nil:
		return ExitOK
	case errors.As(err, &ue):
		return ExitUsage
	case errors.Is(err, evidence.ErrVerification):
		return ExitVerificationFailed
	case errors.Is(err, evidence.ErrNotFound):
		return ExitMiss
	case errors.Is(err, evidence.ErrNotImplemented):
		return ExitNotImplemented
	}
	return ExitError
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "aval",
		Short:         "Verifiable test execution reuse: signed test results as OCI artifacts",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	root.Args = func(_ *cobra.Command, args []string) error {
		if len(args) > 0 {
			return usageError{fmt.Errorf("unknown command %q", args[0])}
		}
		return nil
	}
	root.RunE = func(c *cobra.Command, _ []string) error { return c.Help() }
	root.AddCommand(newRunCmd(), newPushCmd(), newVerifyCmd())
	return root
}

// noArgs is cobra.NoArgs with exit code 2 (usage) instead of 1.
func noArgs(c *cobra.Command, args []string) error {
	if err := cobra.NoArgs(c, args); err != nil {
		return usageError{err}
	}
	return nil
}

// loadAndFingerprint loads the contract and computes its fingerprint, rooted
// at the contract's directory.
func loadAndFingerprint(file string) (*contract.Contract, fingerprint.Fingerprint, error) {
	c, err := contract.Load(file)
	if err != nil {
		return nil, fingerprint.Fingerprint{}, usageError{err}
	}
	fp, err := fingerprint.Compute(filepath.Dir(file), c, os.LookupEnv)
	if err != nil {
		// Cannot prove input equivalence: callers must run the test.
		return nil, fingerprint.Fingerprint{}, fmt.Errorf("fingerprint: %w", err)
	}
	return c, fp, nil
}

func newRunCmd() *cobra.Command {
	var (
		file, repo, policyFile string
		push, noReuse          bool
		fingerprintOnly        bool
		explain                bool
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Reuse a verified result for the current inputs, or run the test",
		Long: `run computes the execution fingerprint of the contract, looks for a signed
attestation in the registry, and reuses it if it verifies against the trust
policy. Otherwise it runs the test (and, with --push, publishes the result).`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, fp, err := loadAndFingerprint(file)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if explain {
				fmt.Fprint(out, fp.Manifest)
			}
			if fingerprintOnly {
				fmt.Fprintln(out, fp.Digest)
				return nil
			}
			fmt.Fprintf(out, "test %s fingerprint %s\n", c.Metadata.Name, fp.Digest)
			if !c.Cacheability.ReusableByDefault() {
				fmt.Fprintf(out, "cacheability %s: not reusable by default\n", c.Cacheability)
			}
			// TODO(milestone-2): lookup + verify (reuse on success), else execute
			// runner.command, record the result and optionally push it.
			return fmt.Errorf("run: lookup and execution: %w", evidence.ErrNotImplemented)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&file, "contract", "f", contract.DefaultFile, "path to the test contract")
	f.StringVar(&repo, "registry", "", "OCI repository for attestations, e.g. ghcr.io/acme/orders-aval")
	f.StringVar(&policyFile, "policy", "", "trust policy file (required for reuse; without it the test always runs)")
	f.BoolVar(&push, "push", false, "sign and publish the result after running")
	f.BoolVar(&noReuse, "no-reuse", false, "always run, never reuse")
	f.BoolVar(&fingerprintOnly, "fingerprint-only", false, "print the execution fingerprint and exit")
	f.BoolVar(&explain, "explain", false, "print the fingerprint manifest (what was hashed)")
	return cmd
}

func newPushCmd() *cobra.Command {
	var file, repo, key, resultFile string
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Sign and publish a recorded execution result",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if repo == "" {
				return usageError{errors.New("--registry is required")}
			}
			if _, _, err := loadAndFingerprint(file); err != nil {
				return err
			}
			// TODO(milestone-2): read the result recorded by `aval run`, check it
			// matches the current fingerprint, attest.Sign, registry.Push.
			return fmt.Errorf("push: %w", evidence.ErrNotImplemented)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&file, "contract", "f", contract.DefaultFile, "path to the test contract")
	f.StringVar(&repo, "registry", "", "OCI repository for attestations (required)")
	f.StringVar(&key, "key", "", "signing key path or KMS URI (default: keyless OIDC)")
	f.StringVar(&resultFile, "result", ".aval/result.json", "result recorded by 'aval run'")
	return cmd
}

func newVerifyCmd() *cobra.Command {
	var file, repo, policyFile, output string
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Check whether a trusted result exists for the current inputs",
		Long: `verify computes the fingerprint, looks up the attestation and verifies its
signature and the trust policy, without running anything.

Exit codes: 0 verified, 4 miss, 5 verification failed (security event).`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if output != "text" && output != "json" {
				return usageError{fmt.Errorf("--output must be text or json, got %q", output)}
			}
			if repo == "" {
				return usageError{errors.New("--registry is required")}
			}
			c, fp, err := loadAndFingerprint(file)
			if err != nil {
				return err
			}
			report, err := verify(cmd.Context(), c, fp, repo, policyFile)
			if werr := writeReport(cmd.OutOrStdout(), output, report); werr != nil {
				return werr
			}
			return err
		},
	}
	f := cmd.Flags()
	f.StringVarP(&file, "contract", "f", contract.DefaultFile, "path to the test contract")
	f.StringVar(&repo, "registry", "", "OCI repository for attestations (required)")
	f.StringVar(&policyFile, "policy", "", "trust policy file (without it every lookup is a miss)")
	f.StringVarP(&output, "output", "o", "text", "output format: text or json")
	return cmd
}

func verify(ctx context.Context, c *contract.Contract, fp fingerprint.Fingerprint, repo, policyFile string) (evidence.Report, error) {
	r := evidence.Report{Version: evidence.ReportVersion, TestID: c.Metadata.Name, Fingerprint: fp.Digest}
	fail := func(err error) (evidence.Report, error) {
		r.Reason = err.Error()
		switch {
		case errors.Is(err, evidence.ErrVerification):
			r.Outcome = evidence.OutcomeVerificationFailed
		case errors.Is(err, evidence.ErrNotFound):
			r.Outcome = evidence.OutcomeMiss
		default:
			r.Outcome = evidence.OutcomeError
		}
		return r, err
	}

	// Without a policy nothing is trusted, so do not even look: report a miss
	// instead of flagging every attestation found as a security event.
	if policyFile == "" {
		return fail(fmt.Errorf("no trust policy configured (--policy): %w", evidence.ErrNotFound))
	}
	pol, err := loadPolicy(policyFile)
	if err != nil {
		return fail(err)
	}
	desc, err := lookup(ctx, repo, fp.Digest)
	if err != nil {
		return fail(err)
	}
	r.Reference = desc.Reference()
	env, err := fetch(ctx, desc)
	if err != nil {
		return fail(err)
	}
	v, err := verifyEnvelope(ctx, env, pol.VerifyOptions(c.Metadata.Name, fp.Digest))
	if err != nil {
		return fail(err)
	}
	r.Outcome, r.Result, r.Signer, r.Predicate = evidence.OutcomeVerified, v.Predicate.Result, &v.Signer, v.Predicate
	return r, nil
}

func writeReport(w io.Writer, format string, r evidence.Report) error {
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	fmt.Fprintf(w, "%s %s %s\n", r.Outcome, r.TestID, r.Fingerprint)
	if r.Reason != "" {
		fmt.Fprintf(w, "reason: %s\n", r.Reason)
	}
	return nil
}
