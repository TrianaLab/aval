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

// A TestSet of three tests (#207) goes through run, push and verify with
// --test and no output flags: each test runs and records into its own
// .aval/<name>/, push publishes that result, verify finds it, and the next
// run of each test reuses its own result. Without --test every command
// exits 2.
func TestTestSetStory(t *testing.T) {
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/testset-aval"
	keys := t.TempDir()
	key, pub := keyPair(t, keys, "dev")
	policy := writePolicy(t, keys, "trusted.yaml", pub)

	dir := writeProject(t, map[string]string{
		".git/HEAD": "ref: refs/heads/main\n",
		"src/a.txt": "a\n",
		"aval.yaml": `apiVersion: aval.trianalab.io/v1
kind: TestSet
defaults: {scope: {source: ["src/**"]}, cacheability: hermetic}
tests:
  - metadata: {name: unit}
    runner: {command: [sh, -c, "echo unit >> runs.log"]}
  - metadata: {name: "shard-{i}"}
    runner: {command: [sh, -c, "echo shard-{i} >> runs.log"]}
    matrix: {i: ["1", "2"]}
`,
	})
	names := []string{"unit", "shard-1", "shard-2"}
	// step runs aval in dir with the registry and fails unless it exits code
	// with want in stdout (want in stderr when code is not 0).
	step := func(code int, want string, args ...string) {
		t.Helper()
		stdout, stderr, got := avalIn(t, dir, append(args, "--registry", repo)...)
		if got != code || !strings.Contains(map[bool]string{true: stdout, false: stderr}[code == 0], want) {
			t.Fatalf("%v: exit %d\nstdout %s\nstderr %s", args, got, stdout, stderr)
		}
	}
	const many = "the TestSet holds 3 tests (unit, shard-1, shard-2); select one with --test"
	step(2, many, "run")
	step(2, many, "push", "--key", key)
	step(2, many, "verify", "--policy", policy)
	for _, n := range names {
		step(0, "miss "+n, "run", "--test", n, "--policy", policy)
	}
	for _, n := range names {
		for _, f := range []string{"result", "report", "record"} {
			if _, err := os.Stat(filepath.Join(dir, ".aval", n, f+".json")); err != nil {
				t.Fatal(err)
			}
		}
		step(0, "pushed "+repo+"@sha256:", "push", "--test", n, "--key", key)
		step(0, "verified "+n, "verify", "--test", n, "--policy", policy)
	}
	for _, n := range names {
		step(0, "reused passed result", "run", "--test", n, "--policy", policy)
		// The report of the reusing run makes push publish nothing.
		step(0, "nothing to push: "+n, "push", "--test", n, "--key", key)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "runs.log")); string(b) != "unit\nshard-1\nshard-2\n" {
		t.Fatalf("runs.log %q", b)
	}
	stdout, _, code := avalIn(t, dir, "explain", "--all")
	if lines := strings.Split(strings.TrimSpace(stdout), "\n"); code != 0 || len(lines) != 3 || !strings.HasPrefix(lines[2], "shard-2 sha256:") {
		t.Fatalf("explain --all: exit %d, %q", code, stdout)
	}
}
