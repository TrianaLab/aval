// Package demo is the Aval of the docs site's "Try it" terminal, compiled to
// WebAssembly by cmd/aval-wasm. It runs the real contract parsing
// (internal/contract), fingerprinting (internal/fingerprint, over an
// in-memory fs.FS) and verification (pkg/evidence.Verify) over an in-memory
// project and registry. The test is simulated, results are signed with a
// throwaway ECDSA P-256 key generated at reset, and the signature is checked
// with the standard library. Sigstore and ORAS stay out of the binary, so
// keyless signing is not offered.
//
// Its output uses the CLI's words: tests/acceptance runs the same story
// through the real binary against an in-process registry and compares.
package demo

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"runtime"
	"slices"
	"strings"
	"testing/fstest"
	"time"

	"github.com/TrianaLab/aval/internal/cliout"
	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/fingerprint"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// Exit codes of the CLI (cmd/aval/main.go) that the demo returns.
const (
	exitOK                 = 0
	exitError              = 1
	exitUsage              = 2
	exitMiss               = 4
	exitVerificationFailed = 5
	exitNotFound           = 127 // a shell's "command not found"
)

const (
	// DefaultRepo is the repository the demo's in-memory registry poses as.
	DefaultRepo = "registry.example/acme/greet-aval"
	policyFile  = "policy.yaml"
	pubFile     = "throwaway.pub"
	// testOutput is the simulated output of `go test ./...`.
	testOutput = "ok  \texample.com/greet\t0.004s\n"
)

// Help is what `help` prints.
const Help = `This terminal runs Aval compiled to WebAssembly, entirely in your browser.
  aval run              look up a trusted result; on a miss run the test, sign and push
  aval run --explain    the same, printing the fingerprint manifest first
  aval verify           look up and verify without running anything
  aval verify -o json   the same, as the JSON verify report
  ls, cat <file>        the demo project
  edit <file>           change a file (a .go file or go.mod: the next run is a miss)
  tamper                alter the signed result in the registry (verify: exit 5)
  reset                 original files, empty registry, new throwaway key
  clear                 clear the screen
Here aval run means: aval run --registry ` + DefaultRepo + ` --policy policy.yaml --push --key throwaway.key
and aval verify adds --registry and --policy. The test is simulated, the registry
is in memory and the key is generated in this tab. Keyless (Sigstore) signing is
not available in the browser.
`

// Demo is one visitor's session: the project files, the registry and the key.
type Demo struct {
	// Repo is the repository named in references.
	Repo  string
	files map[string][]byte
	key   *ecdsa.PrivateKey
	pub   string
	reg   map[string][]*stored // by fingerprint
}

// stored is one attestation in the in-memory registry.
type stored struct {
	ref string
	env evidence.Envelope
}

// New returns a session with the original project, an empty registry and a
// new key.
func New() *Demo {
	d := &Demo{Repo: DefaultRepo}
	d.reset()
	return d
}

func (d *Demo) reset() {
	// ponytail: crypto/rand and a P-256 key cannot fail.
	d.key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&d.key.PublicKey)
	d.pub = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	d.files = map[string][]byte{pubFile: []byte(d.pub)}
	for name, body := range project {
		d.files[name] = []byte(body)
	}
	d.reg = map[string][]*stored{}
}

// Files returns a copy of the project files, throwaway.pub included.
func (d *Demo) Files() map[string][]byte { return maps.Clone(d.files) }

