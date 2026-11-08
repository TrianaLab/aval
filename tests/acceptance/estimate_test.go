package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The estimate-from-history story (#145): scripts/estimate-reuse.sh
// fingerprints past commits with `aval run --fingerprint-only` and counts a
// repeated fingerprint as reuse. A fingerprint error and an unacknowledged
// mutable reference are misses (invariant 3), and neither makes a later
// commit with the same fingerprint a hit, because nothing was published.
func TestEstimateReuseStory(t *testing.T) {
	dir := t.TempDir()
	env := append(os.Environ(), "AVAL="+aval, "DOCKER_CONFIG="+t.TempDir(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	commit := func(msg string, files map[string]string) {
		t.Helper()
		for name, body := range files {
			p := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		git("add", "-A")
		git("commit", "-q", "--allow-empty", "-m", msg)
	}
	ack := strings.Replace(contract, "cacheability:", `  allowMutable:
    - ref: flask
      file: src/requirements.txt
      reason: test
cacheability:`, 1)
	git("init", "-q", "-b", "main")
	commit("first", map[string]string{"t/aval.yaml": contract, "t/src/a": "1"})                      // miss
	commit("readme", map[string]string{"t/README": "x"})                                             // hit
	commit("change", map[string]string{"t/src/a": "2"})                                              // miss
	commit("mutable", map[string]string{"t/src/requirements.txt": "flask\n"})                        // miss, not published
	commit("readme 2", map[string]string{"t/README": "y"})                                           // miss again
	commit("allow", map[string]string{"t/aval.yaml": ack})                                           // miss
	commit("readme 3", map[string]string{"t/README": "z"})                                           // hit
	commit("broken", map[string]string{"t/aval.yaml": strings.Replace(ack, "src/**", "gone/**", 1)}) // miss

	script, err := filepath.Abs("../../scripts/estimate-reuse.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, "t/aval.yaml")
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var verdicts []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f := strings.Fields(line); len(f) > 1 && len(f[0]) == 40 {
			verdicts = append(verdicts, f[1])
		}
	}
	if got, want := strings.Join(verdicts, " "), "miss hit miss miss miss miss hit miss"; got != want {
		t.Errorf("verdicts %q, want %q\n%s", got, want, out)
	}
	if !strings.Contains(string(out), "8 commits, 2 would reuse a result (25%, an upper bound)") {
		t.Errorf("summary missing\n%s", out)
	}
	git("worktree", "prune")
	if wt, _ := exec.Command("git", "-C", dir, "worktree", "list").Output(); strings.Count(string(wt), "\n") != 1 {
		t.Errorf("worktree left behind:\n%s", wt)
	}
}
