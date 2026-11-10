package acceptance

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// nested writes a contract named name in dir whose runner.command runs
// `aval run` on target (relative to dir), or `true` when target is empty.
func nested(name, target string) string {
	argv := []string{"true"}
	if target != "" {
		argv = []string{aval, "run", "-f", target}
	}
	q, _ := json.Marshal(argv)
	return "apiVersion: aval.trianalab.io/v1\nkind: Test\nmetadata: {name: " + name + "}\nrunner: {command: " + string(q) +
		"}\nscope: {source: [\"src/*\"]}\ncacheability: hermetic\n"
}

// A runner.command that starts `aval run` on its own contract, directly or
// through another contract, is refused at the repeat (exit 2) and the outer
// run records a failed test (exit 3) instead of looping (#196). A nested run
// of a different contract works.
func TestNestedRunStory(t *testing.T) {
	refused := "is already running in an outer aval run"
	cases := map[string]struct {
		files map[string]string
		code  int
		want  string
	}{
		"self": {map[string]string{"a/src/x": "x", "a/aval.yaml": nested("a", "aval.yaml")}, 3, "aval.yaml"},
		"cycle": {map[string]string{
			"a/src/x": "x", "a/aval.yaml": nested("a", "../b/aval.yaml"),
			"b/src/x": "x", "b/aval.yaml": nested("b", "../a/aval.yaml"),
		}, 3, "../a/aval.yaml"},
		"other contract": {map[string]string{
			"a/src/x": "x", "a/aval.yaml": nested("a", "../b/aval.yaml"),
			"b/src/x": "x", "b/aval.yaml": nested("b", ""),
		}, 0, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := writeProject(t, tc.files)
			out, stderr, code := avalEnv(t, "run", "-f", filepath.Join(dir, "a", "aval.yaml"))
			if code != tc.code || strings.Count(out+stderr, refused) != min(tc.code, 1) {
				t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tc.code, out, stderr)
			}
			if tc.want != "" && !strings.Contains(stderr, "aval: "+tc.want+" "+refused) {
				t.Fatalf("refusal does not name %s:\n%s", tc.want, stderr)
			}
		})
	}
}