// KeyPEM returns the throwaway private key in PKCS#8 PEM, for tests that
// sign the same results with the real CLI.
func (d *Demo) KeyPEM() []byte {
	der, _ := x509.MarshalPKCS8PrivateKey(d.key) // ponytail: a P-256 key cannot fail
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// Exec runs one command line and returns its exit code. Errors and the
// SECURITY line go to errw, as the CLI writes them to stderr.
func (d *Demo) Exec(line string, out, errw io.Writer) int {
	args := strings.Fields(line)
	if len(args) == 0 {
		return exitOK
	}
	arg := func() string { return strings.Join(args[1:], " ") }
	switch args[0] {
	case "help":
		fmt.Fprint(out, Help)
		return exitOK
	case "aval":
		return d.aval(args[1:], out, errw)
	case "ls":
		fmt.Fprintln(out, strings.Join(slices.Sorted(maps.Keys(d.files)), "  "))
		return exitOK
	case "cat":
		body, ok := d.files[arg()]
		if !ok {
			fmt.Fprintf(errw, "cat: %s: no such file\n", arg())
			return exitError
		}
		fmt.Fprint(out, string(body))
		return exitOK
	case "edit":
		return d.edit(arg(), out, errw)
	case "tamper":
		return d.tamper(out, errw)
	case "reset":
		d.reset()
		fmt.Fprintln(out, "reset: original files, empty registry, new throwaway key")
		return exitOK
	}
	fmt.Fprintf(errw, "%s: command not found (type help)\n", args[0])
	return exitNotFound
}

// edit appends a comment line to a project file.
func (d *Demo) edit(name string, out, errw io.Writer) int {
	body, ok := d.files[name]
	switch {
	case name == policyFile || name == pubFile:
		fmt.Fprintf(errw, "edit: %s is fixed in this demo\n", name)
		return exitError
	case !ok:
		fmt.Fprintf(errw, "edit: %s: no such file (type ls)\n", name)
		return exitError
	}
	comment := "# edited\n"
	if strings.HasSuffix(name, ".go") || name == "go.mod" {
		comment = "// edited\n"
	}
	d.files[name] = append(slices.Clone(body), comment...)
	fmt.Fprintf(out, "edited %s: appended %q\n", name, strings.TrimSuffix(comment, "\n"))
	return exitOK
}

// tamper rewrites the signed result of every attestation for the current
// inputs, as someone with write access to the registry could, and keeps
// the signature.
func (d *Demo) tamper(out, errw io.Writer) int {
	_, fp, code := d.fingerprint(errw)
	if code != exitOK {
		return code
	}
	atts := d.reg[fp.Digest]
	if len(atts) == 0 {
		fmt.Fprintln(errw, "tamper: nothing is published for the current inputs; run aval run first")
		return exitError
	}
	for _, a := range atts {
		a.env.Payload = []byte(strings.Replace(string(a.env.Payload), `"result":"passed"`, `"result":"failed"`, 1))
	}
	fmt.Fprintf(out, "tampered with %d attestation(s) for %s: the signed result now says failed, the signature is unchanged\n", len(atts), fp.Digest)
	return exitOK
}

// aval runs the aval subcommands the demo supports, with the CLI's usage
// errors.
func (d *Demo) aval(args []string, out, errw io.Writer) int {
	usage := func(format string, a ...any) int {
		fmt.Fprintf(errw, "aval: "+format+"\n", a...)
		return exitUsage
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(out, Help)
		return exitOK
	}
	cmd := args[0]
	explain, output, err := parseFlags(cmd, args[1:])
	if err != nil {
		return usage("%s", err)
	}
	switch cmd {
	case "run":
		return d.run(explain, out, errw)
	case "verify":
		if output != "text" && output != "json" {
			return usage("--output must be text or json, got %q", output)
		}
		return d.verifyCmd(output, out, errw)
	case "push", "record":
		return usage("%s is not in this demo: here aval run signs and pushes", cmd)
	}
	return usage("unknown command %q", cmd)
}

// parseFlags parses the flags of cmd the demo knows, with cobra's errors.
func parseFlags(cmd string, args []string) (explain bool, output string, err error) {
	output = "text"
	for i := 0; i < len(args); i++ {
		f := args[i]
		name, val, hasVal := strings.Cut(f, "=")
		switch {
		case cmd == "run" && f == "--explain":
			explain = true
		case cmd == "verify" && (name == "-o" || name == "--output"):
			if !hasVal && i+1 == len(args) {
				return false, "", fmt.Errorf("flag needs an argument: %s", map[string]string{"-o": "'o' in -o", "--output": "--output"}[name])
			}
			if !hasVal {
				i++
				val = args[i]
			}
			output = val
		case strings.HasPrefix(f, "--"):
			return false, "", fmt.Errorf("unknown flag: %s", name)
		case strings.HasPrefix(f, "-") && len(f) > 1:
			return false, "", fmt.Errorf("unknown shorthand flag: '%c' in %s", f[1], f)
		default:
			return false, "", fmt.Errorf("unknown command %q for \"aval %s\"", f, cmd)
		}
	}
	return explain, output, nil
}

// fingerprint parses aval.yaml and fingerprints the project.
func (d *Demo) fingerprint(errw io.Writer) (*contract.Contract, fingerprint.Fingerprint, int) {
	c, err := contract.Parse(d.files[contract.DefaultFile])
	if err != nil {
		fmt.Fprintf(errw, "aval: %s: %s\n", contract.DefaultFile, cliout.OneLine(err.Error()))
		return nil, fingerprint.Fingerprint{}, exitUsage
	}
	// ponytail: fstest.MapFS is the stdlib's in-memory fs.FS.
	fsys := fstest.MapFS{}
	for name, body := range d.files {
		fsys[name] = &fstest.MapFile{Data: body, Mode: 0o644}
	}
	fp, err := fingerprint.ComputeFS(fsys, c, func(string) (string, bool) { return "", false })
	if err != nil {
		fmt.Fprintf(errw, "aval: fingerprint: %s\n", cliout.OneLine(err.Error()))
		return nil, fingerprint.Fingerprint{}, exitError
	}
	return c, fp, exitOK
}

func (d *Demo) verifyCmd(output string, out, errw io.Writer) int {
	c, fp, code := d.fingerprint(errw)
	if code != exitOK {
		return code
	}
	r := d.verify(c, fp, errw)
	_ = cliout.WriteReport(out, output, r) // ponytail: in-memory writers do not fail
	switch r.Outcome {
	case evidence.OutcomeVerified:
		return exitOK
	case evidence.OutcomeVerificationFailed:
		return exitVerificationFailed
	}
	return exitMiss
}

// run is `aval run --push`: reuse a verified result, or run the (simulated)
// test, sign the result and push it. A verification failure runs the test
// and then exits 5.
func (d *Demo) run(explain bool, out, errw io.Writer) int {
	c, fp, code := d.fingerprint(errw)
	if code != exitOK {
		return code
	}
	if explain {
		fmt.Fprint(out, fp.Manifest)
	}
	fmt.Fprintf(out, "test %s fingerprint %s\n", c.Metadata.Name, fp.Digest)
	r := d.verify(c, fp, errw)
	_ = cliout.WriteReport(out, "text", r)
	if r.Outcome == evidence.OutcomeVerified {
		// ponytail: the demo only signs passes and tamper breaks the
		// signature, so a verified result here is always passed.
		fmt.Fprintf(out, "reused %s result\n", r.Result)
		return exitOK
	}
	start := time.Now().UTC()
	fmt.Fprint(out, testOutput)
	fmt.Fprintf(out, "ran %s: %s (exit 0)\n", c.Metadata.Name, evidence.ResultPassed)
	fmt.Fprintf(out, "pushed %s\n", d.push(c, fp, start))
	if r.Outcome == evidence.OutcomeVerificationFailed {
		return exitVerificationFailed
	}
	return exitOK
}

// verify looks up every attestation for fp and verifies it with
// evidence.Verify against the policy that trusts the throwaway key. As in
// the CLI, a verified candidate wins and rejected ones are listed, with one
// SECURITY line.
func (d *Demo) verify(c *contract.Contract, fp fingerprint.Fingerprint, errw io.Writer) evidence.Report {
	r := evidence.Report{Version: evidence.ReportVersion, TestID: c.Metadata.Name, Fingerprint: fp.Digest}
	atts := d.reg[fp.Digest]
	if len(atts) == 0 {
		r.Outcome, r.Reason = evidence.OutcomeMiss, fmt.Sprintf("%s: %s", d.Repo, evidence.ErrNotFound)
		return r
	}
	opts := evidence.VerifyOptions{
		ExpectedFingerprint: fp.Digest,
		ExpectedTestID:      c.Metadata.Name,
		Identities:          []evidence.Identity{{PublicKey: d.pub, TrustLevel: evidence.TrustDeveloper}},
		MinTrustLevel:       evidence.TrustDeveloper,
		Signature:           keyVerifier{&d.key.PublicKey},
	}
	var best *evidence.Verification
	for _, a := range atts {
		v, err := evidence.Verify(context.Background(), &a.env, opts)
		if err != nil {
			r.Rejected = append(r.Rejected, evidence.Rejection{Reference: a.ref, Outcome: evidence.OutcomeVerificationFailed, Reason: fmt.Sprintf("%s (trust policy %s)", err, policyFile)})
			continue
		}
		if best == nil {
			best, r.Reference = v, a.ref
		}
	}
	if len(r.Rejected) > 0 {
		cliout.Security(errw, len(r.Rejected), r.TestID)
	}
	if best == nil {
		first := r.Rejected[0]
		r.Outcome, r.Reference, r.Reason = first.Outcome, first.Reference, first.Reason
		return r
	}
	r.Outcome, r.Result, r.Signer, r.Predicate = evidence.OutcomeVerified, best.Predicate.Result, &best.Signer, best.Predicate
	return r
}

// push signs a passing result for fp as an in-toto statement in a DSSE
// envelope and stores it under a digest of the envelope.
func (d *Demo) push(c *contract.Contract, fp fingerprint.Fingerprint, start time.Time) string {
	env := map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH}
	maps.Copy(env, c.Environment.Toolchain)
	p := evidence.Predicate{
		SchemaVersion: evidence.SchemaVersion,
		Subject:       evidence.Subject{Name: c.Metadata.Name, Digest: map[string]string{"sha256": fp.Hex()}},
		Test:          evidence.Test{ID: c.Metadata.Name, Cacheability: string(c.Cacheability)},
		Result:        evidence.ResultPassed,
		Execution:     evidence.Execution{Fingerprint: fp.Digest, FingerprintVersion: fingerprint.Version, Environment: env},
		Runner:        evidence.Runner{Identity: "browser"},
		StartedAt:     start,
		FinishedAt:    time.Now().UTC(),
	}
	// ponytail: plain structs, Marshal cannot fail.
	raw, _ := json.Marshal(p)
	payload, _ := json.Marshal(evidence.Statement{
		Type:          evidence.StatementType,
		Subject:       []evidence.ResourceDigest{{Name: p.Subject.Name, Digest: p.Subject.Digest}},
		PredicateType: evidence.PredicateType,
		Predicate:     raw,
	})
	sig, _ := ecdsa.SignASN1(rand.Reader, d.key, pae(payload)) // ponytail: cannot fail for P-256
	e := evidence.Envelope{PayloadType: evidence.PayloadType, Payload: payload, Signatures: []evidence.Signature{{Sig: sig}}}
	body, _ := json.Marshal(e)
	sum := sha256.Sum256(body)
	ref := d.Repo + "@sha256:" + hex.EncodeToString(sum[:])
	d.reg[fp.Digest] = append(d.reg[fp.Digest], &stored{ref: ref, env: e})
	slices.SortFunc(d.reg[fp.Digest], func(a, b *stored) int { return strings.Compare(a.ref, b.ref) })
	return ref
}

// pae is the SHA-256 of the DSSE pre-authentication encoding of an in-toto
// payload, the bytes a DSSE signature covers.
func pae(payload []byte) []byte {
	sum := sha256.Sum256(fmt.Appendf(nil, "DSSEv1 %d %s %d %s", len(evidence.PayloadType), evidence.PayloadType, len(payload), payload))
	return sum[:]
}

// keyVerifier checks a DSSE signature with one ECDSA P-256 public key, the
// role Sigstore's bundle verification plays in the CLI.
type keyVerifier struct{ pub *ecdsa.PublicKey }

func (k keyVerifier) VerifySignature(_ context.Context, env *evidence.Envelope) (evidence.Signer, error) {
	if len(env.Signatures) != 1 || !ecdsa.VerifyASN1(k.pub, pae(env.Payload), env.Signatures[0].Sig) {
		return evidence.Signer{}, errors.New("the ECDSA P-256 signature does not match the envelope")
	}
	der, _ := x509.MarshalPKIXPublicKey(k.pub)
	kid, _ := evidence.PublicKeyID(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})))
	return evidence.Signer{KeyID: kid}, nil
}
