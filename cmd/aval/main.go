// Command aval runs, publishes and verifies signed test execution results.
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	orasregistry "oras.land/oras-go/v2/registry"

	"github.com/TrianaLab/aval/internal/attest"
	"github.com/TrianaLab/aval/internal/cliout"
	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/fingerprint"
	"github.com/TrianaLab/aval/internal/policy"
	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/internal/resolve"
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
	ExitNotImplemented     = 6 // reserved, never returned
)

// version is set at release build time (-ldflags "-X main.version=...").
var version = "dev"

func init() { resolve.Version, cliout.Version = version, version }

type usageError struct{ error }

func (u usageError) Unwrap() error { return u.error }

// shown marks an error whose reason the command already printed (in the
// report): run prints no aval: line for it. The exit code still follows it.
// Only return shown directly or as an errors.Join member; wrapping it hides
// the prefix too.
type shown struct{ error }

func (s shown) Unwrap() error { return s.error }

// skipped is a miss where no lookup ran. It is evidence.ErrNotFound for exit
// codes and outcomes, but its message is only its cause.
type skipped string

func (s skipped) Error() string      { return string(s) }
func (skipped) Is(target error) bool { return target == evidence.ErrNotFound }

// reuseOff is a miss because reuse is off: no registry, no trust policy or
// --no-reuse. run's summary says so.
type reuseOff struct{ skipped }

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
	pushOutputs    = registry.PushOutputs
	fetchOutputs   = registry.FetchOutputs
	pushRecord     = registry.Record
	readRecords    = registry.ReadRecords
	now            = time.Now
	// isTerminal: stdin is a terminal, so the interactive login can run.
	isTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
	// login runs the interactive Sigstore login on the terminal.
	login = func() (string, error) {
		return attest.Login(cmp.Or(os.Getenv(oidcIssuerEnv), attest.DefaultOIDCIssuer), cmp.Or(os.Getenv(oidcClientIDEnv), attest.DefaultOIDCClientID), os.Stdin, os.Stderr, nil)
	}
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
	// errMutable: the inputs of a reusable class hold mutable references
	// that scope.allowMutable does not acknowledge (internal/pinning). The
	// fingerprint cannot prove what is behind them, so nothing is reused or
	// published: a miss, never a security event (invariants 3, 4).
	errMutable = errors.New("mutable references in the inputs")
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
	printErrors(stderr, err)
	return exitCode(err)
}

// printErrors prints one aval: line per part of err not already shown.
func printErrors(w io.Writer, err error) {
	if err == nil {
		return
	}
	var l listError
	if errors.As(err, &l) {
		cliout.Problems(w, l.head, l.err)
		return
	}
	for _, p := range errParts(err) {
		fmt.Fprintln(w, "aval:", cliout.OneLine(p))
	}
}

// errParts splits err into one message per errors.Join member, each keeping
// the prefix of the errors that wrap it, and drops members already shown.
// Text inside a member is never split: it may come from an attestation.
func errParts(err error) []string {
	if _, ok := err.(shown); ok {
		return nil
	}
	msg := err.Error()
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		errs := j.Unwrap()
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		if msg == strings.Join(msgs, "\n") { // errors.Join, not fmt.Errorf with several %w
			var parts []string
			for _, e := range errs {
				parts = append(parts, errParts(e)...)
			}
			return parts
		}
	}
	if inner := errors.Unwrap(err); inner != nil && strings.HasSuffix(msg, inner.Error()) {
		prefix := strings.TrimSuffix(msg, inner.Error())
		parts := errParts(inner)
		for i := range parts {
			parts[i] = prefix + parts[i]
		}
		return parts
	}
	return []string{msg}
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
	case errors.Is(err, evidence.ErrNotFound), errors.Is(err, errBelowMinTrust), errors.Is(err, errMutable):
		return ExitMiss
	}
	return ExitError
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "aval",
		Short: "Verifiable test execution reuse: signed test results as OCI artifacts",
		Long: `Verifiable test execution reuse: signed test results as OCI artifacts.

--registry and --policy fall back to $` + registryEnv + ` and $` + policyEnv + `; a flag
wins, and an empty value counts as unset (an empty flag too: use --no-reuse
to turn reuse off). There is no config file.`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.RunE = func(c *cobra.Command, _ []string) error { return c.Help() }
	root.PersistentFlags().Bool("plain", false, "plain aval run output even on a terminal, as in CI (also $"+outputEnv+"=plain or TERM=dumb)")
	root.AddCommand(newInitCmd(), newRunCmd(), newExplainCmd(), newPushCmd(), newKeygenCmd(), newRecordCmd(), newStatsCmd(), newVerifyCmd(), newVersionCmd(), newUpgradeCmd())
	guide(root)
	return root
}

