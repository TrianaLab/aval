package acceptance

import (
	"os"
	"path/filepath"
	"testing"
)

// A uv workspace (testdata/workspace, the example of the guide "Depend on
// another part of the repo"), mapped both ways: a contract at the root per
// member, and a member contract that names a root contract owning the
// lockfile. A lockfile change changes both fingerprints; a change in another
// member changes neither.
func TestWorkspaceStory(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "workspace"))); err != nil {
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
	fps := func() [2]string {
		t.Helper()
		var got [2]string
		for i, f := range []string{"aval.api.yaml", "packages/api/aval.yaml"} {
			out, errOut, code := avalIn(t, dir, "run", "--fingerprint-only", "-f", f)
			if code != 0 || errOut != "" {
				t.Fatalf("%s: exit %d\n%s%s", f, code, out, errOut)
			}
			got[i] = out
		}
		return got
	}
	before := fps()
	write("packages/web/src/web/__init__.py", "x = 1\n")
	if got := fps(); got != before {
		t.Errorf("another member changed the fingerprints: %v, want %v", got, before)
	}
	write("uv.lock", "version = 1\n")
	if got := fps(); got[0] == before[0] || got[1] == before[1] {
		t.Errorf("a lockfile change kept a fingerprint: %v, before %v", got, before)
	}
}
