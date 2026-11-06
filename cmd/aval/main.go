// Command aval runs, publishes and verifies signed test execution results.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/TrianaLab/aval/internal/attest"
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
	ExitOK                 = 0 // test passed, or a verified passing result was reused (verify: any verified result)
	ExitError              = 1 // unexpected error (registry down, I/O, ...), or a false hit in run --shadow
	ExitUsage              = 2 // bad flags or invalid aval.yaml
	ExitTestFailed         = 3 // the test ran and failed, or run reused a verified failing result
	ExitMiss               = 4 // no attestation for this fingerprint that the policy can reuse
	ExitVerificationFailed = 5 // attestation exists but failed signature/policy: security event
	ExitNotImplemented     = 6 // feature lands in a later milestone
)

// version is set at release build time (-ldflags "-X main.version=...").
var version = "dev"

type usageError struct{ error }

// ponytail: package-level seams so tests can drive every outcome without a
// registry or Sigstore; inject dependencies if the CLI grows a library API.
var (
	loadPolicy     = policy.Load
	lookup         = registry.Lookup
	fetch          = registry.Fetch
	verifyEnvelope = evidence.Verify
	newVerifier    = attest.NewVerifier
	sign           = attest.Sign
	publish        = registry.Push
)

var (
	// errTestFailed: the test ran and failed, or a verified failing result
	// was reused (exit 3).
	errTestFailed = errors.New("test failed")
	// errFalseHit: run --shadow found a verified result that disagrees with
	// the real run.
	errFalseHit = errors.New("false hit: the verified result disagrees with the real run")
	// errBelowMinTrust: an authentic attestation from a signer the policy
	// lists, below its minTrustLevel. Routine non-reuse, a miss, never a
	// security event (invariant 4).
	errBelowMinTrust = errors.New("not reusable under the trust policy")
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
		fmt.Fprintln(stderr, "aval:", oneLine(err.Error()))
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
	case errors.Is(err, errTestFailed):
		return ExitTestFailed
	case errors.Is(err, evidence.ErrNotFound), errors.Is(err, errBelowMinTrust):
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
		Version:       version,
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

// registryTimeoutFlag adds --registry-timeout: the deadline for the registry
// calls of one command (lookup and fetch together, push on its own). On
// expiry the outcome is error, so run still runs the test (invariant 3).
func registryTimeoutFlag(cmd *cobra.Command, d *time.Duration) {
	cmd.Flags().DurationVar(d, "registry-timeout", time.Minute, "deadline for this command's registry calls (lookup and fetch share one; push has its own)")
}

// checkTimeout rejects a deadline that would fail every registry call.
func checkTimeout(d time.Duration) error {
	if d <= 0 {
		return usageError{fmt.Errorf("--registry-timeout must be positive, got %s", d)}
	}
	return nil
}

// noArgs is cobra.NoArgs with exit code 2 (usage) instead of 1.
func noArgs(c *cobra.Command, args []string) error {
	if err := cobra.NoArgs(c, args); err != nil {
		return usageError{err}
	}
	return nil
}

// loadAndFingerprint loads the contract and computes its fingerprint, rooted
// at the contract's directory. When only the fingerprint fails it still
// returns the contract, so run can execute the test. outputs are the files
// the command writes or reads as Aval output (--result, --report); inside
// the contract root they are not inputs.
func loadAndFingerprint(file string, outputs ...string) (*contract.Contract, fingerprint.Fingerprint, error) {
	c, err := contract.Load(file)
	if err != nil {
		return nil, fingerprint.Fingerprint{}, usageError{err}
	}
	fp, err := fingerprint.Compute(filepath.Dir(file), c, os.LookupEnv, outputs...)
	if err != nil {
		// Cannot prove input equivalence: callers must run the test.
		return c, fingerprint.Fingerprint{}, fmt.Errorf("fingerprint: %w", err)
	}
	return c, fp, nil
}

func newVerifyCmd() *cobra.Command {
	var file, repo, policyFile, output string
	var timeout time.Duration
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
			if err := checkTimeout(timeout); err != nil {
				return err
			}
			c, fp, err := loadAndFingerprint(file)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			report, err := verify(ctx, cmd.ErrOrStderr(), c, fp, repo, policyFile)
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
	registryTimeoutFlag(cmd, &timeout)
	return cmd
}

func verify(ctx context.Context, warn io.Writer, c *contract.Contract, fp fingerprint.Fingerprint, repo, policyFile string) (evidence.Report, error) {
	r := newReport(c, fp)
	fail := func(err error) (evidence.Report, error) { return failReport(r, err) }

	// Never reusable, so there is nothing to look up (invariant 7).
	if c.Cacheability == contract.NonDeterministic {
		return fail(fmt.Errorf("cacheability %s is never reusable: %w", c.Cacheability, evidence.ErrNotFound))
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
	// A class the policy does not allow is routine non-reuse, not a security
	// event, so do not look (invariant 4).
	if !c.Cacheability.ReusableByDefault() && !slices.Contains(pol.AllowedCacheability, string(c.Cacheability)) {
		return fail(fmt.Errorf("cacheability %s is not reusable under the policy: %w", c.Cacheability, evidence.ErrNotFound))
	}
	sigs, err := newVerifier(pol.TrustedRoot, pol.Identities)
	if err != nil {
		return fail(fmt.Errorf("trust policy: %w", err))
	}
	cands, err := lookup(ctx, repo, fp.Digest)
	if err == nil && len(cands) == 0 {
		err = evidence.ErrNotFound // fail closed on an empty answer
	}
	if err != nil {
		return fail(err)
	}
	best, ref, rejected, err := pick(ctx, warn, cands, pol.VerifyOptions(c.Metadata.Name, fp.Digest), sigs, string(c.Cacheability))
	r.Reference, r.Rejected = ref, rejected
	if err != nil {
		return fail(err)
	}
	r.Outcome, r.Result, r.Signer, r.Predicate = evidence.OutcomeVerified, best.Predicate.Result, &best.Signer, best.Predicate
	return r, nil
}

func newReport(c *contract.Contract, fp fingerprint.Fingerprint) evidence.Report {
	return evidence.Report{Version: evidence.ReportVersion, TestID: c.Metadata.Name, Fingerprint: fp.Digest}
}

func failReport(r evidence.Report, err error) (evidence.Report, error) {
	r.Reason, r.Outcome = err.Error(), outcomeOf(err)
	return r, err
}

// outcomeOf maps an error to its report outcome; miss and verification
// failure stay distinct (invariant 4).
func outcomeOf(err error) evidence.Outcome {
	switch {
	case errors.Is(err, evidence.ErrVerification):
		return evidence.OutcomeVerificationFailed
	case errors.Is(err, evidence.ErrNotFound), errors.Is(err, errBelowMinTrust):
		return evidence.OutcomeMiss
	default:
		return evidence.OutcomeError
	}
}

// pick verifies every candidate and lets the policy choose: the verified one
// with the highest trust level wins. On a tie a non-passed result wins, then
// the lowest digest (the lookup order), so disagreeing trusted runs never
// reuse a pass by accident (fail closed). Candidates that fail verification
// never decide the outcome while another verifies; a candidate that ends in
// any other error makes the outcome an error (run the test), because it might
// have outranked or contradicted the verified one (invariant 3). Rejected
// candidates are returned for the report and printed on warn, never dropped
// (invariant 4); a verification failure is printed as SECURITY even beside a
// verified candidate. A candidate below the policy's minimum trust level is
// rejected as a miss: it was evaluated, so it does not block a sibling. A
// listed candidate whose manifest is gone (deleted from the registry) is
// absent: it is skipped with a warning, and when every candidate is gone the
// outcome is a miss. Whenever no candidate is reused it returns the reference
// and error of the first verification failure, else of the first error, else
// of the first below-minimum candidate: the security event must surface, and
// an undecidable candidate outranks routine non-reuse.
func pick(ctx context.Context, warn io.Writer, cands []registry.Descriptor, opts evidence.VerifyOptions, sigs *attest.Verifier, cacheability string) (*evidence.Verification, string, []evidence.Rejection, error) {
	var (
		best             *evidence.Verification
		bestRef, failRef string
		failErr          error
		unknown          bool // a candidate could not be evaluated
		rejected         []evidence.Rejection
	)
	for _, d := range cands {
		v, err := verifyCandidate(ctx, d, opts, sigs, cacheability)
		if errors.Is(err, evidence.ErrNotFound) && !errors.Is(err, evidence.ErrVerification) {
			fmt.Fprintf(warn, "aval: warning: listed attestation %s is gone, skipping it: %s\n", d.Reference(), oneLine(err.Error()))
			continue
		}
		if err == nil {
			if outranks(v, best) {
				best, bestRef = v, d.Reference()
			}
			continue
		}
		if failErr == nil || severity(err) > severity(failErr) {
			failErr, failRef = err, d.Reference()
		}
		// Only a fully evaluated rejection (a verification failure or a
		// below-minimum signer) lets a verified sibling win; anything else
		// might have outranked it (invariant 3).
		unknown = unknown || (!errors.Is(err, evidence.ErrVerification) && !errors.Is(err, errBelowMinTrust))
		rejected = append(rejected, evidence.Rejection{Reference: d.Reference(), Outcome: outcomeOf(err), Reason: err.Error()})
		prefix := "warning"
		if errors.Is(err, evidence.ErrVerification) {
			prefix = "SECURITY"
		}
		fmt.Fprintf(warn, "aval: %s: rejected attestation %s: %s\n", prefix, d.Reference(), oneLine(err.Error()))
	}
	if best == nil && failErr == nil {
		return nil, "", nil, fmt.Errorf("every listed attestation is gone: %w", evidence.ErrNotFound)
	}
	if best == nil || unknown {
		return nil, failRef, rejected, failErr
	}
	return best, bestRef, rejected, nil
}

// outranks reports whether v beats the best verified candidate so far: a
// higher trust level, or the same level with a non-passed result over a pass.
func outranks(v, best *evidence.Verification) bool {
	return best == nil || v.Signer.TrustLevel > best.Signer.TrustLevel ||
		(v.Signer.TrustLevel == best.Signer.TrustLevel && best.Predicate.Result == evidence.ResultPassed && v.Predicate.Result != evidence.ResultPassed)
}

// verifyCandidate fetches one attestation and verifies it with the Sigstore
// bundle stored next to its envelope. The signed cacheability must equal the
// local contract's: the fingerprint covers the contract, so a different class
// under the same fingerprint is a lie, never a hit (invariant 7). The policy's
// minimum trust level is checked last: an authentic attestation from a listed
// signer below it is routine non-reuse (errBelowMinTrust, a miss), but only
// once every claim checks out; a false claim stays a verification failure.
func verifyCandidate(ctx context.Context, d registry.Descriptor, opts evidence.VerifyOptions, sigs *attest.Verifier, cacheability string) (*evidence.Verification, error) {
	a, err := fetch(ctx, d)
	if err != nil {
		return nil, err
	}
	// A trusted root that cannot be loaded says nothing about the
	// candidate: outcome error, never verification-failed.
	if opts.Signature, err = sigs.For(a.Bundle); err != nil {
		return nil, fmt.Errorf("trust policy: %w", err)
	}
	need := max(opts.MinTrustLevel, evidence.TrustDeveloper)
	opts.MinTrustLevel = evidence.TrustDeveloper // Verify still rejects levels outside developer..isolated
	v, err := verifyEnvelope(ctx, a.Envelope, opts)
	switch {
	case err != nil:
		return nil, err
	case v.Predicate.Test.Cacheability != cacheability:
		return nil, &evidence.VerificationError{Reason: fmt.Sprintf("cacheability %q, want %q", v.Predicate.Test.Cacheability, cacheability)}
	case v.Signer.TrustLevel < need:
		return nil, fmt.Errorf("%w: signer %q has trust level %s, policy requires at least %s", errBelowMinTrust, cmp.Or(v.Signer.KeyID, v.Signer.Subject), v.Signer.TrustLevel, need)
	}
	return v, nil
}

// severity ranks why a candidate was rejected: a verification failure, then
// an error, then a miss (below the minimum trust level).
func severity(err error) int {
	switch outcomeOf(err) {
	case evidence.OutcomeVerificationFailed:
		return 2
	case evidence.OutcomeError:
		return 1
	}
	return 0
}

func writeReport(w io.Writer, format string, r evidence.Report) error {
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	fmt.Fprintf(w, "%s %s %s\n", r.Outcome, r.TestID, r.Fingerprint)
	if r.Reason != "" {
		fmt.Fprintf(w, "reason: %s\n", oneLine(r.Reason))
	}
	for _, rj := range r.Rejected {
		fmt.Fprintf(w, "rejected: %s %s: %s\n", rj.Reference, rj.Outcome, oneLine(rj.Reason))
	}
	return nil
}

// oneLine applies Go string literal escapes, without the surrounding quotes:
// newlines, control and non-printable characters, quotes and backslashes. Reasons can carry text from an untrusted
// attestation; escaped, it cannot start a line of its own in a log, where a
// CI system might read it as a command, or move a terminal cursor.
func oneLine(s string) string {
	q := strconv.Quote(s)
	return q[1 : len(q)-1]
}
