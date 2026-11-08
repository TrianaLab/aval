package acceptance

import (
	"io"
	"io/fs"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ggcr "github.com/google/go-containerregistry/pkg/registry"
)

// Story 12.2: examples/uv-monorepo as a two-shard matrix sharing one
// checkout. Each shard publishes its result and coverage, then reuses the
// result and restores its coverage after the file is deleted; a change to a
// file only shard 2 reads leaves shard 1 reusable. The one acceptance test
// that reaches the network: uv installs the locked packages from PyPI (or
// its cache). It fails without uv instead of skipping, so a pass of this
// suite (aval.e2e.yaml) always includes it.
func TestUVMonorepoShards(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Fatalf("uv is required (https://docs.astral.sh/uv/): %v", err)
	}
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/uv-monorepo-aval"
	key, pub := keyPair(t, t.TempDir(), "dev")
	policy := writePolicy(t, t.TempDir(), "policy.yaml", pub)
	dir := copyExample(t, filepath.Join("..", "..", "examples", "uv-monorepo"))

	// The matrix the guide shows runs these shards, each with its own --result.
	wf, err := os.ReadFile(filepath.Join(dir, "workflow.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"shard: [1, 2]", "-f aval.shard-${{ matrix.shard }}.yaml", "--result .aval/shard-${{ matrix.shard }}.json", "path: coverage-shard-${{ matrix.shard }}.xml", "--version v0.5.0", "branches: [main]"} {
		if !strings.Contains(string(wf), want) {
			t.Errorf("workflow.yaml does not contain %q", want)
		}
	}

	for _, shard := range []string{"1", "2"} {
		publishAndRestore(t, dir, shard, repo, key, policy)
	}

	app := filepath.Join(dir, "packages", "app", "src", "app", "__init__.py")
	f, err := os.OpenFile(app, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("# only shard 2 reads this\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	for shard, want := range map[string]int{"1": 0, "2": 4} {
		if stdout, stderr, code := avalEnv(t, "verify", "-f", filepath.Join(dir, "aval.shard-"+shard+".yaml"), "--registry", repo, "--policy", policy); code != want {
			t.Errorf("shard %s verify after an app change: exit %d, want %d\nstdout %s\nstderr %s", shard, code, want, stdout, stderr)
		}
	}
}

// publishAndRestore runs one shard with --push, deletes its coverage, and
// checks the next run reuses the result and restores the same bytes.
func publishAndRestore(t *testing.T, dir, shard, repo, key, policy string) {
	t.Helper()
	file := filepath.Join(dir, "aval.shard-"+shard+".yaml")
	result := filepath.Join(dir, ".aval", "shard-"+shard+".json")
	cov := filepath.Join(dir, "coverage-shard-"+shard+".xml")
	if stdout, stderr, code := avalEnv(t, "run", "-f", file, "--result", result, "--registry", repo, "--push", "--key", key); code != 0 {
		t.Fatalf("shard %s run --push: exit %d\nstdout %s\nstderr %s", shard, code, stdout, stderr)
	}
	want, err := os.ReadFile(cov)
	if err != nil || !strings.Contains(string(want), "<coverage") {
		t.Fatalf("shard %s: no coverage written: %v %q", shard, err, want)
	}
	if err := os.Remove(cov); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := avalEnv(t, "run", "-f", file, "--result", result, "--registry", repo, "--policy", policy)
	got, _ := os.ReadFile(cov)
	if code != 0 || string(got) != string(want) || !strings.Contains(stdout, "restored coverage-shard-"+shard+".xml\nreused passed result\n") {
		t.Fatalf("shard %s reuse: exit %d, coverage restored %v\nstdout %s\nstderr %s", shard, code, string(got) == string(want), stdout, stderr)
	}
}

// copyExample copies the regular files under src into a temporary directory,
// leaving out what a local uv or pytest run leaves behind.
func copyExample(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && (d.Name() == ".venv" || d.Name() == "__pycache__" || d.Name() == ".pytest_cache"):
			return filepath.SkipDir
		case !d.Type().IsRegular():
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}
