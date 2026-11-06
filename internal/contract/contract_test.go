package contract

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const valid = `
apiVersion: aval.trianalab.io/v1
kind: Test
metadata:
  name: orders-integration
runner:
  command: ["go", "test", "./..."]
scope:
  source: ["**/*.go", "compose.yaml"]
  dependencies: ["go.sum"]
environment:
  toolchain: {go: "1.25"}
  env: [DB_IMAGE]
cacheability: controlled-environment
`

func TestParseValid(t *testing.T) {
	c, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if c.Metadata.Name != "orders-integration" || c.Cacheability != ControlledEnvironment {
		t.Fatalf("unexpected contract: %+v", c)
	}
}

func TestParseInvalid(t *testing.T) {
	cases := map[string]struct{ from, to, wantErr string }{
		"bad apiVersion": {"aval.trianalab.io/v1", "aval.trianalab.io/v9", "apiVersion"},
		// No accept window for the pre-freeze identifiers (fail closed).
		"pre-freeze apiVersion": {"aval.trianalab.io/v1", "aval.dev/v1alpha1", "change the line to apiVersion: aval.trianalab.io/v1"},
		"alpha apiVersion":      {"aval.trianalab.io/v1", "aval.trianalab.io/v1alpha1", "apiVersion"},
		"empty apiVersion":      {"apiVersion: aval.trianalab.io/v1", "apiVersion: \"\"", "apiVersion"},
		"unknown field":         {"cacheability:", "cachability:", "cachability"},
		"bad class":             {"controlled-environment", "sometimes", "unknown class"},
		"escaping glob":         {`"compose.yaml"`, `"../secrets"`, "segments"},
		"absolute glob":         {`"compose.yaml"`, `"/etc/passwd"`, "relative"},
		"partial **":            {`"**/*.go"`, `"a**/*.go"`, "whole path segment"},
		"empty command":         {`["go", "test", "./..."]`, `[]`, "runner.command"},
		"bad name":              {"orders-integration", "Orders Integration", "metadata.name"},
		"bad env name":          {"[DB_IMAGE]", "[DB-IMAGE]", "environment.env"},
		"missing scope":         {`source: ["**/*.go", "compose.yaml"]`, `source: []`, "scope.source"},
		"bad kind":              {"kind: Test", "kind: Suite", "kind"},
		"bad tool key":          {"{go: ", "{Go: ", "environment.toolchain"},
		"no class":              {"cacheability: controlled-environment", "", "cacheability: required"},
		"empty glob":            {`["go.sum"]`, `[""]`, "empty glob"},
		"malformed glob":        {`["go.sum"]`, `["go.[sum"]`, "syntax error"},
		"backslash glob":        {`["go.sum"]`, `["a\\\\b"]`, "slash-separated"},
		"trailing slash":        {`["go.sum"]`, `["dir/"]`, "segments"},
		"dot segment":           {`["go.sum"]`, `["./go.sum"]`, "segments"},
		"empty segment":         {`["go.sum"]`, `["a//b"]`, "segments"},
		"inner ..":              {`["go.sum"]`, `["a/../../b"]`, "segments"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(strings.Replace(valid, tc.from, tc.to, 1)))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestParseRejectsMultipleDocuments(t *testing.T) {
	if _, err := Parse([]byte(valid + "\n---\nfoo: bar\n")); err == nil {
		t.Fatal("extra YAML document must be rejected")
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		glob, name string
		want       bool
	}{
		{"**/*.go", "main.go", true},
		{"**/*.go", "a/b/c.go", true},
		{"**/*.go", "a/b/c.txt", false},
		{"testdata/**", "testdata/x/y.json", true},
		{"testdata/**", "other/x.json", false},
		{"cmd/*/main.go", "cmd/aval/main.go", true},
		{"cmd/*/main.go", "cmd/a/b/main.go", false},
		{"a/**/z", "a/z", true},
		{"a/**/z", "a/b/c/z", true},
		{"**", "a/b/c", true},
		{"**/z", "z", true},
		{"a/**", "a", true}, // ** matches zero segments; exclude refuses this (MatchContents)
		{"a/**", "a/b", true},
		{"a/**/**", "a", true}, // consecutive ** still match zero segments
		{"**/*.go", ".hidden.go", true},
		{"**/*.go", ".gen/x.go", true},
		{"*.go", "a/x.go", false},
		{"A.go", "a.go", false},
		// Names that are not clean relative paths never match.
		{"**", "../secrets", false},
		{"*/x", "../x", false},
		{"**", "/etc/passwd", false},
		{"a/**", "a//b", false},
		{"**", "./a", false},
		{"**", "", false},
	}
	for _, tc := range cases {
		if got := Match(tc.glob, tc.name); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.glob, tc.name, got, tc.want)
		}
	}
}

func TestMatchContents(t *testing.T) {
	cases := []struct {
		glob, name string
		want       bool
	}{
		{"a/**", "a", false}, // a file named a is not under a/
		{"a/**", "a/b", true},
		{"**/build/**", "build/build", true},
		{"**/build/**", "x/build", false},
		{"a/**/z", "a/z", true}, // only a trailing ** needs a segment
		{"**", "a", true},
		{"a/**", "../a/b", false},
		{"**", "../a", false}, // the clean-path guard of Match applies
		{"*.go", "x.txt", false},
	}
	for _, tc := range cases {
		if got := MatchContents(tc.glob, tc.name); got != tc.want {
			t.Errorf("MatchContents(%q, %q) = %v, want %v", tc.glob, tc.name, got, tc.want)
		}
	}
}

