package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A linked git worktree has a .git file holding a machine-specific path
// (#199). aval explain in the main checkout and in two worktrees of the same
// commit, with source "**", prints one fingerprint: the gitfile is VCS
// metadata, not an input.
func TestWorktreesShareFingerprint(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "main")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", main}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	git("init", "-q", "-b", "main")
	for name, body := range map[string]string{
		"aval.yaml": `apiVersion: aval.trianalab.io/v1
kind: Test
metadata: {name: unit}
runner: {command: ["sh", "test.sh"]}
scope: {source: ["**"]}
cacheability: hermetic
`,
		"test.sh": "echo ok\n",
	} {
		if err := os.WriteFile(filepath.Join(main, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")
	git("worktree", "add", "-q", filepath.Join(root, "wt-a"))
	git("worktree", "add", "-q", filepath.Join(root, "wt-b"))

	want, errOut, code := avalIn(t, main, "explain", "--digest")
	if code != 0 || errOut != "" {
		t.Fatalf("main: exit %d\n%s%s", code, want, errOut)
	}
	for _, wt := range []string{"wt-a", "wt-b"} {
		dir := filepath.Join(root, wt)
		if fi, err := os.Lstat(filepath.Join(dir, ".git")); err != nil || !fi.Mode().IsRegular() {
			t.Fatalf("%s: .git is not a gitfile: %v", wt, err)
		}
		if got, errOut, code := avalIn(t, dir, "explain", "--digest"); code != 0 || got != want {
			t.Errorf("%s: exit %d, fingerprint %q, want %q\n%s", wt, code, got, want, errOut)
		}
	}
}
