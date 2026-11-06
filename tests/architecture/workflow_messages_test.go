package architecture

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Workflow steps that assert on aval output: `jq ... contains("...")` and
// `aval ... | grep [flags] "..."`. Push-only jobs do not run on PRs, so a
// reworded message would otherwise only break main.
var (
	jqContains = regexp.MustCompile(`contains\("((?:[^"\\]|\\.)+)"\)`)
	avalGrep   = regexp.MustCompile(`aval\b[^|\n]*\|\s*grep(?:\s+-\w+)*\s+(?:"([^"]+)"|'([^']+)')`)
)

// assertedMessages returns every literal a workflow asserts aval prints.
func assertedMessages(workflow string) []string {
	var out []string
	for _, m := range jqContains.FindAllStringSubmatch(workflow, -1) {
		out = append(out, strings.ReplaceAll(m[1], `\"`, `"`))
	}
	for _, m := range avalGrep.FindAllStringSubmatch(workflow, -1) {
		out = append(out, m[1]+m[2])
	}
	return out
}

// missingMessages lists asserted literals that no Go source contains.
func missingMessages(workflow, source string) []string {
	var missing []string
	for _, msg := range assertedMessages(workflow) {
		if !strings.Contains(source, msg) {
			missing = append(missing, msg)
		}
	}
	return missing
}

// goSource concatenates the non-test Go files that produce aval's output.
func goSource(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, dir := range []string{"cmd", "internal", "pkg"} {
		err := filepath.WalkDir(filepath.Join("..", "..", dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			data, err := os.ReadFile(p)
			b.Write(data)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return b.String()
}

func TestWorkflowAssertedMessagesExist(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.y*ml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no workflows: %v", err)
	}
	src := goSource(t)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, msg := range missingMessages(string(data), src) {
			t.Errorf("%s asserts aval prints %q, but no non-test Go source contains it", filepath.Base(f), msg)
		}
	}
}

func TestWorkflowMessageCheckCatchesStaleMessages(t *testing.T) {
	wf := `
      - run: |
          jq -e '.reason | contains("is not in the trust policy")' r.json
          jq -e '.reason | contains("policy accepts no keys")' r.json
          aval verify -f a.yaml | grep -qF "verified result"
          aval verify -f a.yaml 2>&1 | grep -q 'gone message'
          grep -q "not aval output" some.log
`
	src := `fmt.Errorf("signer key id %q is not in the trust policy", h); "verified result"`
	if got, want := assertedMessages(wf), []string{"is not in the trust policy", "policy accepts no keys", "verified result", "gone message"}; !slices.Equal(got, want) {
		t.Fatalf("asserted = %q, want %q", got, want)
	}
	if got, want := missingMessages(wf, src), []string{"policy accepts no keys", "gone message"}; !slices.Equal(got, want) {
		t.Fatalf("missing = %q, want %q", got, want)
	}
}
