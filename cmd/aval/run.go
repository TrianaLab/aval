package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/TrianaLab/aval/internal/attest"
	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/fingerprint"
	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// defaultResult is where `aval run` records a result for `aval push`.
const defaultResult = ".aval/result.json"

// idTokenEnv holds the OIDC token for keyless signing (the Sigstore tooling
// convention). Obtaining it is the caller's job, so no CI vendor is assumed.
const idTokenEnv = "SIGSTORE_ID_TOKEN"

type runFlags struct {
	file, repo, policyFile, resultFile, reportFile, key string
	push, noReuse, shadow, fingerprintOnly, explain     bool
}

func newRunCmd() *cobra.Command {
	var o runFlags
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Reuse a verified result for the current inputs, or run the test",
		Long: `run computes the execution fingerprint of the contract, looks for a signed
attestation in the registry, and reuses it if it verifies against the trust
policy. Otherwise it runs runner.command, records the result (--result) and,
with --push, signs and publishes it.

Anything that cannot be proven runs the test: a fingerprint error, a registry
or policy error, a miss. A verification failure also runs the test, and then
exits 5: it is a security event. A reused failing result exits 3, like a
failing run. --shadow verifies and still runs the test; if the verified
result disagrees with the run, that is a false hit and run exits 1.

Exit codes: 0 passed or reused a pass, 3 failed or reused a failure,
5 verification failed (after running the test), 1 error or false hit,
2 usage. Keyless signing reads the OIDC token from $` + idTokenEnv + `.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runTest(cmd, o) },
	}
	f := cmd.Flags()
	f.StringVarP(&o.file, "contract", "f", contract.DefaultFile, "path to the test contract")
	f.StringVar(&o.repo, "registry", "", "OCI repository for attestations, e.g. ghcr.io/acme/orders-aval")
	f.StringVar(&o.policyFile, "policy", "", "trust policy file (required for reuse; without it the test always runs)")
	f.BoolVar(&o.push, "push", false, "sign and publish the result after running (needs --registry)")
	f.StringVar(&o.key, "key", "", "signing key path for --push (default: keyless OIDC)")
	f.StringVar(&o.resultFile, "result", defaultResult, "where to record the result for 'aval push'")
	f.StringVar(&o.reportFile, "report", "", "also write the lookup decision as a verify report (JSON) to this file")
	f.BoolVar(&o.noReuse, "no-reuse", false, "always run, never reuse")
	f.BoolVar(&o.shadow, "shadow", false, "verify, but run the test anyway and fail on a false hit")
	f.BoolVar(&o.fingerprintOnly, "fingerprint-only", false, "print the execution fingerprint and exit")
	f.BoolVar(&o.explain, "explain", false, "print the fingerprint manifest (what was hashed)")
	return cmd
}

func (o runFlags) validate() error {
	switch {
	case o.push && o.repo == "":
		return usageError{errors.New("--push requires --registry")}
	case o.shadow && o.noReuse:
		return usageError{errors.New("--shadow and --no-reuse exclude each other")}
	}
	return nil
}

func runTest(cmd *cobra.Command, o runFlags) error {
	if err := o.validate(); err != nil {
		return err
	}
	c, fp, fpErr := loadAndFingerprint(o.file)
	if c == nil {
		return fpErr
	}
	ctx, out, warn := cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	if o.explain || o.fingerprintOnly {
		if err := printFingerprint(out, fp, fpErr, o); err != nil || o.fingerprintOnly {
			return err
		}
	}
	r, lookupErr := decide(ctx, out, warn, c, fp, fpErr, o)
	_ = writeReport(out, "text", r) // text never fails, like the other lines printed
	if o.reportFile != "" {
		if err := writeReportFile(o.reportFile, r); err != nil {
			return err
		}
	}
	if r.Outcome == evidence.OutcomeVerified && !o.shadow {
		fmt.Fprintf(out, "reused %s result %s\n", r.Result, r.Reference)
		if r.Result != evidence.ResultPassed {
			return fmt.Errorf("reused result %s: %w", r.Result, errTestFailed)
		}
		return nil
	}
	// A security event: run the test, then exit 5 (invariant 4).
	var errs []error
	if errors.Is(lookupErr, evidence.ErrVerification) {
		fmt.Fprintf(warn, "aval: SECURITY: attestation verification failed for %s: %v; running the test\n", c.Metadata.Name, lookupErr)
		errs = append(errs, lookupErr)
	}
	p, err := execute(ctx, out, warn, c, filepath.Dir(o.file), fp)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	fmt.Fprintf(out, "ran %s: %s (exit %d)\n", c.Metadata.Name, p.Result, p.Execution.ExitCode)
	errs = append(errs, keep(ctx, out, p, fpErr, o)...)
	return errors.Join(append(errs, judge(out, warn, r, p))...)
}

// printFingerprint handles --explain and --fingerprint-only. Both need the
// fingerprint, so its error is final here. --explain alone goes on to run.
func printFingerprint(out io.Writer, fp fingerprint.Fingerprint, fpErr error, o runFlags) error {
	if fpErr != nil {
		return fpErr
	}
	if o.explain {
		fmt.Fprint(out, fp.Manifest)
	}
	if o.fingerprintOnly {
		fmt.Fprintln(out, fp.Digest)
	}
	return nil
}

// judge turns the real run into the command's verdict. With a verified
// result (only in --shadow) a disagreement is a false hit.
func judge(out, warn io.Writer, r evidence.Report, p *evidence.Predicate) error {
	if r.Outcome == evidence.OutcomeVerified {
		if r.Result != p.Result {
			fmt.Fprintf(warn, "aval: FALSE HIT: %s reused %s from %s, but the test %s\n", r.TestID, r.Result, r.Reference, p.Result)
			return errFalseHit
		}
		fmt.Fprintf(out, "shadow: reuse of %s would have been correct (%s)\n", r.Reference, p.Result)
	}
	if p.Result != evidence.ResultPassed {
		return errTestFailed
	}
	return nil
}

// decide looks up and verifies a result unless reuse is impossible or
// disabled. Every way of not reusing comes back as a report, never silently.
func decide(ctx context.Context, out, warn io.Writer, c *contract.Contract, fp fingerprint.Fingerprint, fpErr error, o runFlags) (evidence.Report, error) {
	if fpErr == nil {
		fmt.Fprintf(out, "test %s fingerprint %s\n", c.Metadata.Name, fp.Digest)
	}
	if !c.Cacheability.ReusableByDefault() {
		fmt.Fprintf(out, "cacheability %s: not reusable by default\n", c.Cacheability)
	}
	r := newReport(c, fp)
	switch {
	case fpErr != nil:
		return failReport(r, fpErr)
	case o.noReuse:
		return failReport(r, fmt.Errorf("reuse disabled (--no-reuse): %w", evidence.ErrNotFound))
	case o.repo == "":
		return failReport(r, fmt.Errorf("no registry configured (--registry): %w", evidence.ErrNotFound))
	}
	return verify(ctx, warn, c, fp, o.repo, o.policyFile)
}

// execute runs runner.command in the contract's directory and returns the
// unsigned predicate for the run. A command that cannot start is an error,
// not a failed test.
func execute(ctx context.Context, stdout, stderr io.Writer, c *contract.Contract, dir string, fp fingerprint.Fingerprint) (*evidence.Predicate, error) {
	argv := c.Runner.Command
	x := exec.CommandContext(ctx, argv[0], argv[1:]...)
	x.Dir, x.Stdout, x.Stderr = dir, stdout, stderr
	start := time.Now().UTC()
	err := x.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return nil, fmt.Errorf("running %q: %w", argv[0], err)
	}
	code := x.ProcessState.ExitCode()
	env := map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH}
	maps.Copy(env, c.Environment.Toolchain)
	host, _ := os.Hostname()
	result := evidence.ResultPassed
	if code != 0 {
		result = evidence.ResultFailed
	}
	return &evidence.Predicate{
		SchemaVersion: evidence.SchemaVersion,
		// TODO(milestone-3): flags to record the tested git commit or image as the subject.
		Subject:    evidence.Subject{Name: c.Metadata.Name, Digest: map[string]string{"sha256": fp.Hex()}},
		Test:       evidence.Test{ID: c.Metadata.Name, Cacheability: string(c.Cacheability)},
		Result:     result,
		Execution:  evidence.Execution{Fingerprint: fp.Digest, FingerprintVersion: fingerprint.Version, Environment: env, ExitCode: code},
		Runner:     evidence.Runner{Identity: cmp.Or(host, "unknown")},
		StartedAt:  start,
		FinishedAt: time.Now().UTC(),
	}, nil
}

// keep records the result and, with --push, publishes it. Without a
// fingerprint there is nothing a result could be reused for.
func keep(ctx context.Context, out io.Writer, p *evidence.Predicate, fpErr error, o runFlags) []error {
	if fpErr != nil {
		if o.push {
			return []error{fmt.Errorf("result not published: %w", fpErr)}
		}
		return nil
	}
	if err := writeJSON(o.resultFile, p); err != nil {
		return []error{fmt.Errorf("recording result: %w", err)}
	}
	if !o.push {
		return nil
	}
	ref, err := signAndPush(ctx, p, o.repo, o.key)
	if err != nil {
		return []error{err}
	}
	fmt.Fprintf(out, "pushed %s\n", ref)
	return nil
}

func writeReportFile(path string, r evidence.Report) error {
	if err := writeJSON(path, r); err != nil {
		return fmt.Errorf("writing report: %w", err)
	}
	return nil
}

func writeJSON(path string, v any) error {
	raw, _ := json.MarshalIndent(v, "", "  ") // ponytail: plain structs, cannot fail
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// signAndPush signs the predicate (key-based with key, else keyless with the
// token in $SIGSTORE_ID_TOKEN) and publishes it under its fingerprint.
func signAndPush(ctx context.Context, p *evidence.Predicate, repo, key string) (string, error) {
	st, err := attest.NewStatement(p)
	var s *attest.Signed
	if err == nil {
		s, err = sign(ctx, st, attest.SignOptions{Key: key, IDToken: os.Getenv(idTokenEnv)})
	}
	if err != nil {
		return "", fmt.Errorf("signing: %w", err)
	}
	d, err := publish(ctx, repo, p.Execution.Fingerprint, registry.Attestation{Envelope: s.Envelope, Bundle: s.Bundle})
	if err != nil {
		return "", fmt.Errorf("publishing: %w", err)
	}
	return d.Reference(), nil
}

func newPushCmd() *cobra.Command {
	var file, repo, key, resultFile string
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Sign and publish a recorded execution result",
		Long: `push signs the result recorded by 'aval run' and publishes it. The result
must be for the current inputs: same test id, cacheability and fingerprint.
Keyless signing reads the OIDC token from $` + idTokenEnv + `.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if repo == "" {
				return usageError{errors.New("--registry is required")}
			}
			c, fp, err := loadAndFingerprint(file)
			if err != nil {
				return err
			}
			raw, err := os.ReadFile(resultFile)
			if err != nil {
				return fmt.Errorf("reading the result recorded by 'aval run': %w", err)
			}
			p, err := evidence.Parse(raw)
			if err != nil {
				return fmt.Errorf("recorded result %s: %w", resultFile, err)
			}
			if p.Execution.Fingerprint != fp.Digest || p.Test.ID != c.Metadata.Name || p.Test.Cacheability != string(c.Cacheability) {
				return fmt.Errorf("recorded result is for test %q (%s) with fingerprint %s; the contract is %q (%s) with fingerprint %s: run the test again",
					p.Test.ID, p.Test.Cacheability, p.Execution.Fingerprint, c.Metadata.Name, c.Cacheability, fp.Digest)
			}
			ref, err := signAndPush(cmd.Context(), p, repo, key)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "pushed %s\n", ref)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&file, "contract", "f", contract.DefaultFile, "path to the test contract")
	f.StringVar(&repo, "registry", "", "OCI repository for attestations (required)")
	f.StringVar(&key, "key", "", "signing key path (default: keyless OIDC)")
	f.StringVar(&resultFile, "result", defaultResult, "result recorded by 'aval run'")
	return cmd
}
