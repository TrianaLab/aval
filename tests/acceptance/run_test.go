package acceptance

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
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
		{"reuse", []string{"--policy", trusted}, 0, 1, "reused passed result " + repo + "@sha256:", ""},
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
