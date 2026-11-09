package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/TrianaLab/aval/internal/cliout"
	"github.com/TrianaLab/aval/internal/policy"
	"github.com/TrianaLab/aval/pkg/evidence"
)

const reachHint = "; is the registry running? check AVAL_REGISTRY or --registry"

func TestUnreachable(t *testing.T) {
	at := func(u string, err error) error { return &url.Error{Op: "Get", URL: u, Err: err} }
	reg := "https://localhost:5000/v2/acme/r/manifests/x"
	dial := at(reg, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")})
	dns := &net.DNSError{Err: "no such host", Name: "localhost"}
	cases := []struct {
		repo string
		err  error
		want string
	}{
		{"localhost:5000/acme/r", fmt.Errorf("resolving: %w", dial), "cannot reach localhost:5000 (connect: connection refused)" + reachHint},
		{"localhost:5000/acme/r", at(reg, &net.OpError{Op: "dial", Err: dns}), "cannot reach localhost:5000 (no such host)" + reachHint},
		{"localhost:5000/acme/r", fmt.Errorf("x: %w", at(reg, context.DeadlineExceeded)), "cannot reach localhost:5000 (timed out; see --registry-timeout)" + reachHint},
		{"localhost:5000/acme/r", at(reg, os.ErrDeadlineExceeded), "cannot reach localhost:5000 (timed out; see --registry-timeout)" + reachHint},
		{"localhost:5000/acme/r", at(reg, &net.OpError{Op: "read", Err: errors.New("reset")}), ""},
		{"localhost:5000/acme/r", errors.New("401 unauthorized"), ""},
		{"localhost:5000/acme/r", nil, ""},
		// Not aimed at the registry: a scope.resolve or Sigstore request
		// that failed must not blame AVAL_REGISTRY.
		{"localhost:5000/r", fmt.Errorf("GET: %w", at("https://registry.npmjs.org/x", dns)), ""},
		{"localhost:5000/r", at("https://tuf-repo-cdn.sigstore.dev/root.json", context.DeadlineExceeded), ""},
		{"localhost:5000/r", &net.OpError{Op: "dial", Err: dns}, ""},
		{"localhost:5000/r", at("%zz", dns), ""},
		{"", at(reg, dns), ""},
	}
	for _, tc := range cases {
		if got := unreachable(tc.repo, tc.err); got != tc.want {
			t.Errorf("unreachable(%q, %v) = %q, want %q", tc.repo, tc.err, got, tc.want)
		}
	}
	err := reachError("localhost:5000/r", dial)
	if !errors.Is(err, dial) || exitCode(err) != ExitError || !strings.HasPrefix(err.Error(), "cannot reach localhost:5000") {
		t.Fatalf("reachError: %v", err)
	}
	if other := errors.New("x"); reachError("r/x", other) != other {
		t.Fatal("reachError must keep other errors")
	}
}

// False-hit style: the hint is only said, never written into a report. The
// verify text and JSON reports and the run report keep the raw error, and
// exit codes and outcomes stay what they were.
func TestUnreachableKeepsTheReason(t *testing.T) {
	dial := fmt.Errorf("reg.example/r: resolving: %w", &url.Error{Op: "Get", URL: "https://reg.example/v2/r/tags/list", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}})
	fakeVerify(t, dial, nil, nil)
	hint := "cannot reach reg.example (connect: connection refused)" + reachHint
	c := writeContract(t, "hermetic")
	for _, format := range []string{"text", "json"} {
		code, stdout, stderr := runCLI("verify", "-f", c, "--registry", "reg.example/r", "--policy", "p.yaml", "-o", format)
		var b strings.Builder
		_ = writeReportFor(&b, format, dial, c)
		if code != ExitError || stdout != b.String() || stderr != "aval: "+hint+"\n" {
			t.Fatalf("verify -o %s: exit %d, stdout %q (want %q), stderr %q", format, code, stdout, b.String(), stderr)
		}
	}
	report := filepath.Join(t.TempDir(), "report.json")
	code, stdout, stderr := runCLI("run", "-f", c, "--registry", "reg.example/r", "--policy", "p.yaml", "--report-file", report, "--result-file", filepath.Join(t.TempDir(), "r.json"))
	r := readReport(t, report)
	if code != ExitOK || r.Outcome != evidence.OutcomeError || r.Reason != dial.Error() || !strings.Contains(stdout, "reason: "+dial.Error()+"\n") ||
		!strings.Contains(stderr, "aval: warning: reuse off: "+hint+"\n") {
		t.Fatalf("run: exit %d, report %+v, stdout %q, stderr %q", code, r, stdout, stderr)
	}
}