// checkTimeout rejects a deadline that would fail every registry call.
func checkTimeout(d time.Duration) error {
	if d <= 0 {
		return usageError{fmt.Errorf("--registry-timeout must be positive, got %s", d)}
	}
	return nil
}

// repoExample shows the --registry form in usage errors.
const repoExample = "ghcr.io/acme/orders-aval"

// requireRegistry rejects a missing registry, then checks it.
func requireRegistry(repo string) error {
	if repo == "" {
		return usageError{fmt.Errorf("--registry is required (or set $%s)", registryEnv)}
	}
	return checkRegistry(repo)
}

// checkRegistry rejects a --registry that is not an OCI repository (host and
// name, no scheme, tag or digest) before anything runs.
func checkRegistry(repo string) error {
	if scheme, rest, ok := strings.Cut(repo, "://"); ok {
		if r, err := orasregistry.ParseReference(rest); err == nil && r.Reference == "" {
			return usageError{fmt.Errorf("--registry %s is not an OCI repository: drop %s://: try --registry %s", repo, scheme, rest)}
		}
		return usageError{fmt.Errorf("--registry %s is not an OCI repository: drop %s://", repo, scheme)}
	}
	ref, err := orasregistry.ParseReference(repo)
	if err == nil && ref.Reference != "" {
		err = errors.New("a tag or digest is not allowed")
	}
	if err != nil {
		return usageError{fmt.Errorf("--registry %s is not an OCI repository (host/name, no scheme, tag or digest), e.g. %s: %w", repo, repoExample, err)}
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
// the command writes or reads as Aval output (--result-file, --report-file,
// --stats-file); inside the contract root they are not inputs.
func loadAndFingerprint(ctx context.Context, file string, outputs ...string) (*contract.Contract, fingerprint.Fingerprint, error) {
	c, err := contract.Load(file)
	var fe *contract.FileError
	if errors.As(err, &fe) {
		return nil, fingerprint.Fingerprint{}, usageError{listError{fe.File + ": invalid contract:", fe.Err}}
	}
	if err != nil {
		return nil, fingerprint.Fingerprint{}, usageError{err}
	}
	for _, o := range outputs {
		if isInput(filepath.Dir(file), c, o) {
			// Skipping it would silently drop an input from the fingerprint.
			return nil, fingerprint.Fingerprint{}, usageError{fmt.Errorf("output %s (--result-file or --report-file) is an input of the test: the contract scope includes it and it holds neither an Aval result nor a report; keep results and reports under %s/ or outside the contract directory", o, fingerprint.OutputDir)}
		}
	}
	fp, err := fingerprint.Compute(ctx, filepath.Dir(file), c, os.LookupEnv, outputs...)
	if err != nil {
		// Cannot prove input equivalence: callers must run the test.
		return c, fingerprint.Fingerprint{}, fmt.Errorf("fingerprint: %w", err)
	}
	return c, fp, nil
}

// isInput reports whether the output path p holds an input of the test that
// fingerprint.Compute would skip: the scope of c, rooted at dir, includes p,
// and p holds something other than Aval's own result, report or run record
// (a whole-tree scope finds those on the next run, #63).
// ponytail: an input that is itself a valid Aval result, report or run record (a test
// fixture) is taken for an output; name such a fixture as --result-file and the
// run overwrites it anyway.
func isInput(dir string, c *contract.Contract, p string) bool {
	return inScope(dir, c, p) && existsNonAvalOutput(p)
}

// existsNonAvalOutput reports whether something other than Aval's own
// result, report or run record sits at p. Anything that cannot be read or is not a
// regular file counts (fail closed).
func existsNonAvalOutput(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return false // nothing there; writing it fails on its own
	}
	if err != nil || !fi.Mode().IsRegular() {
		return true
	}
	raw, err := os.ReadFile(p)
	return err != nil || !isAvalOutput(raw)
}

// isAvalOutput reports whether raw is exactly a result recorded by 'aval
// run', a verify report or a run record: strict JSON (no unknown fields, nothing after
// it) with the mandatory fields set, so an arbitrary input file is not
// mistaken for one and dropped from the fingerprint.
func isAvalOutput(raw []byte) bool {
	var p evidence.Predicate
	if decodeStrict(raw, &p) == nil && p.Validate() == nil {
		return true
	}
	var rec evidence.RunRecord
	if decodeStrict(raw, &rec) == nil {
		if _, err := evidence.ParseRunRecord(raw); err == nil {
			return true
		}
	}
	var r evidence.Report
	if decodeStrict(raw, &r) != nil || r.Version != evidence.ReportVersion || r.TestID == "" {
		return false
	}
	switch r.Outcome {
	case evidence.OutcomeVerified, evidence.OutcomeMiss, evidence.OutcomeVerificationFailed:
		return r.Fingerprint != ""
	case evidence.OutcomeError:
		// 'aval run --report-file' writes an error report without a fingerprint
		// when it could not compute one.
		return true
	}
	return false
}

// decodeStrict decodes one JSON value from raw into v, rejecting unknown
// fields and trailing data.
func decodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return errors.New("trailing data after the JSON value")
	}
	return nil
}

