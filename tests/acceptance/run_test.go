package acceptance

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ggcr "github.com/google/go-containerregistry/pkg/registry"
)

// avalEnv runs the binary with a private Docker config and returns stdout,
// stderr and the exit code.
func avalEnv(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := exec.Command(aval, args...)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	cmd.Env = append(os.Environ(), "DOCKER_CONFIG="+t.TempDir())
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return out.String(), errOut.String(), 0
	case errors.As(err, &exit):
		return out.String(), errOut.String(), exit.ExitCode()
	}
	t.Fatal(err)
	return "", "", 0
}

// keyPair writes a P-256 private key and its public key under dir.
func keyPair(t *testing.T, dir, name string) (priv, pub string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	pubDER, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	priv, pub = filepath.Join(dir, name+".key"), filepath.Join(dir, name+".pub")
	if err := os.WriteFile(priv, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pub, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

func writePolicy(t *testing.T, dir, name, pub string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	body := "apiVersion: aval.trianalab.io/v1\nkind: Policy\nminTrustLevel: developer\nidentities:\n  - publicKey: " + pub + "\n    trustLevel: developer\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The milestone 2 story against an in-process OCI registry: run --push
// executes and publishes; the next run with unchanged inputs reuses the
// result without executing; an attestation from a key the policy does not
// trust runs the test and exits 5; --shadow runs and confirms the reuse;
// changed inputs miss and run again.
func TestRunPushReuseStory(t *testing.T) {
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/story-aval"

	keys := t.TempDir()
	key, pub := keyPair(t, keys, "dev")
	_, otherPub := keyPair(t, keys, "other")
	trusted := writePolicy(t, keys, "trusted.yaml", pub)
	untrusted := writePolicy(t, keys, "untrusted.yaml", otherPub)

	runner := strings.Replace(contract, `["true"]`, `["sh", "-c", "echo ran >> runs.log"]`, 1)
	dir := writeProject(t, map[string]string{"aval.yaml": runner, "src/a.txt": "a\n"})
	file := filepath.Join(dir, "aval.yaml")
	result := filepath.Join(dir, ".aval", "result.json")
	runs := func() int {
		b, _ := os.ReadFile(filepath.Join(dir, "runs.log"))
		return strings.Count(string(b), "ran\n")
	}

	steps := []struct {
		name   string
		args   []string
		code   int
		runs   int
		stdout string
		stderr string
	}{
		{"run and publish", []string{"--push", "--key", key}, 0, 1, "pushed " + repo + "@sha256:", ""},
		{"reuse", []string{"--policy", trusted}, 0, 1, "reused passed result\n", ""},
		{"untrusted signer is a security event", []string{"--policy", untrusted}, 5, 2, "ran story: passed", "SECURITY"},
		{"shadow confirms the reuse", []string{"--policy", trusted, "--shadow"}, 0, 3, "would have been correct", ""},
	}
	for _, s := range steps {
		args := append([]string{"run", "-f", file, "--registry", repo, "--result", result}, s.args...)
		stdout, stderr, code := avalEnv(t, args...)
		if code != s.code || runs() != s.runs || !strings.Contains(stdout, s.stdout) || !strings.Contains(stderr, s.stderr) {
			t.Fatalf("%s: exit %d, %d runs; want %d, %d\nstdout %s\nstderr %s", s.name, code, runs(), s.code, s.runs, stdout, stderr)
		}
	}

	if stdout, _, code := avalEnv(t, "verify", "-f", file, "--registry", repo, "--policy", trusted); code != 0 || !strings.HasPrefix(stdout, "verified story") {
		t.Fatalf("verify: exit %d, %s", code, stdout)
	}

	if err := os.WriteFile(filepath.Join(dir, "src", "a.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := avalEnv(t, "run", "-f", file, "--registry", repo, "--result", result, "--policy", trusted)
	if code != 0 || runs() != 4 || !strings.Contains(stdout, "miss story") {
		t.Fatalf("changed input: exit %d, %d runs\nstdout %s\nstderr %s", code, runs(), stdout, stderr)
	}
}

// #195: a result signed by a key the policy does not trust, beside a trusted
// one for the same fingerprint, is a security event in the exit code alone:
// run does not reuse, runs the test and exits 5 with the SECURITY line last;
// verify prints the unchanged report (verified, the bad one in rejected) and
// exits 5.
func TestRejectedCandidateStory(t *testing.T) {
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/rejected-aval"

	keys := t.TempDir()
	key, pub := keyPair(t, keys, "dev")
	otherKey, _ := keyPair(t, keys, "other")
	trusted := writePolicy(t, keys, "trusted.yaml", pub)

	runner := strings.Replace(contract, `["true"]`, `["sh", "-c", "echo ran >> runs.log"]`, 1)
	dir := writeProject(t, map[string]string{"aval.yaml": runner, "src/a.txt": "a\n"})
	file := filepath.Join(dir, "aval.yaml")
	result := filepath.Join(dir, ".aval", "result.json")
	run := []string{"run", "-f", file, "--registry", repo, "--result", result}
	for _, k := range []string{key, otherKey} {
		if stdout, stderr, code := avalEnv(t, append(run, "--push", "--key", k)...); code != 0 {
			t.Fatalf("push with %s: exit %d\nstdout %s\nstderr %s", k, code, stdout, stderr)
		}
	}

	stdout, stderr, code := avalEnv(t, append(run, "--policy", trusted)...)
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if b, _ := os.ReadFile(filepath.Join(dir, "runs.log")); code != 5 || strings.Count(string(b), "ran\n") != 3 || strings.Contains(stdout, "reused") ||
		!strings.HasPrefix(lines[len(lines)-1], "aval: SECURITY: 1 attestation(s) for story failed") {
		t.Fatalf("run: exit %d, runs.log %q\nstdout %s\nstderr %s", code, b, stdout, stderr)
	}

	stdout, stderr, code = avalEnv(t, "verify", "-f", file, "--registry", repo, "--policy", trusted, "-o", "json")
	var r struct {
		Outcome, Reference string
		Rejected           []struct{ Outcome string }
	}
	if err := json.Unmarshal([]byte(stdout), &r); err != nil || code != 5 || r.Outcome != "verified" || r.Reference == "" ||
		len(r.Rejected) != 1 || r.Rejected[0].Outcome != "verification-failed" || !strings.Contains(stderr, "SECURITY") {
		t.Fatalf("verify: exit %d, %v\nstdout %s\nstderr %s", code, err, stdout, stderr)
	}
}

// The policy main and releases apply (#130): .github/aval/policy.yaml with
// the minimum raised to runner (#216), a CI identity at runner and a
// maintainer listed at developer. A fingerprint whose
// only result the maintainer signed is a miss (the test runs, verify exits 4,
// so a release stops), not a verification failure; once a runner-signed
// sibling exists it is reused from that one; an unlisted signer is still a
// security event (exit 5). Keys stand in for the keyless identities: the
// trust level comes from the policy entry either way.
func TestMainPolicyKnowsDevelopersStory(t *testing.T) {
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/main-policy-aval"

	keys := t.TempDir()
	devKey, devPub := keyPair(t, keys, "dev")
	ciKey, ciPub := keyPair(t, keys, "ci")
	otherKey, _ := keyPair(t, keys, "other")
	mainPolicy := filepath.Join(keys, "main.yaml")
	body := "apiVersion: aval.trianalab.io/v1\nkind: Policy\nminTrustLevel: runner\nidentities:\n" +
		"  - publicKey: " + ciPub + "\n    trustLevel: runner\n" +
		"  - publicKey: " + devPub + "\n    trustLevel: developer\n"
	if err := os.WriteFile(mainPolicy, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	runner := strings.Replace(contract, `["true"]`, `["sh", "-c", "echo ran >> runs.log"]`, 1)
	dir := writeProject(t, map[string]string{"aval.yaml": runner, "src/a.txt": "a\n"})
	file := filepath.Join(dir, "aval.yaml")
	result := filepath.Join(dir, ".aval", "result.json")
	runs := func() int {
		b, _ := os.ReadFile(filepath.Join(dir, "runs.log"))
		return strings.Count(string(b), "ran\n")
	}
	run := []string{"run", "-f", file, "--registry", repo, "--result", result}
	onMain := append(append([]string{}, run...), "--policy", mainPolicy)
	verify := []string{"verify", "-f", file, "--registry", repo, "--policy", mainPolicy}
	steps := []struct {
		name                      string
		args                      []string
		change                    bool // edit the source first: a fresh fingerprint
		code, runs                int
		stdout, notStdout, stderr string
	}{
		{"developer publishes", append(run, "--push", "--key", devKey), false, 0, 1, "pushed ", "", ""},
		{"developer-only is a miss on main", onMain, false, 0, 2, "trust level developer, policy requires at least runner", "reused", ""},
		{"verify stops a release on it", verify, false, 4, 2, "miss story", "", ""},
		{"CI publishes a runner sibling", []string{"push", "-f", file, "--registry", repo, "--key", ciKey, "--result", result}, false, 0, 2, "pushed ", "", ""},
		{"main reuses the runner one", onMain, false, 0, 2, "trust level runner", "", ""},
		{"verify picks the runner one", verify, false, 0, 2, "trust level runner", "", ""},
		{"unlisted signer publishes", append(run, "--push", "--key", otherKey), true, 0, 3, "pushed ", "", ""},
		{"unlisted signer is a security event", onMain, false, 5, 4, "ran story", "reused", "SECURITY"},
		{"verify fails on it", verify, false, 5, 4, "verification-failed story", "", "SECURITY"},
	}
	for _, s := range steps {
		if s.change {
			if err := os.WriteFile(filepath.Join(dir, "src", "a.txt"), []byte("b\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		stdout, stderr, code := avalEnv(t, s.args...)
		if code != s.code || runs() != s.runs || !strings.Contains(stdout, s.stdout) || (s.notStdout != "" && strings.Contains(stdout, s.notStdout)) ||
			!strings.Contains(stderr, s.stderr) || (s.stderr == "" && strings.Contains(stderr, "SECURITY")) {
			t.Fatalf("%s: exit %d, %d runs; want %d, %d\nstdout %s\nstderr %s", s.name, code, runs(), s.code, s.runs, stdout, stderr)
		}
	}
}

// A contract that declares the whole tree (scope.source ["**"]) is not
// changed by Aval's own outputs (#63): a run writes its result and report
// inside the root, the next run gets the same fingerprint, and push accepts
// the recorded result. One output lives under .aval/, the other at a custom
// --result path that only the flag excludes.
func TestWholeTreeScopeSurvivesOwnOutputs(t *testing.T) {
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/whole-tree-aval"
	key, _ := keyPair(t, t.TempDir(), "dev")

	whole := strings.Replace(contract, `["src/**"]`, `["**"]`, 1)
	dir := writeProject(t, map[string]string{"aval.yaml": whole, "src/a.txt": "a\n"})
	file := filepath.Join(dir, "aval.yaml")
	result := filepath.Join(dir, "out", "result.json")
	report := filepath.Join(dir, ".aval", "report.json")

	fingerprint := func(stdout string) string { // from the outcome line
		_, rest, _ := strings.Cut(stdout, "miss story ")
		fp, _, _ := strings.Cut(rest, "\n")
		return fp
	}
	var fps []string
	for i := range 2 {
		stdout, stderr, code := avalEnv(t, "run", "-f", file, "--result", result, "--report", report)
		if code != 0 || !strings.Contains(stdout, "ran story: passed") {
			t.Fatalf("run %d: exit %d\nstdout %s\nstderr %s", i+1, code, stdout, stderr)
		}
		fps = append(fps, fingerprint(stdout))
	}
	if !digest.MatchString(fps[0]) || fps[0] != fps[1] {
		t.Fatalf("fingerprint changed across runs: %q then %q", fps[0], fps[1])
	}
	stdout, stderr, code := avalEnv(t, "push", "-f", file, "--registry", repo, "--key", key, "--result", result)
	if code != 0 || !strings.Contains(stdout, "pushed "+repo+"@sha256:") {
		t.Fatalf("push: exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
}