// writeReportFor is the report verify printed before #167 for a lookup
// that failed with err.
func writeReportFor(b *strings.Builder, format string, err error, c string) error {
	ct, fp, lerr := loadAndFingerprint(context.Background(), c)
	if lerr != nil {
		return lerr
	}
	r, _ := failReport(newReport(ct, fp), err)
	return cliout.WriteReport(b, format, r)
}

// A public key as --key says which file to pass instead, with exit 2 and
// before the test runs.
func TestPublicKeyAsKey(t *testing.T) {
	fakeLogin(t, false, "", nil)
	dir := t.TempDir()
	_, pub := devKey(t, dir, "aval")
	pubFile := filepath.Join(dir, "aval.pub")
	other := filepath.Join(dir, "trusted.pem")
	for _, p := range []string{pubFile, other} {
		if err := os.WriteFile(p, pub, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	argv, ran := marker(t, 0)
	c := contractWith(t, "hermetic", argv...)
	for key, want := range map[string]string{
		pubFile: "aval: --key " + pubFile + " is a public key; pass the private key (" + filepath.Join(dir, "aval.key") + ")\n",
		other:   "aval: --key " + other + " is a public key; pass the private key\n",
	} {
		for _, cmd := range []string{"run", "push"} {
			args := []string{cmd, "-f", c, "--registry", "reg.example/r", "--key", key, "--result-file", filepath.Join(t.TempDir(), "r.json")}
			if cmd == "run" {
				args = append(args, "--push")
			}
			if code, _, stderr := runCLI(args...); code != ExitUsage || ran() || stderr != want {
				t.Errorf("%s --key %s: exit %d, stderr %q, want %q", cmd, key, code, stderr, want)
			}
		}
	}
}

// Without a key, a token or a terminal, signing lists the three ways to
// sign, one indented line each, with exit 2.
func TestSigningNeedsList(t *testing.T) {
	fakeLogin(t, false, "", nil)
	t.Setenv(idTokenEnv, "")
	c := writeContract(t, "hermetic")
	want := "aval: signing needs one of:\n" +
		"  --key FILE: a private key (create one with: aval keygen)\n" +
		"  $SIGSTORE_ID_TOKEN: an OIDC token, for keyless signing\n" +
		"  --login, or a terminal: the interactive Sigstore login, for keyless signing\n"
	for _, args := range [][]string{
		{"run", "-f", c, "--registry", "reg.example/r", "--push"},
		{"push", "-f", c, "--registry", "reg.example/r"},
	} {
		if code, _, stderr := runCLI(args...); code != ExitUsage || stderr != want {
			t.Errorf("%s: exit %d, stderr %q", args[0], code, stderr)
		}
	}
}

// push without a saved result keeps exit 1 and says how to get one.
func TestPushWithoutResult(t *testing.T) {
	key, _ := devKey(t, t.TempDir(), "k")
	none := filepath.Join(t.TempDir(), "none.json")
	code, _, stderr := runCLI("push", "-f", writeContract(t, "hermetic"), "--registry", "reg.example/r", "--key", key, "--result-file", none)
	if code != ExitError || !strings.HasPrefix(stderr, "aval: reading the result recorded by 'aval run': open "+none) || !strings.HasSuffix(stderr, "; run aval run first, or use aval run --push\n") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	// Any other read error keeps its message without the hint.
	code, _, stderr = runCLI("push", "-f", writeContract(t, "hermetic"), "--registry", "reg.example/r", "--key", key, "--result-file", t.TempDir())
	if code != ExitError || strings.Contains(stderr, "run aval run first") {
		t.Fatalf("directory: exit %d, stderr %q", code, stderr)
	}
}

// Signing with a key the active policy does not list warns on stderr only:
// the exit code, stdout and what is published stay the same.
func TestUntrustedKeyWarning(t *testing.T) {
	dir := t.TempDir()
	key, pub := devKey(t, dir, "dev")
	c := writeContract(t, "hermetic")
	run := func(ids []evidence.Identity) (int, string, string, []*evidence.Statement) {
		signed := fakePublish(t, nil, nil)
		fakeCandidates(t, fmt.Errorf("none: %w", evidence.ErrNotFound), nil)
		loadPolicy = func(string) (*policy.Policy, error) { return &policy.Policy{Identities: ids}, nil }
		code, stdout, stderr := runCLI("run", "-f", c, "--registry", "reg.example/r", "--policy", "p.yaml", "--push", "--key", key, "--result-file", filepath.Join(t.TempDir(), "r.json"))
		return code, stdout, stderr, *signed
	}
	_, other := devKey(t, dir, "other")
	codeT, outT, errT, signedT := run([]evidence.Identity{{PublicKey: string(other)}, {Issuer: "i", SubjectRegexp: "s"}, {PublicKey: string(pub)}})
	codeU, outU, errU, signedU := run([]evidence.Identity{{PublicKey: string(other)}})
	kid, _ := evidence.PublicKeyID(string(pub))
	warning := "aval: warning: key " + key + " (key id " + kid + ") is not in p.yaml; consumers with this policy will reject the result\n"
	took := regexp.MustCompile(` in [0-9.]+(ns|µs|ms|s)\b`)
	norm := func(s string) string { return took.ReplaceAllString(s, " in D") }
	if codeT != ExitOK || codeU != codeT || norm(outU) != norm(outT) || strings.Contains(outU, "warning") ||
		strings.Contains(errT, "is not in") || !strings.Contains(errU, warning) || len(signedT) != 1 || len(signedU) != 1 ||
		!reflect.DeepEqual(stable(t, signedT[0]), stable(t, signedU[0])) {
		t.Fatalf("trusted: exit %d stderr %q; untrusted: exit %d stdout %q stderr %q", codeT, errT, codeU, outU, errU)
	}

	// Nothing to compare: no key, no policy, or either unreadable.
	var b strings.Builder
	loadPolicy = func(string) (*policy.Policy, error) { return nil, errors.New("unreadable") }
	t.Cleanup(func() { loadPolicy = policy.Load })
	for _, kp := range [][2]string{{"", "p.yaml"}, {key, ""}, {filepath.Join(dir, "nope.key"), "p.yaml"}, {key, "p.yaml"}} {
		warnUntrustedKey(&b, kp[0], kp[1])
	}
	if b.Len() != 0 {
		t.Fatalf("warned %q", b.String())
	}
}

// stable is the predicate of s without its timestamps, which differ between
// any two runs.
func stable(t *testing.T, s *evidence.Statement) evidence.Predicate {
	t.Helper()
	var p evidence.Predicate
	if err := json.Unmarshal(s.Predicate, &p); err != nil {
		t.Fatal(err)
	}
	p.StartedAt, p.FinishedAt = time.Time{}, time.Time{}
	return p
}

// Contract errors print one indented line per problem under one aval: line,
// in YAML terms, each escaped, with exit 2.
func TestContractErrorLines(t *testing.T) {
	noLookup(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "aval.yaml")
	body := strings.NewReplacer(`source: ["*.txt"]`, "sources: [\"*.txt\"]\n  \"x\\e]8;;\": [a]", `command: ["true"]`, "command: true").Replace(testContract)
	if err := os.WriteFile(p, []byte(strings.Replace(body, "CLASS", "hermetic", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	want := "aval: " + p + ": invalid contract:\n" +
		"  line 6: runner.command: got a boolean `true`, want a list of strings\n" +
		"  line 8: scope.sources: unknown field; did you mean source?\n" +
		"  line 9: scope.x\\x1b]8;;: unknown field; known fields: source, exclude, dependencies, allowMutable, resolve, contracts\n"
	for _, cmd := range []string{"run", "verify", "explain"} {
		code, stdout, stderr := runCLI(cmd, "-f", p, "--registry", "reg.example/r")
		if cmd == "explain" {
			code, stdout, stderr = runCLI(cmd, "-f", p)
		}
		if code != ExitUsage || stdout != "" || stderr != want {
			t.Errorf("%s: exit %d, stdout %q, stderr %q, want %q", cmd, code, stdout, stderr, want)
		}
	}
	// A validation problem prints the same way.
	if err := os.WriteFile(p, []byte(strings.Replace(testContract, "CLASS", "sometimes", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCLI("explain", "-f", p); code != ExitUsage || stderr != "aval: "+p+": invalid contract:\n  cacheability: unknown class \"sometimes\"\n" {
		t.Errorf("validation: exit %d, stderr %q", code, stderr)
	}
}
