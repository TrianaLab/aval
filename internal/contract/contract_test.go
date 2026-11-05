package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		"pre-freeze apiVersion": {"aval.trianalab.io/v1", "aval.dev/v1alpha1", "apiVersion"},
		"alpha apiVersion":      {"aval.trianalab.io/v1", "aval.trianalab.io/v1alpha1", "apiVersion"},
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
	if _, err := Load(filepath.Join(dir, "missing.yaml")); !os.IsNotExist(err) {
		t.Fatalf("missing file: got %v", err)
	}
}
