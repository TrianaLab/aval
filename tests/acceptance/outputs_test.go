package acceptance

import (
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ggcr "github.com/google/go-containerregistry/pkg/registry"
)

// Story 12.1: run --push publishes coverage.xml with the result; after the
// file is deleted, the next run reuses the result and restores the exact
// bytes without running the test; verify checks the outputs and writes
// nothing.
func TestOutputsRestoredOnReuse(t *testing.T) {
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/outputs-aval"
	key, pub := keyPair(t, t.TempDir(), "dev")
	policy := writePolicy(t, t.TempDir(), "policy.yaml", pub)

	const coverage = `<coverage>0.93</coverage>`
	runner := strings.Replace(contract, `["true"]`, `["sh", "-c", "echo ran >> runs.log; printf '`+coverage+`' > coverage.xml"]`, 1) + "outputs: [coverage.xml]\n"
	dir := writeProject(t, map[string]string{"aval.yaml": runner, "src/a.txt": "a\n"})
	file, out := filepath.Join(dir, "aval.yaml"), filepath.Join(dir, "coverage.xml")
	runs := func() int {
		b, _ := os.ReadFile(filepath.Join(dir, "runs.log"))
		return strings.Count(string(b), "ran\n")
	}

	if stdout, stderr, code := avalEnv(t, "run", "-f", file, "--registry", repo, "--push", "--key", key); code != 0 || !strings.Contains(stdout, "pushed "+repo+"@sha256:") {
		t.Fatalf("run --push: exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	if err := os.Remove(out); err != nil {
		t.Fatal(err)
	}
	if stdout, stderr, code := avalEnv(t, "verify", "-f", file, "--registry", repo, "--policy", policy); code != 0 || fileExists(out) {
		t.Fatalf("verify: exit %d, coverage.xml written: %v\nstdout %s\nstderr %s", code, fileExists(out), stdout, stderr)
	}
	stdout, stderr, code := avalEnv(t, "run", "-f", file, "--registry", repo, "--policy", policy)
	got, _ := os.ReadFile(out)
	if code != 0 || runs() != 1 || string(got) != coverage || !strings.Contains(stdout, "restored coverage.xml\nreused passed result\n") {
		t.Fatalf("reuse: exit %d, %d runs, coverage.xml %q\nstdout %s\nstderr %s", code, runs(), got, stdout, stderr)
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
