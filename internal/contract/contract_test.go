package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const valid = `
apiVersion: aval.dev/v1alpha1
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
		"bad apiVersion": {"aval.dev/v1alpha1", "aval.dev/v9", "apiVersion"},
		"unknown field":  {"cacheability:", "cachability:", "cachability"},
		"bad class":      {"controlled-environment", "sometimes", "unknown class"},
		"escaping glob":  {`"compose.yaml"`, `"../secrets"`, "segments"},
		"absolute glob":  {`"compose.yaml"`, `"/etc/passwd"`, "relative"},
		"partial **":     {`"**/*.go"`, `"a**/*.go"`, "whole path segment"},
		"empty command":  {`["go", "test", "./..."]`, `[]`, "runner.command"},
		"bad name":       {"orders-integration", "Orders Integration", "metadata.name"},
		"bad env name":   {"[DB_IMAGE]", "[DB-IMAGE]", "environment.env"},
		"missing scope":  {`source: ["**/*.go", "compose.yaml"]`, `source: []`, "scope.source"},
		"bad kind":       {"kind: Test", "kind: Suite", "kind"},
		"bad tool key":   {"{go: ", "{Go: ", "environment.toolchain"},
		"no class":       {"cacheability: controlled-environment", "", "cacheability: required"},
		"empty glob":     {`["go.sum"]`, `[""]`, "empty glob"},
		"malformed glob": {`["go.sum"]`, `["go.[sum"]`, "syntax error"},
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
