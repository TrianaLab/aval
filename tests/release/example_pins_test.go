package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scripts/pin-examples.sh, which release.yaml's pin-examples job runs on
// main (#261), moves every use of Aval's actions in an example workflow to
// the given commit with a "# <tag>" comment, keeps every other line byte
// for byte, leaves a step that already says "# <tag>", and changes nothing
// when the commit or the tag is malformed.
func TestPinExamplesScript(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	src, err := os.ReadFile(filepath.Join("..", "..", "examples", "github-monorepo", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	uses := strings.Count(string(src), "uses: TrianaLab/aval/integrations/github/")
	if uses == 0 {
		t.Fatal("the example uses no Aval action")
	}
	// One step already on the target tag stays as it is.
	kept := "      - uses: TrianaLab/aval/integrations/github/run@" + strings.Repeat("f", 40) + " # v9.8.7\n"
	f := filepath.Join(t.TempDir(), "ci.yml")
	before := append(append([]byte{}, src...), kept...)
	if err := os.WriteFile(f, before, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{sha[:12], "v9.8.7"}, {sha, "9.8.7"}, {sha, "v9.8.7|rm"}, {strings.ToUpper(sha), "v9.8.7"}} {
		if out, err := pinExamples(append(args, f)...); err == nil {
			t.Errorf("%v: succeeded: %s", args, out)
		}
		if got, _ := os.ReadFile(f); string(got) != string(before) {
			t.Errorf("%v: changed the file", args)
		}
	}

	if out, err := pinExamples(sha, "v9.8.7", f); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(got), "@"+sha+" # v9.8.7\n"); n != uses {
		t.Errorf("%d steps moved, want %d", n, uses)
	}
	if !strings.HasSuffix(string(got), kept) {
		t.Error("moved a step that already pins v9.8.7")
	}
	if changed := diffLines(before, got); changed != uses {
		t.Errorf("%d lines changed, want %d", changed, uses)
	}
}

func pinExamples(args ...string) ([]byte, error) {
	return exec.Command("bash", append([]string{filepath.Join("..", "..", "scripts", "pin-examples.sh")}, args...)...).CombinedOutput()
}
