package acceptance

import (
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

// Story 17.6: aval gen go --go-cover-profile writes tests that publish their
// package's coverage profile; after the profile is deleted, the next run
// reuses the result and restores it without running go test.
func TestGenGoCoverProfileRestoredOnReuse(t *testing.T) {
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/shop-aval"
	key, pub := keyPair(t, t.TempDir(), "dev")
	policy := writePolicy(t, t.TempDir(), "policy.yaml", pub)

	dir := filepath.Join(t.TempDir(), "shop")
	if err := os.CopyFS(dir, os.DirFS("../../internal/gen/golang/testdata/shop")); err != nil {
		t.Fatal(err)
	}
	// The go.module probe checks the toolchain: pin the one installed.
	version, err := exec.Command("go", "env", "GOVERSION").Output()
	if err != nil {
		t.Fatal(err)
	}
	mod := "module example.com/shop\n\ngo 1.22\n\ntoolchain " + strings.TrimSpace(string(version)) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "aval.yaml")
	if stdout, stderr, code := avalEnv(t, "gen", "go", "-f", file, "--go-cover-profile", "cover"); code != 0 {
		t.Fatalf("gen go: exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	profile := filepath.Join(dir, "cover", "go.internal.money.out")
	args := []string{"run", "-f", file, "--test", "go.internal.money", "--registry", repo}
	if stdout, stderr, code := avalEnv(t, append(args, "--push", "--key", key)...); code != 0 || !strings.Contains(stdout, "pushed "+repo+"@sha256:") {
		t.Fatalf("run --push: exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	want, err := os.ReadFile(profile)
	if err != nil || !strings.HasPrefix(string(want), "mode: set\nexample.com/shop/internal/money/money.go:") {
		t.Fatalf("profile after the run: %q, %v", want, err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "cover")); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := avalEnv(t, append(args, "--policy", policy)...)
	got, _ := os.ReadFile(profile)
	if code != 0 || string(got) != string(want) || !strings.Contains(stdout, "restored cover/go.internal.money.out\nreused passed result\n") {
		t.Fatalf("reuse: exit %d, profile %q\nstdout %s\nstderr %s", code, got, stdout, stderr)
	}
}