// inScope reports whether the scope of c, rooted at dir, would hash the file
// at path p if it were not an Aval output: it lies inside dir, is neither a
// .git entry (directory, file or symlink) nor under one, lies outside .aval
// directories, and matches source (not excluded) or dependencies.
// A path that cannot be resolved is not skipped by fingerprint.Compute either.
func inScope(dir string, c *contract.Contract, p string) bool {
	root, err := filepath.Abs(dir)
	abs, aerr := filepath.Abs(p)
	if p == "" || err != nil || aerr != nil {
		return false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || !filepath.IsLocal(rel) {
		return false
	}
	segs := strings.Split(filepath.ToSlash(rel), "/")
	if slices.Contains(segs, ".git") || slices.Contains(segs[:len(segs)-1], fingerprint.OutputDir) {
		return false
	}
	match := func(globs []string, m func(contract.Glob, []string) bool) bool {
		return slices.ContainsFunc(globs, func(g string) bool { return m(contract.SplitGlob(g), segs) })
	}
	return match(c.Scope.Source, contract.Glob.Match) && !match(c.Scope.Exclude, contract.Glob.MatchContents) ||
		match(c.Scope.Dependencies, contract.Glob.Match)
}

func newVerifyCmd() *cobra.Command {
	var output string
	g := &globalFlags{}
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Check whether a trusted result exists for the current inputs",
		Long: `verify computes the fingerprint, looks up the attestation and verifies its
signature and the trust policy, without running anything. It needs a
registry; without a trust policy every lookup is a miss.

Exit codes: 0 verified, 4 miss, 5 verification failed (security event:
also a verified result beside a rejected verification-failed attestation, or
more attestations than the lookup cap), 1 error (registry, policy or
fingerprint), 2 usage or invalid aval.yaml.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			file, repo, timeout := g.file, g.registry, g.timeout
			g.printConfig(cmd.ErrOrStderr()) // stdout stays the frozen report
			if output != "text" && output != "json" {
				return usageError{fmt.Errorf("--output must be text or json, got %q", output)}
			}
			if err := cmp.Or(requireRegistry(repo), checkTimeout(timeout)); err != nil {
				return err
			}
			c, fp, err := loadAndFingerprint(cmd.Context(), file)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			report, _, err := verify(ctx, cmd.ErrOrStderr(), c, file, fp, repo, g.policy)
			if msg := unreachable(repo, err); msg != "" && report.Outcome == evidence.OutcomeError {
				fmt.Fprintln(cmd.ErrOrStderr(), "aval:", cliout.OneLine(msg)) // the report keeps the raw error
			}
			if werr := cliout.WriteReport(cmd.OutOrStdout(), output, report); werr != nil {
				return werr
			}
			if err != nil {
				return shown{err} // the report carries the reason
			}
			return nil
		},
	}
	g.bind(cmd, true, true)
	cmd.Flags().StringVarP(&output, "output", "o", "text", "output format: text or json")
	return cmd
}

// verify decides whether a trusted result exists and returns the outputs
// it carries, fetched and checked (never written). When any attestation
// failed verification it prints one SECURITY line and returns an error
// wrapping evidence.ErrVerification, even beside a verified candidate
// (invariant 4, #195); the reasons are in the report. It does so too when
// the fingerprint holds more attestations than lookup lists.
func verify(ctx context.Context, warn io.Writer, c *contract.Contract, file string, fp fingerprint.Fingerprint, repo, policyFile string) (evidence.Report, []registry.Output, error) {
	r, files, err := lookupAndVerify(ctx, warn, c, file, fp, repo, policyFile)
	if line := securityLineFor(r, err); line != "" {
		fmt.Fprintln(warn, line)
	}
	return r, files, securityErr(r, err)
}

// errRejected is the error of a verified lookup beside an attestation that
// failed verification: a security event, so nothing is reused and the exit
// code is 5 (invariant 4, #195). The report keeps outcome verified.
var errRejected = &evidence.VerificationError{Reason: "an attestation for the fingerprint failed verification beside the verified one"}

// securityErr is err, or errRejected when err is nil but r holds a rejected
// verification-failed attestation.
func securityErr(r evidence.Report, err error) error {
	if err == nil && securityCount(r) > 0 {
		return errRejected
	}
	return err
}

// reusable reports whether the lookup r, with its error after securityErr,
// may be reused: verified, and no attestation failed verification.
func reusable(r evidence.Report, err error) bool {
	return r.Outcome == evidence.OutcomeVerified && err == nil
}

// securityLineFor is the SECURITY line for the lookup r and its error, or
// empty. Over the candidate cap nothing was verified, so an untrusted
// attestation may hide among them (#135): the outcome is
// verification-failed and the line names the cap.
func securityLineFor(r evidence.Report, err error) string {
	if errors.Is(err, registry.ErrTooManyCandidates) {
		return cliout.FloodLine(registry.ErrTooManyCandidates, r.TestID)
	}
	if n := securityCount(r); n > 0 {
		return cliout.SecurityLine(n, r.TestID)
	}
	return ""
}

// securityCount is how many attestations in r failed verification.
func securityCount(r evidence.Report) int {
	n := 0
	for _, rj := range r.Rejected {
		if rj.Outcome == evidence.OutcomeVerificationFailed {
			n++
		}
	}
	if n == 0 && r.Outcome == evidence.OutcomeVerificationFailed {
		n = 1 // the lookup itself failed verification (a moved tag)
	}
	return n
}

func lookupAndVerify(ctx context.Context, warn io.Writer, c *contract.Contract, file string, fp fingerprint.Fingerprint, repo, policyFile string) (evidence.Report, []registry.Output, error) {
	r := newReport(c, fp)
	fail := func(err error) (evidence.Report, []registry.Output, error) {
		r, err := failReport(r, err)
		return r, nil, err
	}

	// Never reusable, so there is nothing to look up (invariant 7).
	if c.Cacheability == contract.NonDeterministic {
		return fail(skipped(fmt.Sprintf("cacheability %s is never reusable", c.Cacheability)))
	}
	if err := mutable(c, fp); err != nil {
		return fail(err)
	}
	// Without a policy nothing is trusted, so do not even look: report a miss
	// instead of flagging every attestation found as a security event.
	if policyFile == "" {
		return fail(skipped("no trust policy configured: pass --policy to reuse verified results"))
	}
	pol, err := loadPolicy(policyFile)
	if err != nil {
		return fail(err)
	}
	// A class the policy does not allow is routine non-reuse, not a security
	// event, so do not look (invariant 4).
	if !c.Cacheability.ReusableByDefault() && !slices.Contains(pol.AllowedCacheability, string(c.Cacheability)) {
		return fail(skipped(fmt.Sprintf("cacheability %s is not in allowedCacheability of the trust policy %s", c.Cacheability, policyFile)))
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
	best, ref, rejected, err := pick(ctx, warn, cands, pol.VerifyOptions(c.Metadata.Name, fp.Digest), sigs, c, file, policyFile)
	r.Reference, r.Rejected = ref, rejected
	if err != nil {
		return fail(err)
	}
	r.Outcome, r.Result, r.Signer, r.Predicate = evidence.OutcomeVerified, best.Predicate.Result, &best.Signer, best.Predicate
	return r, best.files, nil
}

// mutable returns errMutable, listing each reference, when c is of a class
// reusable by default and its inputs hold mutable references that
// scope.allowMutable does not acknowledge. For the other classes findings
// are information only: they are not reusable by default anyway.
func mutable(c *contract.Contract, fp fingerprint.Fingerprint) error {
	u := fp.Unpinned()
	if !c.Cacheability.ReusableByDefault() || len(u) == 0 {
		return nil
	}
	refs := make([]string, len(u))
	for i, f := range u {
		refs[i] = f.String()
	}
	return fmt.Errorf("%w (%d), so the fingerprint cannot prove what is behind them: %s; pin each one or acknowledge it in scope.allowMutable (see docs/pinning.md)",
		errMutable, len(u), strings.Join(refs, "; "))
}

func newReport(c *contract.Contract, fp fingerprint.Fingerprint) evidence.Report {
	return evidence.Report{Version: evidence.ReportVersion, TestID: c.Metadata.Name, Fingerprint: fp.Digest}
}

func failReport(r evidence.Report, err error) (evidence.Report, error) {
	r.Reason, r.Outcome = reason(err), outcomeOf(err)
	return r, err
}

// reason is the report text for err: joined errors are separated by "; ".
func reason(err error) string { return strings.Join(errParts(err), "; ") }

// outcomeOf maps an error to its report outcome; miss and verification
// failure stay distinct (invariant 4).
func outcomeOf(err error) evidence.Outcome {
	switch {
	case errors.Is(err, evidence.ErrVerification):
		return evidence.OutcomeVerificationFailed
	case errors.Is(err, evidence.ErrNotFound), errors.Is(err, errBelowMinTrust), errors.Is(err, errMutable):
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
// candidates are returned for the report, never dropped (invariant 4); verify
// prints a SECURITY line for them even beside a verified candidate. A candidate below the policy's minimum trust level is
// rejected as a miss: it was evaluated, so it does not block a sibling. A
// listed candidate whose manifest is gone (deleted from the registry) is
// absent: it is skipped with a warning, and when every candidate is gone the
// outcome is a miss. Whenever no candidate is reused it returns the reference
// and error of the first verification failure, else of the first error, else
// of the first below-minimum candidate: the security event must surface, and
// an undecidable candidate outranks routine non-reuse.
func pick(ctx context.Context, warn io.Writer, cands []registry.Descriptor, opts evidence.VerifyOptions, sigs *attest.Verifier, c *contract.Contract, file, policyFile string) (*hit, string, []evidence.Rejection, error) {
	var (
		best             *hit
		bestRef, failRef string
		failErr          error
		unknown          bool // a candidate could not be evaluated
		rejected         []evidence.Rejection
	)
	for _, d := range cands {
		v, err := verifyCandidate(ctx, d, opts, sigs, c, file, policyFile)
		if errors.Is(err, evidence.ErrNotFound) && !errors.Is(err, evidence.ErrVerification) {
			fmt.Fprintf(warn, "aval: warning: listed attestation %s is gone, skipping it: %s\n", d.Reference(), cliout.OneLine(err.Error()))
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
		rejected = append(rejected, evidence.Rejection{Reference: d.Reference(), Outcome: outcomeOf(err), Reason: reason(err)})
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
func outranks(v, best *hit) bool {
	return best == nil || v.Signer.TrustLevel > best.Signer.TrustLevel ||
		(v.Signer.TrustLevel == best.Signer.TrustLevel && best.Predicate.Result == evidence.ResultPassed && v.Predicate.Result != evidence.ResultPassed)
}

// verifyCandidate fetches one attestation and verifies it with the Sigstore
// bundle stored next to its envelope. The signed cacheability must equal the
// local contract's: the fingerprint covers the contract, so a different class
// under the same fingerprint is a lie, never a hit (invariant 7). The policy's
// minimum trust level is checked last: an authentic attestation from a listed
// signer below it is routine non-reuse (errBelowMinTrust, a miss), but only
// once every claim checks out; a false claim stays a verification failure,
// and names the policy file it was checked against.
func verifyCandidate(ctx context.Context, d registry.Descriptor, opts evidence.VerifyOptions, sigs *attest.Verifier, c *contract.Contract, file, policyFile string) (*hit, error) {
	v, err := checkCandidate(ctx, d, opts, sigs, c, file)
	if errors.Is(err, evidence.ErrVerification) {
		err = fmt.Errorf("%w (trust policy %s)", err, policyFile)
	}
	return v, err
}

// hit is a verified candidate and the outputs it carries.
type hit struct {
	*evidence.Verification
	files []registry.Output
}

func checkCandidate(ctx context.Context, d registry.Descriptor, opts evidence.VerifyOptions, sigs *attest.Verifier, c *contract.Contract, file string) (*hit, error) {
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
	case v.Predicate.Test.Cacheability != string(c.Cacheability):
		return nil, &evidence.VerificationError{Reason: fmt.Sprintf("cacheability %q, want %q", v.Predicate.Test.Cacheability, c.Cacheability)}
	}
	// ponytail: fetches the outputs of every candidate, even one later
	// below the minimum trust level or outranked; ceiling is the candidate
	// cap times MaxOutputTotal. Fetch only the chosen best if that bites.
	files, err := verifyOutputs(ctx, d.Repository, c, file, v.Predicate.Execution.Outputs)
	switch {
	case err != nil:
		return nil, err
	case v.Signer.TrustLevel < need:
		return nil, fmt.Errorf("%w: signer %q has trust level %s, policy requires at least %s", errBelowMinTrust, cmp.Or(v.Signer.KeyID, v.Signer.Subject), v.Signer.TrustLevel, need)
	}
	return &hit{v, files}, nil
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
