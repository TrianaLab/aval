// Package acceptance holds level 4 tests: whole user stories through the
// aval binary, no cluster and no network (except uv in
// TestUVMonorepoShards).
package acceptance

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var aval string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "aval-acceptance")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	aval = filepath.Join(dir, "aval")
	build := exec.Command("go", "build", "-o", aval, "../../cmd/aval")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	code := 1
	if err := build.Run(); err == nil {
		code = m.Run()
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// run executes the binary and returns stdout and the exit code.
func run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	cmd := exec.Command(aval, args...)
	cmd.Stdout = &out
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return out.String(), 0
	case errors.As(err, &exit):
		return out.String(), exit.ExitCode()
	}
	t.Fatal(err)
	return "", 0
}

func writeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const contract = `apiVersion: aval.trianalab.io/v1
kind: Test
metadata:
  name: story
runner:
  command: ["true"]
scope:
  source: ["src/**"]
cacheability: hermetic
`

var digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// A user fingerprints the same project from two checkouts and gets the same
// digest; editing a declared input changes it; a broken contract is a usage
// error (exit 2).
func TestFingerprintStory(t *testing.T) {
	files := map[string]string{"aval.yaml": contract, "src/a.txt": "a\n", "README.md": "out of scope\n"}
	one, two := writeProject(t, files), writeProject(t, files)

	a, code := run(t, "run", "--fingerprint-only", "-f", filepath.Join(one, "aval.yaml"))
	a = strings.TrimSpace(a)
	if code != 0 || !digest.MatchString(a) {
		t.Fatalf("run --fingerprint-only: exit %d, output %q", code, a)
	}
	if b, _ := run(t, "run", "--fingerprint-only", "-f", filepath.Join(two, "aval.yaml")); strings.TrimSpace(b) != a {
		t.Fatalf("second checkout fingerprint %q, want %q", b, a)
	}

	if err := os.WriteFile(filepath.Join(two, "README.md"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := run(t, "run", "--fingerprint-only", "-f", filepath.Join(two, "aval.yaml")); strings.TrimSpace(b) != a {
		t.Fatal("editing an undeclared file changed the fingerprint")
	}
	if err := os.WriteFile(filepath.Join(two, "src", "a.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := run(t, "run", "--fingerprint-only", "-f", filepath.Join(two, "aval.yaml")); strings.TrimSpace(b) == a {
		t.Fatal("editing a declared input kept the fingerprint: false hit")
	}

	explain, code := run(t, "run", "--explain", "--fingerprint-only", "-f", filepath.Join(one, "aval.yaml"))
	if code != 0 || !strings.Contains(explain, "src/a.txt") || strings.Contains(explain, "README.md") {
		t.Fatalf("run --explain: exit %d, manifest:\n%s", code, explain)
	}

	bad := writeProject(t, map[string]string{"aval.yaml": contract + "unknown: field\n", "src/a.txt": "a\n"})
	if _, code := run(t, "run", "--fingerprint-only", "-f", filepath.Join(bad, "aval.yaml")); code != 2 {
		t.Fatalf("unknown contract field: exit %d, want 2", code)
	}
}
