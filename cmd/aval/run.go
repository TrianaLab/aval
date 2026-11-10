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
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

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
	config                                              *globalFlags // prints the config: line; nil prints none
}

func newRunCmd() *cobra.Command {
	var o runFlags
	g := &globalFlags{}
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Reuse a verified result for the current inputs, or run the test",
		Long: `run reuses a verified result for the current inputs when the trust policy
accepts it, restoring the contract's outputs; otherwise it runs runner.command
and records the result. Anything Aval cannot prove runs the test.

Without a registry and a trust policy the test always runs. --registry-timeout
covers lookup and fetch together; --push has its own. runner.command never
sees $` + idTokenEnv + `, $` + registryEnv + ` or $` + policyEnv + `.

Exit codes: 0 pass, 3 fail, 5 verification failed (a security event, after
running the test: also a verified result beside a rejected verification-failed
attestation, which is never reused, or more attestations than the lookup cap),
1 error or false hit (--shadow), 2 usage. 5 wins over 3 and 1.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.file, o.repo, o.policyFile, o.registryTimeout, o.config = g.file, g.registry, g.policy, g.timeout, g
			return runTest(cmd, o)
		},
	}
	g.bind(cmd, true, true)
	f := cmd.Flags()
	f.BoolVar(&o.push, "push", false, "sign and publish the result after running (needs --registry)")
	f.StringVar(&o.key, "key", "", "signing key path for --push (default: keyless, from $"+idTokenEnv+" or the interactive login; no token is stored and runner.command never sees it)")
	f.BoolVar(&o.login, "login", false, loginFlagHelp)
	f.StringVar(&o.resultFile, "result-file", defaultResult, "where to record the result for 'aval push'")
	f.StringVar(&o.reportFile, "report-file", "", "also write the lookup decision as a verify report (JSON) to this file")
	f.BoolVar(&o.noReuse, "no-reuse", false, "always run, never reuse (excludes --shadow)")
	f.BoolVar(&o.shadow, "shadow", false, "verify, but run the test anyway and exit 1 on a false hit (excludes --no-reuse)")
	f.BoolVar(&o.fingerprintOnly, "fingerprint-only", false, "print the execution fingerprint and exit (same as 'aval explain --digest')")
	f.BoolVar(&o.explain, "explain", false, "print the fingerprint manifest (what was hashed) before the run; 'aval explain' prints it alone")
	f.BoolVar(&o.record, "stats", false, "publish a run record for statistics after the run (needs --registry; best-effort)")
	f.StringVar(&o.recordFile, "stats-file", "", "also write the run record to this file, for 'aval stats publish'")
	f.StringVar(&o.ciRunURL, "ci-run-url", "", "link to the CI run, stored in the run record")
	f.StringVar(&o.repository, "source-repo", "", "source repository (e.g. acme/orders), stored in the run record for 'aval stats --by repository'")
	// TODO(milestone-13): remove these aliases (deprecated in 0.7.0, #165), not before 0.8.0.
	alias(cmd, "result-file", "result")
	alias(cmd, "report-file", "report")
	alias(cmd, "stats", "record")
	alias(cmd, "stats-file", "record-file")
	alias(cmd, "source-repo", "repository")
	return cmd
}

// alias adds old to cmd as a hidden, deprecated name of the flag name, bound
// to the same value. Using old prints pflag's deprecation notice on stderr.
func alias(cmd *cobra.Command, name, old string) {
	f := cmd.Flags()
	n := f.Lookup(name)
	f.AddFlag(&pflag.Flag{Name: old, Usage: n.Usage, Value: n.Value, DefValue: n.DefValue, NoOptDefVal: n.NoOptDefVal})
	_ = f.MarkDeprecated(old, "use --"+name) // ponytail: old was just added, cannot fail
	// cobra prints pflag's notices with Print, on stdout once SetOut is used.
	f.SetOutput(stderrOf{cmd})
}

// stderrOf writes to the command's stderr, resolved at write time.
type stderrOf struct{ c *cobra.Command }

func (w stderrOf) Write(p []byte) (int, error) { return w.c.ErrOrStderr().Write(p) }

// validate rejects bad flags before anything runs. token is the keyless
// signing token, checked only with --push.
func (o runFlags) validate(token string) error {
	switch {
	case o.push && o.repo == "":
		return usageError{errors.New("--push requires --registry")}
	case o.record && o.repo == "":
		return usageError{errors.New("--stats requires --registry")}
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
const keyHint = "aval signs with an unencrypted ECDSA P-256 private key in PEM (PKCS#8 or SEC1); cosign keys are encrypted and not supported; create one with: aval keygen"

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
			return signingNeeds()
		}
		return nil
	}
	if login {
		return usageError{errors.New("--key and --login exclude each other")}
	}
	_, err := loadP256(key)
	if errors.Is(err, errPublicKey) {
		fix := "pass the private key"
		if base, ok := strings.CutSuffix(key, ".pub"); ok {
			fix += " (" + base + ".key)"
		}
		return usageError{fmt.Errorf("--key %s is a public key; %s", key, fix)}
	}
	if err != nil {
		return usageError{fmt.Errorf("--key %s: %w; %s", key, err, keyHint)}
	}
	return nil
}

// errPublicKey: the --key file holds a public key, such as name.pub from
// 'aval keygen'.
var errPublicKey = errors.New("a public key")

// loadP256 loads the key file as attest.Sign will require it.
// ponytail: mirrors attest's key loading so a bad key fails before the test
// runs; signing loads it again and stays the authority.
func loadP256(path string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode(data)
	switch {
	case b == nil:
		return nil, errors.New("not a PEM file")
	case b.Type == "PUBLIC KEY":
		return nil, errPublicKey
	case strings.Contains(b.Type, "ENCRYPTED"):
		return nil, fmt.Errorf("PEM type %s is encrypted", b.Type)
	}
	k, err := cryptoutils.UnmarshalPEMToPrivateKey(data, cryptoutils.SkipPassword)
	if err != nil {
		return nil, err
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("not an ECDSA P-256 key")
	}
	return ec, nil
}

// checkOutputs refuses to overwrite an existing file at --result-file or
// --report-file that is not an Aval output, wherever it lies: the scope check
// cannot see through aliases (a symlinked root, a case-insensitive file
// system). --fingerprint-only writes nothing.
func (o runFlags) checkOutputs() error {
	for _, p := range []string{o.resultFile, o.reportFile} {
		if !o.fingerprintOnly && existsNonAvalOutput(p) {
			return usageError{fmt.Errorf("output %s (--result-file or --report-file) exists and holds neither an Aval result nor a report; Aval does not overwrite it: choose another path or remove the file", p)}
		}
	}
	return nil
}

// outputs are the files run writes as Aval output. A file at --stats-file
// that Aval did not write is left out, so it stays what it is (an input, if
// in scope): recordRun does not overwrite it, and a statistics flag never
// stops the test from running (#86).
func (o runFlags) outputs() []string {
	if existsNonAvalOutput(o.recordFile) {
		return []string{o.resultFile, o.reportFile}
	}
	return []string{o.resultFile, o.reportFile, o.recordFile}
}

func runTest(cmd *cobra.Command, o runFlags) (err error) {
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
	w := runWriter(out, warn, plainFlag(cmd))
	if o.explain || o.fingerprintOnly {
		if err := printFingerprint(out, warn, fp, fpErr, o); err != nil || o.fingerprintOnly {
			return err
		}
	}
	w.Note(o.config.configLine())
	observed, probeErr := probe(ctx, c, filepath.Dir(o.file))
	r, files, lookupErr := decide(ctx, w, c, fp, cmp.Or(fpErr, probeErr), o)
	// A rejected verification-failed candidate blocks reuse even beside a
	// verified one: run the test, then exit 5 (invariants 3, 4, #195).
	lookupErr = securityErr(r, lookupErr)
	sum, security := announce(w, c, r, lookupErr, o.repo)
	w.Security = sum.Security
	// Whatever fails below, the SECURITY line announce printed is repeated as
	// the last stderr line.
	defer func() { err = securityLast(warn, err, security) }()
	// No I/O failure may skip the test or hide a verification failure
	// (invariants 3, 4): errors are collected, never returned early.
	var errs []error
	if err := writeReportFile(o.reportFile, r); err != nil {
		errs = append(errs, err)
	}
	if reusable(r, lookupErr) && !o.shadow && len(errs) == 0 {
		// Outputs that cannot be written safely run the test instead.
		if err := restore(out, filepath.Dir(o.file), files); err != nil {
			errs = append(errs, fmt.Errorf("restoring outputs: %w", err))
		} else {
			return reuse(ctx, w, o, r, sum)
		}
	}
	// A security event: run the test, then exit 5 (invariant 4). announce
	// printed the SECURITY line and the report the reason.
	if errors.Is(lookupErr, evidence.ErrVerification) {
		errs = append(errs, shown{lookupErr})
	}
	w.Start(c.Metadata.Name, c.Runner.Command)
	p, err := execute(ctx, out, warn, c, filepath.Dir(o.file), fp, observed)
	if err != nil {
		recordRun(ctx, out, warn, o, r, false, nil)
		return errors.Join(append(errs, err)...)
	}
	sum.Result, sum.Took = p.Result, p.FinishedAt.Sub(p.StartedAt)
	w.End(c.Metadata.Name, p.Result, p.Execution.ExitCode, sum.Took)
	kept := keep(ctx, w, &sum, c, p, fpErr, probeErr, mutable(c, fp), token, o)
	recordRun(ctx, out, warn, o, r, false, p)
	verdict := judge(w, r, p, o.shadow)
	w.Summary(sum)
	return errors.Join(append(append(errs, kept...), verdict)...)
}

// announce prints the lookup decision r and starts the summary. security
// is the SECURITY line it printed, to repeat as the last stderr line;
// empty when no attestation failed verification.
func announce(w cliout.Run, c *contract.Contract, r evidence.Report, lookupErr error, repo string) (sum cliout.Summary, security string) {
	// Flooding is a security event with its own wording (#135).
	flood := errors.Is(lookupErr, registry.ErrTooManyCandidates)
	security = securityLineFor(r, lookupErr)
	sum = cliout.Summary{
		Test: c.Metadata.Name, ReuseOff: r.Outcome == evidence.OutcomeError || errors.As(lookupErr, new(reuseOff)),
		Security: security != "", Flood: flood,
		NeverReusable: c.Cacheability == contract.NonDeterministic, Verified: r.Outcome == evidence.OutcomeVerified,
	}
	if security != "" {
		w.SecurityBlock(security) // when found, before the report; securityLast repeats it last
	}
	w.Decision(r, c.Cacheability)
	if r.Outcome == evidence.OutcomeError {
		// The test runs: fail closed (invariant 3). The report keeps the raw error.
		w.ReuseOff(cmp.Or(unreachable(repo, lookupErr), r.Reason))
	}
	return sum, security
}

// securityLast prints what err says, then line, so the SECURITY line is the
// last on stderr whatever else failed. err comes back marked shown. Without
// a line it returns err as it is.
func securityLast(warn io.Writer, err error, line string) error {
	if line == "" {
		return err
	}
	printErrors(warn, err)
	if err != nil {
		err = shown{err}
	}
	fmt.Fprintln(warn, line)
	return err
}

// reuse reports the reused verified result r; a failing one exits 3, which
// the summary shows.
func reuse(ctx context.Context, w cliout.Run, o runFlags, r evidence.Report, sum cliout.Summary) error {
	w.Reused(r.Result) // the report printed the reference
	recordRun(ctx, w.Out, w.Err, o, r, true, nil)
	sum.Reused, sum.Result, sum.Saved = true, r.Result, r.Predicate.FinishedAt.Sub(r.Predicate.StartedAt)
	w.Summary(sum)
	if r.Result != evidence.ResultPassed {
		return shown{errTestFailed}
	}
	return nil
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

// printFindings lists every mutable reference, acknowledged or not, on w.
func printFindings(w io.Writer, fp fingerprint.Fingerprint) {
	noteFindings(func(s string) { fmt.Fprintln(w, s) }, fp)
}

// noteFindings writes each mutable reference through note.
func noteFindings(note func(string), fp fingerprint.Fingerprint) {
	for _, f := range fp.Findings {
		note("mutable: " + cliout.OneLine(f.String()))
	}
}

// judge turns the real run into the command's verdict. In --shadow, with a
// verified result, a disagreement is a false hit.
func judge(w cliout.Run, r evidence.Report, p *evidence.Predicate, shadow bool) error {
	if shadow && r.Outcome == evidence.OutcomeVerified {
		if r.Result != p.Result {
			fmt.Fprintf(w.Err, "aval: FALSE HIT: %s reused %s from %s, but the test %s\n", r.TestID, r.Result, r.Reference, p.Result)
			return errFalseHit
		}
		w.Note(fmt.Sprintf("shadow: reuse of %s would have been correct (%s)", r.Reference, p.Result))
	}
	if p.Result != evidence.ResultPassed {
		return shown{errTestFailed} // the summary says so
	}
	return nil
}

// decide looks up and verifies a result unless reuse is impossible or
// disabled. Every way of not reusing comes back as a report, never silently.
// A verified result comes with its outputs, checked but not yet written.
func decide(ctx context.Context, w cliout.Run, c *contract.Contract, fp fingerprint.Fingerprint, fpErr error, o runFlags) (evidence.Report, []registry.Output, error) {
	noteFindings(w.Note, fp) // the outcome line carries the digest
	var err error
	switch {
	case fpErr != nil:
		err = fpErr
	case mutable(c, fp) != nil:
		err = mutable(c, fp)
	case o.noReuse:
		err = reuseOff{"reuse off: --no-reuse"}
	case o.repo == "":
		err = reuseOff{"reuse off: no registry (set " + registryEnv + " or --registry)"}
	case o.policyFile == "" && c.Cacheability != contract.NonDeterministic: // verify names the class first
		err = reuseOff{"reuse off: no trust policy (set " + policyEnv + " or --policy)"}
	}
	if err != nil {
		r, err := failReport(newReport(c, fp), err)
		return r, nil, err
	}
	// The deadline bounds the registry, never runner.command.
	ctx, cancel := context.WithTimeout(ctx, o.registryTimeout)
	defer cancel()
	// The spinner is gone before announce prints and the runner starts.
	warn, stop := spin(w.Err, w.ErrStyled, "looking up "+o.repo)
	defer stop()
	return lookupAndVerify(ctx, warn, c, o.file, fp, o.repo, o.policyFile) // announce prints the SECURITY block
}

// probeTimeout bounds each environment.probe command.
var probeTimeout = 10 * time.Second

// maxProbeOutput caps a probe's output: it is signed into the predicate, and
// an envelope over the registry blob limit would read as a security event.
const maxProbeOutput = 4 << 10

// probe runs each environment.probe argv in dir, without a shell or the
// signing token, and checks that its output holds the declared toolchain
// value as a version token. It returns the outputs as probe.<key>. Only
// `aval run` calls it: a consumer never executes contract-supplied commands.
func probe(ctx context.Context, c *contract.Contract, dir string) (map[string]string, error) {
	seen := map[string]string{}
	for _, k := range slices.Sorted(maps.Keys(c.Environment.Probe)) {
		argv, want := c.Environment.Probe[k], c.Environment.Toolchain[k]
		pctx, cancel := context.WithTimeout(ctx, probeTimeout)
		x := exec.CommandContext(pctx, argv[0], argv[1:]...)
		x.Dir, x.Env, x.WaitDelay = dir, childEnv(), time.Second
		raw, err := x.CombinedOutput()
		cancel()
		if err == nil && len(raw) > maxProbeOutput {
			err, raw = fmt.Errorf("output over %d bytes", maxProbeOutput), raw[:maxProbeOutput]
		}
		got := strings.TrimSpace(string(raw))
		switch {
		case errors.Is(pctx.Err(), context.DeadlineExceeded):
			err = fmt.Errorf("timed out after %s", probeTimeout)
		case err == nil && !hasVersion(got, want):
			err = errors.New("the declared value is not in the output")
		}
		if err != nil {
			return nil, fmt.Errorf("environment.probe %s %q: declared %q, observed %q: %w", k, argv, want, cliout.OneLine(got), err)
		}
		seen["probe."+k] = got
	}
	return seen, nil
}

// hasVersion reports whether want appears in out as a version token: not
// preceded by a digit or dot, not followed by a digit (3.13 is in
// "Python 3.13.1", not in "Python 3.130"; 3.1 is not in "Python 3.13").
func hasVersion(out, want string) bool {
	return want != "" && regexp.MustCompile(`(^|[^0-9.])`+regexp.QuoteMeta(want)+`([^0-9]|$)`).MatchString(out)
}

// childEnv is the environment of commands Aval runs, without the signing
// token, the registry and the trust policy: the code under test must not be
// able to sign results as this workload, nor reuse or publish against the
// operator's trust settings.
func childEnv() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return k == idTokenEnv || k == registryEnv || k == policyEnv
	})
}

// execute runs runner.command in the contract's directory and returns the
// unsigned predicate for the run, with the probe outputs (observed) in its
// environment. A command that cannot start is an error, not a failed test.
func execute(ctx context.Context, stdout, stderr io.Writer, c *contract.Contract, dir string, fp fingerprint.Fingerprint, observed map[string]string) (*evidence.Predicate, error) {
	argv := c.Runner.Command
	x := exec.CommandContext(ctx, argv[0], argv[1:]...)
	x.Dir, x.Stdout, x.Stderr, x.Env = dir, stdout, stderr, childEnv()
	start := time.Now().UTC()
	err := x.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return nil, fmt.Errorf("running %q: %w", argv[0], err)
	}
	code := x.ProcessState.ExitCode()
	// The real os and arch win over toolchain keys of the same name.
	env := map[string]string{}
	maps.Copy(env, c.Environment.Toolchain)
	maps.Copy(env, observed)
	env["os"], env["arch"] = runtime.GOOS, runtime.GOARCH
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

// keep records the result and, with --push, publishes it and marks sum. Without a
// fingerprint there is nothing a result could be reused for; with
// unacknowledged mutable references (pinErr) nothing may reuse it, so it is
// recorded but not published. After a failed probe it is not even recorded,
// so 'aval push' cannot publish it.
func keep(ctx context.Context, w cliout.Run, sum *cliout.Summary, c *contract.Contract, p *evidence.Predicate, fpErr, probeErr, pinErr error, token string, o runFlags) []error {
	if probeErr != nil {
		return []error{fmt.Errorf("result not recorded: %w", probeErr)}
	}
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
	warnUntrustedKey(w.Err, o.key, o.policyFile)
	ref, err := signAndPush(ctx, w, p, c, o.file, o.repo, o.key, token, o.registryTimeout)
	if err != nil {
		return []error{err}
	}
	w.Pushed(ref)
	sum.Pushed = true
	return nil
}

// recordRun writes the run record (--stats-file) and publishes it
// (--stats). reused is true only when the verified result was reused
// (not in --shadow, not after a failed --report-file write): a verified lookup
// alone is no reuse. ran is the real run, nil when the test did not run:
// reused, or runner.command could not start. Best effort: a failure is a warning and
// never changes the exit code, and a record is statistics, never evidence.
func recordRun(ctx context.Context, out, warn io.Writer, o runFlags, r evidence.Report, reused bool, ran *evidence.Predicate) {
	if !o.record && o.recordFile == "" {
		return
	}
	rec := evidence.RunRecord{
		Version: evidence.RunRecordVersion, TestID: r.TestID, Fingerprint: r.Fingerprint, Outcome: recordOutcome(r),
		AvalVersion: version, RecordedAt: now().UTC(), CIRunURL: o.ciRunURL,
		Repository: o.repository,
	}
	if rec.Outcome == evidence.OutcomeVerified {
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
		fmt.Fprintln(warn, "aval: warning: run record not written: --stats-file is also --result-file/--report-file")
	case existsNonAvalOutput(o.recordFile):
		fmt.Fprintf(warn, "aval: warning: run record not written: %s (--stats-file) exists and holds no Aval output; Aval does not overwrite it\n", o.recordFile)
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

// recordOutcome is the run record outcome for r: its outcome, except that a
// verified lookup beside a rejected verification-failed candidate was not
// reused and counts as a verification failure (#195).
func recordOutcome(r evidence.Report) evidence.Outcome {
	if securityCount(r) > 0 {
		return evidence.OutcomeVerificationFailed
	}
	return r.Outcome
}

// writeReportFile writes r to path, unless no --report-file was asked for.
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

// signAndPush collects the outputs of c (loaded from file), takes the OIDC
// token from the interactive login when there is no key and no token,
// publishes the outputs, then signs the predicate with them (key-based with
// key, else keyless with the token) and publishes it under its fingerprint,
// each within timeout. Outputs that break a rule, or a failed login,
// publish nothing. The login runs only now, after the test, so the
// short-lived token is fresh and runner.command never had it. ui shows the
// registry wait spinner, never over the login prompt.
func signAndPush(ctx context.Context, ui cliout.Run, p *evidence.Predicate, c *contract.Contract, file, repo, key, token string, timeout time.Duration) (string, error) {
	var files []registry.Output
	var err error
	if len(c.Outputs) > 0 {
		if files, err = collectOutputs(file, c); err != nil {
			return "", fmt.Errorf("result not published: %w", err)
		}
	}
	if key == "" && token == "" {
		if token, err = login(); err != nil {
			return "", fmt.Errorf("signing: interactive Sigstore login: %w", err)
		}
	}
	if len(c.Outputs) > 0 {
		octx, cancel := context.WithTimeout(ctx, timeout)
		_, stop := spin(ui.Err, ui.ErrStyled, "pushing outputs to "+repo)
		o, err := pushOutputs(octx, repo, p.Execution.Fingerprint, files)
		stop()
		cancel()
		if err != nil {
			return "", fmt.Errorf("publishing outputs: %w", reachError(repo, err))
		}
		q := *p
		q.Execution.Outputs = o
		p = &q
	}
	st, err := attest.NewStatement(p)
	var s *attest.Signed
	if err == nil {
		s, err = sign(ctx, st, attest.SignOptions{Key: key, IDToken: token})
	}
	if err != nil {
		return "", fmt.Errorf("signing: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, stop := spin(ui.Err, ui.ErrStyled, "pushing to "+repo)
	d, err := publish(ctx, repo, p.Execution.Fingerprint, registry.Attestation{Envelope: s.Envelope, Bundle: s.Bundle})
	stop()
	if err != nil {
		return "", fmt.Errorf("publishing: %w", reachError(repo, err))
	}
	return d.Reference(), nil
}

func newPushCmd() *cobra.Command {
	var key, resultFile string
	g := &globalFlags{}
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
			file, repo, timeout := g.file, g.registry, g.timeout
			g.printConfig(cmd.ErrOrStderr())
			token := os.Getenv(idTokenEnv)
			if err := cmp.Or(requireRegistry(repo), checkTimeout(timeout), checkSigner(key, token, interactive)); err != nil {
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
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("reading the result recorded by 'aval run': %w; run aval run first, or use aval run --push", err)
			}
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
			ui := runWriter(cmd.OutOrStdout(), cmd.ErrOrStderr(), plainFlag(cmd))
			ref, err := signAndPush(cmd.Context(), ui, p, c, file, repo, key, token, timeout)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "pushed %s\n", ref)
			return nil
		},
	}
	g.bind(cmd, true, false)
	f := cmd.Flags()
	f.StringVar(&key, "key", "", "signing key path (default: keyless OIDC)")
	f.BoolVar(&interactive, "login", false, loginFlagHelp)
	f.StringVar(&resultFile, "result-file", defaultResult, "result recorded by 'aval run'")
	alias(cmd, "result-file", "result") // TODO(milestone-13): remove (deprecated in 0.7.0, #165), not before 0.8.0.
	return cmd
}

// recordDeprecated is the notice `aval record` prints on stderr, in cobra's
// words for a deprecated command (cobra's own notice would go to stdout).
// TODO(milestone-13): remove `aval record` (deprecated in 0.7.0, #165), not before 0.8.0.
const recordDeprecated = "use 'aval stats publish FILE' instead"

// newRecordCmd is the deprecated `aval record --record-file FILE`, hidden
// from help: `aval stats publish FILE`.
func newRecordCmd() *cobra.Command {
	var file, sourceRepo string
	g := &globalFlags{}
	cmd := &cobra.Command{
		Use:         "record",
		Short:       "Publish a run record written by 'aval run --stats-file'",
		Long:        "record is the deprecated form of 'aval stats publish FILE'.",
		Hidden:      true,
		Annotations: map[string]string{"deprecated": recordDeprecated},
		Args:        noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(cmd.ErrOrStderr(), "Command %q is deprecated, %s\n", cmd.Name(), recordDeprecated)
			return publishRecord(cmd, g, file, sourceRepo)
		},
	}
	g.bind(cmd, false, false)
	f := cmd.Flags()
	f.StringVar(&file, "record-file", "", "run record written by 'aval run --stats-file' (required)")
	f.StringVar(&sourceRepo, "source-repo", "", "source repository (e.g. acme/orders); default: the one in the record")
	alias(cmd, "source-repo", "repository")
	return cmd
}

func newStatsPublishCmd() *cobra.Command {
	var sourceRepo string
	g := &globalFlags{}
	cmd := &cobra.Command{
		Use:   "publish FILE",
		Short: "Publish a run record written by 'aval run --stats-file'",
		Long: `stats publish publishes the run record that 'aval run --stats-file' wrote to
FILE, as 'aval run --stats' would have. Use it to publish, from a job with
write credentials that runs no tests, the record of a run made without them.
Run records are unsigned statistics: run and verify never read them.
--source-repo sets the record's source repository.`,
		Example: `  aval stats publish --registry ghcr.io/acme/orders-aval record.json`,
		Args: func(c *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(c, args); err != nil {
				return usageError{err}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return publishRecord(cmd, g, args[0], sourceRepo)
		},
	}
	g.bind(cmd, false, false)
	cmd.Flags().StringVar(&sourceRepo, "source-repo", "", "source repository (e.g. acme/orders); default: the one in the record")
	return cmd
}

// publishRecord publishes the run record in file to the registry, with
// sourceRepo as its source repository when set.
func publishRecord(cmd *cobra.Command, g *globalFlags, file, sourceRepo string) error {
	repo, timeout := g.registry, g.timeout
	g.printConfig(cmd.ErrOrStderr())
	if err := cmp.Or(requireRegistry(repo), checkTimeout(timeout)); err != nil {
		return err
	}
	if file == "" {
		return usageError{errors.New("a run record file is required")}
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("reading run record: %w", err)
	}
	rec, err := evidence.ParseRunRecord(raw)
	if err != nil {
		return fmt.Errorf("run record %s: %w", file, err)
	}
	rec.Repository = cmp.Or(sourceRepo, rec.Repository)
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	d, err := pushRecord(ctx, repo, *rec)
	if err != nil {
		return fmt.Errorf("publishing run record: %w", reachError(repo, err))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "recorded %s\n", d.Reference())
	return nil
}
