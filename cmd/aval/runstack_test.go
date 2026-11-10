package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/contract"
)

const sep = string(os.PathListSeparator)

// resolved is p made absolute and symlink-free, as runStack records it.
func resolved(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// runStack appends the absolute, symlink-resolved contract to the outer
// stack: a relative path and a path through a symlinked directory record the
// same entry as the real path.
func TestRunStackNormalises(t *testing.T) {
	real := filepath.Dir(writeContract(t, "hermetic"))
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	self := resolved(t, filepath.Join(real, "aval.yaml"))
	t.Chdir(real)
	for _, p := range []string{"aval.yaml", filepath.Join(link, "aval.yaml"), filepath.Join(real, "aval.yaml")} {
		t.Setenv(contract.RunStackEnv, "")
		if got, err := runStack(p); err != nil || got != self {
			t.Errorf("runStack(%s) = %q, %v; want %q", p, got, err, self)
		}
		t.Setenv(contract.RunStackEnv, "/outer/aval.yaml")
		if got, _ := runStack(p); got != "/outer/aval.yaml"+sep+self {
			t.Errorf("runStack(%s) under an outer run = %q", p, got)
		}
	}
}

// A contract already in the stack is refused, whichever path names it.
func TestRunStackRefusesSameContract(t *testing.T) {
	real := filepath.Dir(writeContract(t, "hermetic"))
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	stack := "/outer/aval.yaml" + sep + resolved(t, filepath.Join(real, "aval.yaml"))
	t.Setenv(contract.RunStackEnv, stack)
	p := filepath.Join(link, "aval.yaml")
	_, err := runStack(p)
	want := p + " is already running in an outer aval run (" + stack + "): runner.command must not start the same contract again"
	if !errors.As(err, new(usageError)) || err.Error() != want {
		t.Fatalf("err = %v, want usage error %q", err, want)
	}
}

func TestRunStackMissingContract(t *testing.T) {
	if _, err := runStack(filepath.Join(t.TempDir(), "aval.yaml")); err == nil {
		t.Fatal("no error for a missing contract")
	}
}

// The child gets exactly one AVAL_RUN_STACK: this run's, not the inherited.
func TestChildEnvSetsRunStack(t *testing.T) {
	t.Setenv(contract.RunStackEnv, "/outer/aval.yaml")
	var got []string
	for _, kv := range childEnv("/outer/aval.yaml" + sep + "/self/aval.yaml") {
		if strings.HasPrefix(kv, contract.RunStackEnv+"=") {
			got = append(got, kv)
		}
	}
	if want := []string{contract.RunStackEnv + "=/outer/aval.yaml" + sep + "/self/aval.yaml"}; !slices.Equal(got, want) {
		t.Fatalf("child env %q, want %q", got, want)
	}
}

// aval run inside its own runner.command exits 2 before running anything
// or recording a result.
func TestRunRefusesNestedSameContract(t *testing.T) {
	c := writeContract(t, "hermetic")
	t.Setenv(contract.RunStackEnv, resolved(t, c))
	result := filepath.Join(filepath.Dir(c), ".aval", "result.json")
	code, stdout, stderr := runCLI("run", "-f", c, "--result-file", result)
	if code != ExitUsage || !strings.Contains(stderr, "is already running in an outer aval run") || strings.Contains(stdout, "--- run") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, err := os.Stat(result); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("result recorded: %v", err)
	}
}

// runner.command sees the stack with this contract on top.
func TestRunPassesRunStack(t *testing.T) {
	c := writeContract(t, "hermetic")
	dir := filepath.Dir(c)
	body, _ := os.ReadFile(c)
	body = []byte(strings.Replace(string(body), `["true"]`, `["sh", "-c", "printf %s \"$AVAL_RUN_STACK\" > stack.out"]`, 1))
	if err := os.WriteFile(c, body, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(contract.RunStackEnv, "/outer/aval.yaml")
	if code, _, stderr := runCLI("run", "-f", c, "--result-file", filepath.Join(dir, ".aval", "r.json")); code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "stack.out"))
	if want := "/outer/aval.yaml" + sep + resolved(t, c); string(got) != want {
		t.Fatalf("runner saw %q, want %q", got, want)
	}
}
