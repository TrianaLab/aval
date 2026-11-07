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

// A monorepo (testdata/monorepo, the example of the guide "Depend on
// another part of the repo") where the frontend tests depend on the backend
// through scope.contracts: run --push publishes, the next run reuses, a
// change to a backend file outside the backend's scope keeps the reuse, and
// a change to one inside it makes the frontend miss and run. --explain
// shows the dependency.
func TestContractsMonorepoStory(t *testing.T) {
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/monorepo-aval"
	keys := t.TempDir()
	key, pub := keyPair(t, keys, "dev")
	policy := writePolicy(t, keys, "trusted.yaml", pub)

	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "monorepo"))); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".git/HEAD", "ref: refs/heads/main\n") // the boundary of scope.contracts
	file := filepath.Join(dir, "frontend", "aval.yaml")
	result := filepath.Join(dir, "frontend", ".aval", "result.json")
	runs := func() int {
		b, _ := os.ReadFile(filepath.Join(dir, "frontend", "runs.log"))
		return strings.Count(string(b), "ran\n")
	}

	steps := []struct {
		name   string
		change func()
		args   []string
		runs   int
		stdout string
	}{
		{"run and publish", func() {}, []string{"--push", "--key", key}, 1, "pushed " + repo + "@sha256:"},
		{"reuse", func() {}, []string{"--policy", policy}, 1, "reused passed result\n"},
		{"backend file outside its scope keeps the reuse", func() { write("backend/README.md", "edited\n") }, []string{"--policy", policy}, 1, "reused passed result\n"},
		{"backend file in its scope misses", func() { write("backend/api/api.go", "package api // v2\n") },
			[]string{"--policy", policy}, 2, "miss frontend"},
	}
	for _, s := range steps {
		s.change()
		args := append([]string{"run", "-f", file, "--registry", repo, "--result", result}, s.args...)
		stdout, stderr, code := avalEnv(t, args...)
		if code != 0 || runs() != s.runs || !strings.Contains(stdout, s.stdout) {
			t.Fatalf("%s: exit %d, %d runs; want 0, %d\nstdout %s\nstderr %s", s.name, code, runs(), s.runs, stdout, stderr)
		}
	}
	if stdout, _, code := avalEnv(t, "run", "-f", file, "--explain", "--fingerprint-only"); code != 0 || !strings.Contains(stdout, "\ndepends on ../backend/aval.yaml (backend) sha256:") {
		t.Fatalf("--explain: exit %d\n%s", code, stdout)
	}
}
