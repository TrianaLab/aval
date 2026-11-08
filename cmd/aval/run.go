package main

import (
	"cmp"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"github.com/spf13/cobra"

	"github.com/TrianaLab/aval/internal/attest"
	"github.com/TrianaLab/aval/internal/cliout"
	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/fingerprint"
	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/internal/resolve"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// defaultResult is where `aval run` records a result for `aval push`.
const defaultResult = ".aval/result.json"

// idTokenEnv holds the OIDC token for keyless signing (the Sigstore tooling
// convention). Obtaining it is the caller's job, so no CI vendor is assumed.
const idTokenEnv = "SIGSTORE_ID_TOKEN"

// Without a key or $SIGSTORE_ID_TOKEN, keyless signing can get its token
// from the interactive Sigstore login in the browser. These override its
// public defaults.
const (
	oidcIssuerEnv   = "SIGSTORE_OIDC_ISSUER"
	oidcClientIDEnv = "SIGSTORE_OIDC_CLIENT_ID"
)

type runFlags struct {
	file, repo, policyFile, resultFile, reportFile, key string
	recordFile, ciRunURL, repository                    string
	push, noReuse, shadow, fingerprintOnly, explain     bool
	record, login                                       bool
	registryTimeout                                     time.Duration
}

func newRunCmd() *cobra.Command {
	var o runFlags
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Reuse a verified result for the current inputs, or run the test",
		Long: `run computes the fingerprint, looks up a signed result and reuses it if the
trust policy accepts it. Otherwise it runs runner.command, records the result
(--result) and, with --push, signs and publishes it.

Anything Aval cannot prove runs the test. A verification failure runs the
test and exits 5: a security event. --shadow runs the test even after a
verified result and exits 1 if they disagree (a false hit).

--record publishes a run record for statistics; a failure is only a warning.
--record-file writes it to a file for 'aval record'.

Exit codes: 0 pass, 3 fail, 5 verification failed, 1 error or false hit,
2 usage. Keyless signing reads $` + idTokenEnv + `; runner.command never sees
it.
` + loginHelp + `.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runTest(cmd, o) },
	}
	f := cmd.Flags()
	f.StringVarP(&o.file, "contract", "f", contract.DefaultFile, "path to the test contract")
	f.StringVar(&o.repo, "registry", "", "OCI repository for attestations, e.g. ghcr.io/acme/orders-aval")
	f.StringVar(&o.policyFile, "policy", "", "trust policy file (required for reuse; without it the test always runs)")
	f.BoolVar(&o.push, "push", false, "sign and publish the result after running (needs --registry)")
	f.StringVar(&o.key, "key", "", "signing key path for --push (default: keyless OIDC)")
	f.BoolVar(&o.login, "login", false, loginFlagHelp)
	f.StringVar(&o.resultFile, "result", defaultResult, "where to record the result for 'aval push'")
	f.StringVar(&o.reportFile, "report", "", "also write the lookup decision as a verify report (JSON) to this file")
	f.BoolVar(&o.noReuse, "no-reuse", false, "always run, never reuse")
	f.BoolVar(&o.shadow, "shadow", false, "verify, but run the test anyway and fail on a false hit")
	f.BoolVar(&o.fingerprintOnly, "fingerprint-only", false, "print the execution fingerprint and exit")
	f.BoolVar(&o.explain, "explain", false, "print the fingerprint manifest (what was hashed)")
	f.BoolVar(&o.record, "record", false, "publish a run record for statistics after the run (needs --registry; best-effort)")
	f.StringVar(&o.recordFile, "record-file", "", "also write the run record to this file, for 'aval record'")
	f.StringVar(&o.ciRunURL, "ci-run-url", "", "link to the CI run, stored in the run record")
	f.StringVar(&o.repository, "repository", "", "source repository (e.g. acme/orders), stored in the run record for 'aval stats --by repository'")
	registryTimeoutFlag(cmd, &o.registryTimeout, "(lookup and fetch share one; push has its own)")
	return cmd
}

// validate rejects bad flags before anything runs. token is the keyless
// signing token, checked only with --push.
func (o runFlags) validate(token string) error {
	switch {
	case o.push && o.repo == "":
		return usageError{errors.New("--push requires --registry")}
	case o.record && o.repo == "":
		return usageError{errors.New("--record requires --registry")}
	case o.shadow && o.noReuse:
		return usageError{errors.New("--shadow and --no-reuse exclude each other")}
	case o.login && !o.push:
		return usageError{errors.New("--login needs --push")}
	case o.repo != "":
		if err := checkRegistry(o.repo); err != nil {
			return err
		}
	}
	if err := checkTimeout(o.registryTimeout); err != nil || !o.push {
		return err
	}
	return checkSigner(o.key, token, o.login)
}

// keyHint is how to create a key aval can sign with.
const keyHint = "aval signs with an unencrypted ECDSA P-256 private key in PEM (PKCS#8 or SEC1); cosign keys are encrypted and not supported; create one with: openssl ecparam -name prime256v1 -genkey -noout -out aval.key"

// loginHelp and loginFlagHelp describe the interactive keyless login.
const (
	loginHelp     = "Without --key or $" + idTokenEnv + ", signing on a terminal (or with\n--login) opens the interactive Sigstore login in the browser; no token is\nstored"
	loginFlagHelp = "sign keyless after the interactive Sigstore login, even without a terminal (overrides: $" + oidcIssuerEnv + ", $" + oidcClientIDEnv + ")"
)

// checkSigner fails fast, before the test runs, when signing could not
// work: no key, no OIDC token and no interactive login (--login or a
// terminal), or a key that does not load.
func checkSigner(key, token string, login bool) error {
	if key == "" {
		if login && token != "" {
			return usageError{fmt.Errorf("--login and $%s exclude each other", idTokenEnv)}
		}
		if token == "" && !login && !isTerminal() {
			return usageError{fmt.Errorf("signing needs --key or, for keyless signing, an OIDC token in $%s or a terminal for the interactive Sigstore login (--login)", idTokenEnv)}
		}
		return nil
	}
	if login {
		return usageError{errors.New("--key and --login exclude each other")}
	}
	if err := loadsAsP256(key); err != nil {
		return usageError{fmt.Errorf("--key %s: %w; %s", key, err, keyHint)}
	}
	return nil
}

// loadsAsP256 checks what attest.Sign will require of the key file.
// ponytail: mirrors attest's key loading so a bad key fails before the test
// runs; signing loads it again and stays the authority.
func loadsAsP256(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	b, _ := pem.Decode(data)
	switch {
	case b == nil:
		return errors.New("not a PEM file")
	case strings.Contains(b.Type, "ENCRYPTED"):
		return fmt.Errorf("PEM type %s is encrypted", b.Type)
	}
	k, err := cryptoutils.UnmarshalPEMToPrivateKey(data, cryptoutils.SkipPassword)
	if err != nil {
		return err
	}
	if ec, ok := k.(*ecdsa.PrivateKey); !ok || ec.Curve != elliptic.P256() {
		return errors.New("not an ECDSA P-256 key")
	}
	return nil
}

// checkOutputs refuses to overwrite an existing file at --result or
// --report that is not an Aval output, wherever it lies: the scope check
// cannot see through aliases (a symlinked root, a case-insensitive file
// system). --fingerprint-only writes nothing.
func (o runFlags) checkOutputs() error {
	for _, p := range []string{o.resultFile, o.reportFile} {
		if !o.fingerprintOnly && existsNonAvalOutput(p) {
			return usageError{fmt.Errorf("output %s (--result or --report) exists and holds neither an Aval result nor a report; Aval does not overwrite it: choose another path or remove the file", p)}
		}
	}
	return nil
}

// outputs are the files run writes as Aval output. A file at --record-file
// that Aval did not write is left out, so it stays what it is (an input, if
// in scope): recordRun does not overwrite it, and a statistics flag never
// stops the test from running (#86).
func (o runFlags) outputs() []string {
	if existsNonAvalOutput(o.recordFile) {
		return []string{o.resultFile, o.reportFile}
	}
	return []string{o.resultFile, o.reportFile, o.recordFile}
}

func runTest(cmd *cobra.Command, o runFlags) error {
	// Read the signing token once, before validation needs it, and keep it
	// out of the environment of anything that runs later.
	token := os.Getenv(idTokenEnv)
	_ = os.Unsetenv(idTokenEnv) // ponytail: only fails on an invalid name
	if err := o.validate(token); err != nil {
		return err
	}
	c, fp, fpErr := loadAndFingerprint(cmd.Context(), o.file, o.outputs()...)
	if c == nil {
		return fpErr
	}
	if err := o.checkOutputs(); err != nil {
		return err
	}
	ctx, out, warn := cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	if o.explain || o.fingerprintOnly {
		if err := printFingerprint(out, warn, fp, fpErr, o); err != nil || o.fingerprintOnly {
			return err
		}
	}
	r, lookupErr := decide(ctx, out, warn, c, fp, fpErr, o)
	_ = cliout.WriteReport(out, "text", r) // text never fails, like the other lines printed
	// No I/O failure may skip the test or hide a verification failure
	// (invariants 3, 4): errors are collected, never returned early.
	var errs []error
	if err := writeReportFile(o.reportFile, r); err != nil {
		errs = append(errs, err)
	}
	if r.Outcome == evidence.OutcomeVerified && !o.shadow && len(errs) == 0 {
		fmt.Fprintf(out, "reused %s result\n", r.Result) // WriteReport printed the reference
		recordRun(ctx, out, warn, o, r, true, nil)
		if r.Result != evidence.ResultPassed {
			return fmt.Errorf("reused result %s: %w", r.Result, errTestFailed)
		}
		return nil
	}
	// A security event: run the test, then exit 5 (invariant 4). verify
	// printed the SECURITY line and the report the reason.
	if errors.Is(lookupErr, evidence.ErrVerification) {
		errs = append(errs, shown{lookupErr})
	}
	p, err := execute(ctx, out, warn, c, filepath.Dir(o.file), fp)
	if err != nil {
		recordRun(ctx, out, warn, o, r, false, nil)
		return errors.Join(append(errs, err)...)
	}
	fmt.Fprintf(out, "ran %s: %s (exit %d)\n", c.Metadata.Name, p.Result, p.Execution.ExitCode)
	errs = append(errs, keep(ctx, out, p, fpErr, mutable(c, fp), token, o)...)
	recordRun(ctx, out, warn, o, r, false, p)
	return errors.Join(append(errs, judge(out, warn, r, p))...)
}

// printFingerprint handles --explain and --fingerprint-only. Both need the
// fingerprint, so its error is final here. --explain alone goes on to run
// when the fingerprint is known, and decide lists the mutable references;
// --fingerprint-only lists them on warn, so stdout stays the digest.
func printFingerprint(out, warn io.Writer, fp fingerprint.Fingerprint, fpErr error, o runFlags) error {
	if fpErr != nil {
		return fpErr
	}
	if o.explain {
		fmt.Fprint(out, fp.Manifest)
		printContracts(out, fp.Contracts, "")
		printResolveCost(warn, fp)
	}
	if o.fingerprintOnly {
		printFindings(warn, fp)
		fmt.Fprintln(out, fp.Digest)
	}
	return nil
}

// printContracts prints the scope.contracts tree, one indented line per
// reference: path (relative to the contract that names it), name and
// fingerprint.
func printContracts(w io.Writer, deps []fingerprint.Dependency, indent string) {
	for _, d := range deps {
		fmt.Fprintf(w, "%sdepends on %s (%s) %s\n", indent, d.Path, d.Name, d.Digest)
		printContracts(w, d.Contracts, indent+"  ")
	}
}

// printResolveCost reports what scope.resolve cost: the network time of
// each reference and the total, across the contract and the contracts it
// references. Not part of the manifest.
func printResolveCost(w io.Writer, fp fingerprint.Fingerprint) {
	var all []resolve.Result
	seen := map[string]bool{}
	var walk func(fingerprint.Fingerprint)
	walk = func(f fingerprint.Fingerprint) {
		all = append(all, f.Resolved...)
		for _, d := range f.Contracts {
			if !seen[d.Digest] { // a shared contract was resolved once
				seen[d.Digest] = true
				walk(d.Fingerprint)
			}
		}
	}
	walk(fp)
	var total time.Duration
	for _, r := range all {
		total += r.Took
		fmt.Fprintf(w, "resolved %s %s to %s in %s\n", r.Kind, r.Ref, r.ID, r.Took.Round(time.Millisecond))
	}
	if len(all) > 0 {
		fmt.Fprintf(w, "resolved %d reference(s) in %s\n", len(all), total.Round(time.Millisecond))
	}
}

// printFindings lists every mutable reference, acknowledged or not.
func printFindings(w io.Writer, fp fingerprint.Fingerprint) {
	for _, f := range fp.Findings {
		fmt.Fprintf(w, "mutable: %s\n", cliout.OneLine(f.String()))
	}
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
		printFindings(out, fp)
	}
	switch c.Cacheability {
	case contract.NonDeterministic:
		fmt.Fprintf(out, "cacheability %s: never reusable\n", c.Cacheability)
	case contract.ExternalDependencies:
		fmt.Fprintf(out, "cacheability %s: not reusable by default (the policy may allow it)\n", c.Cacheability)
	}
	r := newReport(c, fp)
	switch {
	case fpErr != nil:
		return failReport(r, fpErr)
	case mutable(c, fp) != nil:
		return failReport(r, mutable(c, fp))
	case o.noReuse:
		return failReport(r, skipped("reuse disabled (--no-reuse)"))
	case o.repo == "":
		return failReport(r, skipped("no registry configured: pass --registry to reuse verified results"))
	}
	// The deadline bounds the registry, never runner.command.
	ctx, cancel := context.WithTimeout(ctx, o.registryTimeout)
	defer cancel()
	return verify(ctx, warn, c, fp, o.repo, o.policyFile)
}

// execute runs runner.command in the contract's directory and returns the
// unsigned predicate for the run. A command that cannot start is an error,
// not a failed test. The signing token is withheld from the command: the code
// under test must not be able to sign results as this workload.
func execute(ctx context.Context, stdout, stderr io.Writer, c *contract.Contract, dir string, fp fingerprint.Fingerprint) (*evidence.Predicate, error) {
	argv := c.Runner.Command
	x := exec.CommandContext(ctx, argv[0], argv[1:]...)
	x.Dir, x.Stdout, x.Stderr = dir, stdout, stderr
	x.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, idTokenEnv+"=") })
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
// fingerprint there is nothing a result could be reused for; with
// unacknowledged mutable references (pinErr) nothing may reuse it, so it is
// recorded but not published.
func keep(ctx context.Context, out io.Writer, p *evidence.Predicate, fpErr, pinErr error, token string, o runFlags) []error {
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
	if pinErr != nil {
		return []error{fmt.Errorf("result not published: %s", pinErr)}
	}
	ref, err := signAndPush(ctx, p, o.repo, o.key, token, o.registryTimeout)
	if err != nil {
		return []error{err}
	}
	fmt.Fprintf(out, "pushed %s\n", ref)
	return nil
}

// recordRun writes the run record (--record-file) and publishes it
// (--record). reused is true only when the verified result was reused
// (not in --shadow, not after a failed --report write): a verified lookup
// alone is no reuse. ran is the real run, nil when the test did not run:
// reused, or runner.command could not start. Best effort: a failure is a warning and
// never changes the exit code, and a record is statistics, never evidence.
func recordRun(ctx context.Context, out, warn io.Writer, o runFlags, r evidence.Report, reused bool, ran *evidence.Predicate) {
	if !o.record && o.recordFile == "" {
		return
	}
	rec := evidence.RunRecord{
		Version: evidence.RunRecordVersion, TestID: r.TestID, Fingerprint: r.Fingerprint, Outcome: r.Outcome,
		AvalVersion: version, RecordedAt: now().UTC(), CIRunURL: o.ciRunURL,
		Repository: o.repository,
	}
	if r.Outcome == evidence.OutcomeVerified {
		rec.VerifiedResult = r.Result
	}
	switch {
	case ran != nil:
		rec.Result, rec.TestDurationMs = ran.Result, ran.FinishedAt.Sub(ran.StartedAt).Milliseconds()
	case reused:
		rec.Reused, rec.Result = true, r.Result
		rec.SavedDurationMs = r.Predicate.FinishedAt.Sub(r.Predicate.StartedAt).Milliseconds()
	}
	rf := filepath.Clean(o.recordFile)
	switch {
	case o.recordFile != "" && (rf == filepath.Clean(o.resultFile) || o.reportFile != "" && rf == filepath.Clean(o.reportFile)):
		fmt.Fprintln(warn, "aval: warning: run record not written: --record-file is also --result/--report")
	case existsNonAvalOutput(o.recordFile):
		fmt.Fprintf(warn, "aval: warning: run record not written: %s (--record-file) exists and holds no Aval output; Aval does not overwrite it\n", o.recordFile)
	case o.recordFile != "":
		if err := writeJSON(o.recordFile, rec); err != nil {
			fmt.Fprintf(warn, "aval: warning: run record not written: %s\n", cliout.OneLine(err.Error()))
		}
	}
	if !o.record {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, o.registryTimeout)
	defer cancel()
	d, err := pushRecord(ctx, o.repo, rec)
	if err != nil {
		fmt.Fprintf(warn, "aval: warning: run record not published: %s\n", cliout.OneLine(err.Error()))
		return
	}
	fmt.Fprintf(out, "recorded %s\n", d.Reference())
}

// writeReportFile writes r to path, unless no --report was asked for.
func writeReportFile(path string, r evidence.Report) error {
	if path == "" {
		return nil
	}
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
// OIDC token, from the interactive login when there is none) and publishes
// it under its fingerprint, within timeout. The login runs only now, after
// the test, so the short-lived token is fresh and runner.command never had
// it.
func signAndPush(ctx context.Context, p *evidence.Predicate, repo, key, token string, timeout time.Duration) (string, error) {
	st, err := attest.NewStatement(p)
	if err == nil && key == "" && token == "" {
		if token, err = login(); err != nil {
			err = fmt.Errorf("interactive Sigstore login: %w", err)
		}
	}
	var s *attest.Signed
	if err == nil {
		s, err = sign(ctx, st, attest.SignOptions{Key: key, IDToken: token})
	}
	if err != nil {
		return "", fmt.Errorf("signing: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	d, err := publish(ctx, repo, p.Execution.Fingerprint, registry.Attestation{Envelope: s.Envelope, Bundle: s.Bundle})
	if err != nil {
		return "", fmt.Errorf("publishing: %w", err)
	}
	return d.Reference(), nil
}

func newPushCmd() *cobra.Command {
	var file, repo, key, resultFile string
	var timeout time.Duration
	var interactive bool
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Sign and publish a recorded execution result",
		Long: `push signs the result recorded by 'aval run' and publishes it. The result
must be for the current inputs: same test id, cacheability and fingerprint.
Keyless signing reads the OIDC token from $` + idTokenEnv + `.
` + loginHelp + `.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if repo == "" {
				return usageError{errors.New("--registry is required")}
			}
			token := os.Getenv(idTokenEnv)
			if err := cmp.Or(checkRegistry(repo), checkTimeout(timeout), checkSigner(key, token, interactive)); err != nil {
				return err
			}
			c, fp, err := loadAndFingerprint(cmd.Context(), file, resultFile)
			if err != nil {
				return err
			}
			if err := mutable(c, fp); err != nil {
				return fmt.Errorf("result not published: %s", err)
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
			ref, err := signAndPush(cmd.Context(), p, repo, key, token, timeout)
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
	f.BoolVar(&interactive, "login", false, loginFlagHelp)
	f.StringVar(&resultFile, "result", defaultResult, "result recorded by 'aval run'")
	registryTimeoutFlag(cmd, &timeout)
	return cmd
}

func newRecordCmd() *cobra.Command {
	var repo, file, repository string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Publish a run record written by 'aval run --record-file'",
		Long: `record publishes a run record that 'aval run --record-file' wrote, as 'aval
run --record' would have. Use it to publish, from a job with write credentials
that runs no tests, the record of a run made without them. Run records are
unsigned statistics: run and verify never read them. --repository sets the
record's source repository.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if repo == "" || file == "" {
				return usageError{errors.New("--registry and --record-file are required")}
			}
			if err := checkRegistry(repo); err != nil {
				return err
			}
			if err := checkTimeout(timeout); err != nil {
				return err
			}
			raw, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("reading run record: %w", err)
			}
			rec, err := evidence.ParseRunRecord(raw)
			if err != nil {
				return fmt.Errorf("run record %s: %w", file, err)
			}
			rec.Repository = cmp.Or(repository, rec.Repository)
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			d, err := pushRecord(ctx, repo, *rec)
			if err != nil {
				return fmt.Errorf("publishing run record: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "recorded %s\n", d.Reference())
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&repo, "registry", "", "OCI repository for run records (required)")
	f.StringVar(&file, "record-file", "", "run record written by 'aval run --record-file' (required)")
	f.StringVar(&repository, "repository", "", "source repository (e.g. acme/orders); default: the one in the record")
	registryTimeoutFlag(cmd, &timeout)
	return cmd
}