// matchRef is the recursive matcher Match replaced: obviously correct and
// exponential in the number of `**`. Match must agree with it.
func matchRef(p, n []string, contents bool) bool {
	for len(p) > 0 {
		if p[0] == "**" {
			p = p[1:]
			if len(p) == 0 {
				return !contents || len(n) > 0
			}
			for i := 0; i <= len(n); i++ {
				if matchRef(p, n[i:], contents) {
					return true
				}
			}
			return false
		}
		if len(n) == 0 {
			return false
		}
		if ok, _ := path.Match(p[0], n[0]); !ok {
			return false
		}
		p, n = p[1:], n[1:]
	}
	return len(n) == 0
}

// refAgrees reports the first disagreement between Match, MatchContents and
// MayContain and the reference semantics, or "".
func refAgrees(glob, name string) string {
	g, n := strings.Split(glob, "/"), strings.Split(name, "/")
	ok := clean(n)
	if got, want := Match(glob, name), ok && matchRef(g, n, false); got != want {
		return fmt.Sprintf("Match(%q, %q) = %v, reference %v", glob, name, got, want)
	}
	if got, want := MatchContents(glob, name), ok && matchRef(g, n, true); got != want {
		return fmt.Sprintf("MatchContents(%q, %q) = %v, reference %v", glob, name, got, want)
	}
	// The walk prunes with MayContain: every directory above a match must
	// stay reachable, or a scoped file would silently drop out.
	if Match(glob, name) {
		for i := range len(n) - 1 {
			if !SplitGlob(glob).MayContain(n[:i+1]) {
				return fmt.Sprintf("MayContain(%q, %q) = false above match %q", glob, strings.Join(n[:i+1], "/"), name)
			}
		}
	}
	return ""
}

func TestMatchAgreesWithReference(t *testing.T) {
	globs := []string{
		"**", "**/**", "a/**", "a/**/**", "**/a", "**/**/a", "**/a/**", "a/**/b", "a/**/**/b",
		"**/a/**/b/**", "*/a", "a/*", "*", "a/*/b/**", "**/*.go", "?", "a/b/c", "[ab]/**/c",
	}
	names := []string{
		"a", "b", "c", "a/a", "a/b", "b/a", "a/b/c", "a/a/b", "a/x/b", "x/a/y/b/z", "a/b/a/b",
		"c/a/b", "x.go", "a/x.go", "a/b/c/d", "b/c", "a/c", "x/y/z/a",
	}
	for _, g := range globs {
		for _, n := range names {
			if msg := refAgrees(g, n); msg != "" {
				t.Error(msg)
			}
		}
	}
}

// Audit F12: consecutive `**` must not change what matches, and must not
// make matching exponential.
func TestRepeatedDoubleStar(t *testing.T) {
	for _, n := range []string{"x", "a/x", "a/b/x", "a/b", "x/a", "a/x/b"} {
		if Match("**/**/x", n) != Match("**/x", n) || MatchContents("**/**/x", n) != MatchContents("**/x", n) {
			t.Errorf("**/**/x and **/x disagree on %q", n)
		}
	}
	deep := strings.Repeat("a/", 40) + "b"
	for _, g := range []string{
		strings.Repeat("**/", 12) + "x",
		strings.Repeat("**/a/", 11) + "**/x",
	} {
		start := time.Now()
		if Match(g, deep) || MatchContents(g, deep) {
			t.Fatalf("%q must not match %q", g, deep)
		}
		if d := time.Since(start); d > 10*time.Millisecond {
			t.Errorf("matching a 12-** glob took %v, want < 10ms", d)
		}
	}
}

func TestMayContain(t *testing.T) {
	cases := []struct {
		glob, dir string
		want      bool
	}{
		{"src/**", "", true},
		{"src/**", "src", true},
		{"src/**", "src/a/b", true},
		{"src/**", "pgdata", false},
		{"go.mod", "", true},
		{"go.mod", "a", false},
		{"cmd/*/main.go", "cmd", true},
		{"cmd/*/main.go", "cmd/aval", true},
		{"cmd/*/main.go", "cmd/aval/x", false},
		{"cmd/*/main.go", "pkg", false},
		{"**/*.go", "anything/deep", true},
		{"a/**/b", "a/x/y", true},
		{"a/**/b", "b", false},
	}
	for _, tc := range cases {
		var dir []string
		if tc.dir != "" {
			dir = strings.Split(tc.dir, "/")
		}
		if got := SplitGlob(tc.glob).MayContain(dir); got != tc.want {
			t.Errorf("MayContain(%q, %q) = %v, want %v", tc.glob, tc.dir, got, tc.want)
		}
	}
}

func TestReusableByDefault(t *testing.T) {
	if NonDeterministic.ReusableByDefault() || ExternalDependencies.ReusableByDefault() {
		t.Fatal("non-deterministic and external-dependencies must not be reusable by default")
	}
	if !Hermetic.ReusableByDefault() {
		t.Fatal("hermetic must be reusable by default")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := Load(write("ok.yaml", valid)); err != nil {
		t.Fatal(err)
	}
	bad := write("bad.yaml", "kind: Test\n")
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("invalid contract error must name the file, got %v", err)
	}
	// A missing contract is still ErrNotExist, plus a next step.
	if _, err := Load(filepath.Join(dir, "missing.yaml")); !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "pass its path with -f") {
		t.Fatalf("missing file: got %v", err)
	}
	if _, err := Load(dir); err == nil || strings.Contains(err.Error(), "-f") {
		t.Fatalf("unreadable contract: got %v", err)
	}
}
