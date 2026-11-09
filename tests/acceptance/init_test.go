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

// aval init, without a terminal, writes a contract, a key and a policy that
// work as they are: run --push publishes, the next run reuses.
func TestInitStory(t *testing.T) {
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/init-aval"
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "hello.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := avalIn(t, dir, "init", "--name", "hello", "--source", "src/**", "--cacheability", "hermetic", "--key", "aval.key", "--", "grep", "-q", "hello", "src/hello.txt")
	if code != 0 || !strings.Contains(stdout, "next: export AVAL_REGISTRY=") {
		t.Fatalf("init: exit %d\n%s%s", code, stdout, stderr)
	}
	if stdout, stderr, code = avalIn(t, dir, "run", "--registry", repo, "--push", "--key", "aval.key"); code != 0 || !strings.Contains(stdout, "summary: hello ran: passed") || !strings.Contains(stdout, "result pushed") {
		t.Fatalf("run --push: exit %d\n%s%s", code, stdout, stderr)
	}
	if stdout, stderr, code = avalIn(t, dir, "run", "--registry", repo, "--policy", "policy.yaml"); code != 0 || !strings.Contains(stdout, "summary: hello reused: passed") {
		t.Fatalf("run: exit %d\n%s%s", code, stdout, stderr)
	}
}
