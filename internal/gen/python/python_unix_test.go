//go:build unix

package python

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePython puts an executable python3 that runs script first on PATH.
func fakePython(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "python3"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

func TestFakePythonFails(t *testing.T) {
	root := t.TempDir()
	write(t, root, "pyproject.toml", "[project]\nname = \"a\"\n")
	for script, want := range map[string]string{
		"exit 3\n":               "python: exit status 3",
		"echo not json\n":        "python: invalid character",
		"echo old >&2\nexit 1\n": "python: old",
	} {
		fakePython(t, script)
		if _, err := (&Adapter{}).Units(root); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err %v, want %q", script, err, want)
		}
	}
	// The second call, for the sources, fails too.
	fakePython(t, `read in; case "$in" in *'"manifests":[]'*) exit 4;; esac
echo '{"manifests":{"pyproject.toml":{"project":{"name":"a"}}}}'
`)
	if _, err := (&Adapter{}).Units(root); err == nil || !strings.Contains(err.Error(), "exit status 4") {
		t.Errorf("sources: err %v", err)
	}
}
