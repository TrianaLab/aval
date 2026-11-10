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

	"gopkg.in/yaml.v3"
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

func TestParseProbe(t *testing.T) {
	c, err := Parse([]byte(strings.Replace(valid, `toolchain: {go: "1.25"}`, "toolchain: {go: \"1.25\"}\n  probe: {go: [go, version]}", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Environment.Probe["go"]; len(got) != 2 || got[1] != "version" {
		t.Fatalf("probe %q", got)
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
		// The run stack differs between an outer and a nested run of the
		// same inputs: hashing it would split their fingerprints (#196).
		"run stack in env":            {"[DB_IMAGE]", "[DB_IMAGE, AVAL_RUN_STACK]", "environment.env[1]: AVAL_RUN_STACK is set by aval run and is not an input"},
		"run stack in env, any case":  {"[DB_IMAGE]", "[aval_run_stack]", "environment.env[0]: aval_run_stack is set by aval run"},
		"missing scope":               {`source: ["**/*.go", "compose.yaml"]`, `source: []`, "scope.source"},
		"bad kind":                    {"kind: Test", "kind: Suite", "kind"},
		"bad tool key":                {"{go: ", "{Go: ", "environment.toolchain"},
		"probe without toolchain key": {`toolchain: {go: "1.25"}`, "toolchain: {go: \"1.25\"}\n  probe: {python: [python, --version]}", "environment.probe.python: not a key of environment.toolchain"},
		"probe empty toolchain value": {`toolchain: {go: "1.25"}`, "toolchain: {go: \" \"}\n  probe: {go: [go, version]}", "environment.probe.go: environment.toolchain.go must not be empty"},
		"probe empty argv":            {`toolchain: {go: "1.25"}`, "toolchain: {go: \"1.25\"}\n  probe: {go: []}", "environment.probe.go: must be a non-empty argv list"},
		"probe blank command":         {`toolchain: {go: "1.25"}`, "toolchain: {go: \"1.25\"}\n  probe: {go: [\" \"]}", "environment.probe.go: must be a non-empty argv list"},
		"probe not a list":            {`toolchain: {go: "1.25"}`, "toolchain: {go: \"1.25\"}\n  probe: {go: go version}", "line 13: environment.probe.go: got a string `go version`, want a list of strings"},
		"no class":                    {"cacheability: controlled-environment", "", "cacheability: required"},
		"empty glob":                  {`["go.sum"]`, `[""]`, "empty glob"},
		"malformed glob":              {`["go.sum"]`, `["go.[sum"]`, "syntax error"},
		"backslash glob":              {`["go.sum"]`, `["a\\\\b"]`, "slash-separated"},
		"trailing slash":              {`["go.sum"]`, `["dir/"]`, "segments"},
		"dot segment":                 {`["go.sum"]`, `["./go.sum"]`, "segments"},
		"empty segment":               {`["go.sum"]`, `["a//b"]`, "segments"},
		"inner ..":                    {`["go.sum"]`, `["a/../../b"]`, "segments"},
		"allowMutable without reason": {`dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  allowMutable: [{ref: "postgres:16"}]`, "scope.allowMutable[0]: ref and reason are both required"},
		"allowMutable blank ref": {`dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  allowMutable: [{ref: " ", reason: "x"}]`, "scope.allowMutable[0]"},
		"allowMutable unknown key": {`dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  allowMutable: [{ref: a, reason: b, path: c}]`, "path"},
		"allowMutable file outside": {`dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  allowMutable: [{file: ../x/kustomization.yaml, ref: a, reason: b}]`, "scope.allowMutable[0]: file"},
		"allowMutable file absolute": {`dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  allowMutable: [{file: /compose.yaml, ref: a, reason: b}]`, "scope.allowMutable[0]: file"},
		"allowMutable file dot": {`dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  allowMutable: [{file: ./compose.yaml, ref: a, reason: b}]`, "scope.allowMutable[0]: file"},
		"allowMutable file and files": {`dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  allowMutable: [{file: compose.yaml, files: ["*.yaml"], ref: a, reason: b}]`, "scope.allowMutable[0]: file and files exclude each other"},
		"allowMutable files without ref": {`dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  allowMutable: [{files: ["*.yaml"], reason: b}]`, "scope.allowMutable[0]: ref and reason are both required"},
		"allowMutable files empty reason": {`dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  allowMutable: [{files: ["*.yaml"], ref: a, reason: ""}]`, "scope.allowMutable[0]: ref and reason are both required"},
		"allowMutable files outside": {`dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  allowMutable: [{files: ["deploy/*.yaml", "../x/*.yaml"], ref: a, reason: b}]`, "scope.allowMutable[0].files[1]: \"../x/*.yaml\": empty, '.' or '..' segments"},
		"allowMutable files not a list": {`dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  allowMutable: [{files: "*.yaml", ref: a, reason: b}]`, "files"},
		"resolve unknown kind": {`dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  resolve: [{docker: postgres:16}]`, `scope.resolve[0]: unknown kind "docker"`},
		"resolve two keys": {`dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  resolve: [{image: a, npm: b}]`, "one `kind: reference` pair"},
		"resolve not a map": {`dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  resolve: [postgres:16]`, "scope.resolve[0]: got a string"},
		"resolve space": {`dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  resolve: [{npm: "react@>=1 <2"}]`, "scope.resolve[0]: npm: the reference must be non-empty, without spaces"},
		"resolve empty": {`dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  resolve: [{url: ""}]`, "scope.resolve[0]: url: the reference must be non-empty"},
		"resolve password": {`dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  resolve: [{url: "https://u:s3cret@example.com/x"}]`, "credentials in the reference are not allowed"},
		"resolve token user": {`dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  resolve: [{git: "https://ghp_tok@github.com/o/r@main"}]`, "credentials in the reference are not allowed"},
		"resolve token in a URL that does not parse": {`dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  resolve: [{git: "https://ghp_tok@[::1/o/r@main"}]`, "credentials in the reference are not allowed"},
		"resolve password in a URL that does not parse": {`dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  resolve: [{url: "ssh://u:s3cret@exa%zz.com/x"}]`, "credentials in the reference are not allowed"},
		"contracts empty path":   {`dependencies: ["go.sum"]`, "dependencies: [\"go.sum\"]\n  contracts: [\"\"]", "scope.contracts[0]: \"\": must be a clean relative path"},
		"contracts absolute":     {`dependencies: ["go.sum"]`, "dependencies: [\"go.sum\"]\n  contracts: [/repo/aval.yaml]", "clean relative path"},
		"contracts inner ..":     {`dependencies: ["go.sum"]`, "dependencies: [\"go.sum\"]\n  contracts: [a/../b/aval.yaml]", "clean relative path"},
		"contracts dot segment":  {`dependencies: ["go.sum"]`, "dependencies: [\"go.sum\"]\n  contracts: [./b/aval.yaml]", "clean relative path"},
		"contracts backslash":    {`dependencies: ["go.sum"]`, "dependencies: [\"go.sum\"]\n  contracts: ['..\\b\\aval.yaml']", "slash-separated"},
		"contracts space":        {`dependencies: ["go.sum"]`, "dependencies: [\"go.sum\"]\n  contracts: [\"b c/aval.yaml\"]", "without spaces"},
		"contracts not yaml":     {`dependencies: ["go.sum"]`, "dependencies: [\"go.sum\"]\n  contracts: [../backend]", "must name a contract file"},
		"contracts listed twice": {`dependencies: ["go.sum"]`, "dependencies: [\"go.sum\"]\n  contracts: [../b/aval.yaml, ../b/aval.yaml]", "scope.contracts[1]: \"../b/aval.yaml\" is listed twice"},
		"contracts not a list":   {`dependencies: ["go.sum"]`, "dependencies: [\"go.sum\"]\n  contracts: ../b/aval.yaml", "scope.contracts: got a string `../b/av...`, want a list of strings"},
		"outputs escaping":       {"cacheability:", "outputs: [../coverage.xml]\ncacheability:", "outputs[0]"},
		"outputs absolute":       {"cacheability:", "outputs: [/tmp/junit.xml]\ncacheability:", "outputs[0]"},
		"outputs empty":          {"cacheability:", "outputs: [\"\"]\ncacheability:", "outputs[0]: empty glob"},
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

// TestParseAllowMutable: scope.allowMutable is optional (contracts without
// it still parse) and keeps each acknowledgement as written.
func TestParseAllowMutable(t *testing.T) {
	c, err := Parse([]byte(strings.Replace(valid, `dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  allowMutable:
    - ref: postgres:16
      reason: the image is rebuilt and scanned nightly
    - file: deploy/kustomization.yaml
      ref: resources[0]
      reason: vendored upstream
    - files: ["charts/**/values.yaml", "a[1].yaml"]
      ref: ghcr.io/stefanprodan/podinfo:6.5.0
      reason: public demo image`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	want := []AllowMutable{{Ref: "postgres:16", Reason: "the image is rebuilt and scanned nightly"}, {File: "deploy/kustomization.yaml", Ref: "resources[0]", Reason: "vendored upstream"},
		{Files: []string{"charts/**/values.yaml", "a[1].yaml"}, Ref: "ghcr.io/stefanprodan/podinfo:6.5.0", Reason: "public demo image"}}
	if fmt.Sprint(c.Scope.AllowMutable) != fmt.Sprint(want) {
		t.Fatalf("allowMutable = %+v", c.Scope.AllowMutable)
	}
	if c, _ := Parse([]byte(valid)); c.Scope.AllowMutable != nil {
		t.Fatalf("omitted allowMutable = %+v, want nil", c.Scope.AllowMutable)
	}
}

// TestParseOutputs: outputs is optional and keeps each glob as written.
func TestParseOutputs(t *testing.T) {
	c, err := Parse([]byte(strings.Replace(valid, "cacheability:", "outputs: [coverage.xml, reports/*.xml]\ncacheability:", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(c.Outputs) != "[coverage.xml reports/*.xml]" {
		t.Fatalf("outputs = %v", c.Outputs)
	}
	if c, _ := Parse([]byte(valid)); c.Outputs != nil {
		t.Fatalf("omitted outputs = %v, want nil", c.Outputs)
	}
}

// TestParseContracts: scope.contracts is optional and keeps each path as
// written, ".." and .yml included.
func TestParseContracts(t *testing.T) {
	c, err := Parse([]byte(strings.Replace(valid, `dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  contracts: [../backend/aval.yaml, ../../shared/aval.int.yml, lib/aval.yaml]`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if want := "[../backend/aval.yaml ../../shared/aval.int.yml lib/aval.yaml]"; fmt.Sprint(c.Scope.Contracts) != want {
		t.Fatalf("contracts = %v, want %s", c.Scope.Contracts, want)
	}
	if c, _ := Parse([]byte(valid)); c.Scope.Contracts != nil {
		t.Fatalf("omitted contracts = %v, want nil", c.Scope.Contracts)
	}
}

// TestParseRoot: scope.root is optional and accepts only ".." segments; a
// path that goes down, ".", absolute, backslashes and mixed paths are
// rejected (exit 2 in the CLI).
func TestParseRoot(t *testing.T) {
	with := func(root string) string {
		return strings.Replace(valid, `dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  root: `+root, 1)
	}
	for _, root := range []string{"..", "../..", "../../.."} {
		c, err := Parse([]byte(with(root)))
		if err != nil || c.Scope.Root != root {
			t.Fatalf("root %q: %v, %+v", root, err, c)
		}
	}
	if c, _ := Parse([]byte(valid)); c.Scope.Root != "" {
		t.Fatalf("omitted root = %q, want empty", c.Scope.Root)
	}
	for _, root := range []string{`"."`, "x", "../x", "x/..", "../../x", "/..", `"..\\.."`, "../", "./..", "...", "..//..", `"../..\n"`, "'.. '", `""`} {
		_, err := Parse([]byte(with(root)))
		if root == `""` {
			if err != nil {
				t.Fatalf("empty root (omitted): %v", err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "scope.root:") {
			t.Fatalf("root %s: want a scope.root error, got %v", root, err)
		}
	}
}

// TestParseResolve: scope.resolve is optional and keeps each `kind: ref`
// entry; a user in a URL is fine, a password is not echoed.
func TestParseResolve(t *testing.T) {
	c, err := Parse([]byte(strings.Replace(valid, `dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  resolve:
    - image: postgres:16
    - npm: react@^18
    - git: ssh://git@github.com/org/repo@main`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	want := []Reference{{"image", "postgres:16"}, {"npm", "react@^18"}, {"git", "ssh://git@github.com/org/repo@main"}}
	if fmt.Sprint(c.Scope.Resolve) != fmt.Sprint(want) {
		t.Fatalf("resolve = %+v", c.Scope.Resolve)
	}
	if c, _ := Parse([]byte(valid)); c.Scope.Resolve != nil {
		t.Fatalf("omitted resolve = %+v, want nil", c.Scope.Resolve)
	}
	_, err = Parse([]byte(strings.Replace(valid, `dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  resolve: [{url: "https://u:s3cret@example.com/x"}]`, 1)))
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("a password must be rejected without echoing it: %v", err)
	}
	// A token in a scheme-less user name or in a query parameter is
	// published with the manifest: rejected, never echoed.
	for _, ref := range []string{
		"//s3cret@example.com/f", "https://h/f?token=s3cret", "https://h/f?a=1&Private_Token=s3cret", "https://h/f?X-Amz-Signature=s3cret",
		"https://h/f?x-goog-signature=s3cret", "https://h/f?sig=s3cret", "https://h/f?a=1;signature=s3cret", "https://h/f?%74oken=s3cret",
		"https://h/f?access_token=s3cret", "https://h/f?KEY=s3cret", "https://h/f?apikey=s3cret", "https://h/f?api_key=s3cret",
		"https://h/f?auth=s3cret", "https://h/f?password=s3cret", "https://h/f?secret=s3cret", "https://h/f?credential=s3cret",
		"https://h/f?token", "https://h/r?private_token=s3cret@main",
		"https://h/f?X-Amz-Security-Token=s3cret", "https://h/f?api-key=s3cret", "https://h/f?client_secret=s3cret", "https://h/f?token[]=s3cret",
		"https://h/f?security_token=s3cret", "https://h/f?Session=s3cret", "https://h/f?sessionid=s3cret", "https://h/f?Client-Secret=s3cret",
	} {
		_, err := Parse([]byte(strings.Replace(valid, `dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  resolve: [{url: "`+ref+`"}]`, 1)))
		if err == nil || !strings.Contains(err.Error(), "credentials in the reference are not allowed") || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: want a credentials error that does not echo the value, got %v", ref, err)
		}
	}
	for _, ref := range []string{"https://h/f?v=1&format=tar.gz", "https://h/f?%zz=1", "https://h/f?tokens=1"} {
		if _, err := Parse([]byte(strings.Replace(valid, `dependencies: ["go.sum"]`, `dependencies: ["go.sum"]
  resolve: [{url: "`+ref+`"}]`, 1))); err != nil {
			t.Errorf("%s: %v", ref, err)
		}
	}
}

// yaml.v3 problems read in YAML terms, one joined error each: the field's
// path instead of a Go type, the closest field name for an unknown one, and
// the user's text kept for the caller to escape. Unknown fields still fail
// (a typo must never shrink the scope).
func TestParseYAMLErrors(t *testing.T) {
	doc := strings.NewReplacer("  source: [\"**/*.go\", \"compose.yaml\"]", "  sources: [\"**/*.go\"]\n  \"src\\e\": [x]",
		`command: ["go", "test", "./..."]`, "command: go test ./...",
		"cacheability: controlled-environment", "cacheability: controlled-environment\nmetadata2: x\nzzzzzzzz: 1").Replace(valid)
	_, err := Parse([]byte(doc))
	var j interface{ Unwrap() []error }
	if !errors.As(err, &j) {
		t.Fatalf("want joined problems, got %v", err)
	}
	var got []string
	for _, e := range j.Unwrap() {
		got = append(got, e.Error())
	}
	lines := []string{
		"line 7: runner.command: got a string `go test...`, want a list of strings",
		"line 9: scope.sources: unknown field; did you mean source?",
		"line 10: scope.src\x1b: unknown field; known fields: source, exclude, dependencies, allowMutable, resolve, contracts, root", // the caller escapes it
		"line 16: metadata2: unknown field; did you mean metadata?",
		"line 17: zzzzzzzz: unknown field; known fields: apiVersion, kind, metadata, runner, scope, environment, cacheability, outputs",
	}
	if strings.Join(got, "\n") != strings.Join(lines, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(lines, "\n"))
	}
	if errors.As(err, new(*yaml.TypeError)) {
		t.Fatal("yaml types must not leak")
	}
	for msg, want := range map[string]string{
		"line 3: field x not found in type other.T":       "line 3: unknown field x; check the spelling",
		"line 3: cannot unmarshal !!binary into chan int": "line 3: got !!binary, want chan int",
		"line 3: something else":                          "line 3: something else",
	} {
		if got := yamlPath(msg, nil); got != want {
			t.Errorf("%q: got %q, want %q", msg, got, want)
		}
	}
	for goType, w := range map[string]string{"[]contract.AllowMutable": "a list of mappings", "contract.Scope": "a mapping", "contract.Cacheability": "a string", "chan int": "chan int"} {
		if got := want(goType); got != w {
			t.Errorf("want(%s) = %q, want %q", goType, got, w)
		}
	}
	for tag, w := range map[string]string{"!!bool": "a boolean", "!!map": "a mapping", "!!float": "a number", "!!null": "nothing", "!!x": "!!x"} {
		if got := kindOf(tag); got != w {
			t.Errorf("kindOf(%s) = %q", tag, got)
		}
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
		// Linear matching takes microseconds; exponential backtracking over
		// 40 levels and 12 ** would take hours. 200ms leaves room for -race
		// on a loaded machine and still catches the exponential case.
		if d := time.Since(start); d > 200*time.Millisecond {
			t.Errorf("matching a 12-** glob took %v, want < 200ms", d)
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
	_, err := Load(bad)
	var fe *FileError
	if !errors.As(err, &fe) || fe.File != bad || err.Error() != bad+": "+errors.Unwrap(err).Error() {
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

// TestPackageNames: a package name is accepted only when it matches its
// ecosystem's naming rules, so a path, a URL, a local file or another source
// never passes for a registry name.
func TestPackageNames(t *testing.T) {
	ok := map[string][]string{
		"npm":   {"left-pad", "left-pad@1.3.0", "@types/node", "@types/node@^20", "@a-b/c.d_e~f@latest", "lodash@>=4", strings.Repeat("a", 214), "JSONStream@1.3.5", "Base64", "@types/Node~x"},
		"pypi":  {"requests", "requests==2.32.3", "Requests[security,socks]>=2", "zope.interface~=6.0", "a"},
		"go":    {"example.com/m@v1.0.0", "github.com/Acme/m/v2@main", "golang.org/x/mod"},
		"crate": {"serde@=1.0.0", "serde_json", "a-b@^1"},
		"gem":   {"rails@7.1.3", "net-http", "A.b_c"},
		"maven": {"org.junit:junit-bom:5.9.3", "com.example_x:a-b.c:[1,2)"},
	}
	bad := map[string][]string{
		"npm": {"left-pad/", "@types/node/", "@types/node/@1.0.0", "./x", "../x", "/x", "C:x", "x.tgz", "x.TAR.GZ", "@Types/node",
			".x", "_x", "a/b", "@scope", `a\b`, strings.Repeat("a", 215), "lib@./x", "lib@x.tgz", "lib@github:o/r", "lib@file:../x", "lib@npm:evil", "lib@~/x"},
		"pypi": {"./x", "../x", "/x", "dir/x", `dir\x`, "C:x", "x.whl", "x-1.0.tar.gz", "X.WHL==1.0", "x.tgz", "-x", "x-", "x[", "x[a]b", "x@https://h/x", "x==1.0/../y",
			"demo.tar.xz", "demo.tar.xz[x]", "d.tar.bz2", "d.TBZ", "d.txz", "d.tlz", "d.tar.lz", "d.tar.lzma==1", "d.tar[x]"},
		"go":    {"./x", "../x", "/x", "C:x", "UPPER.com/m", "Example..com/m@latest", "noperiod/m"},
		"crate": {"./x", "../x", "/x", "C:x", "x.tgz", "1abc", "_x", strings.Repeat("a", 65), "x@./y", "x@git:y"},
		"gem":   {"./x", "../x", "/x", "C:x", "x.gem", "-x", ".x", "x@/y", "x@1#y"},
		"maven": {"../x:a:1", "a..b:x:1", "g:./x:1", "g:a/b:1", "/g:a:1", "C\\x:a:1", "g:.a:1"},
	}
	for kind, refs := range ok {
		for _, ref := range refs {
			if err := (Reference{kind, ref}).validate(); err != nil {
				t.Errorf("%s %s: %v", kind, ref, err)
			}
		}
	}
	for kind, refs := range bad {
		for _, ref := range refs {
			err := (Reference{kind, ref}).validate()
			if err == nil || !strings.Contains(err.Error(), "not a valid "+kind+" package name") && !strings.Contains(err.Error(), "is not a registry version, range or tag") {
				t.Errorf("%s %s: want a package name error, got %v", kind, ref, err)
			}
		}
	}
	for _, ref := range []string{"g:a", "g::1", "g:a:1:x"} {
		if err := (Reference{"maven", ref}).validate(); err == nil || !strings.Contains(err.Error(), "want group:artifact:version") {
			t.Errorf("maven %s: %v", ref, err)
		}
	}
	// A name with spaces fails before the name check.
	if err := (Reference{"npm", "left pad"}).validate(); err == nil {
		t.Error("npm: a name with a space must fail")
	}
	// Not packages: no name rules.
	for _, r := range []Reference{{"image", "postgres:16"}, {"git", "https://h/r@main"}, {"url", "https://h/x.tgz"}} {
		if err := r.validate(); err != nil {
			t.Errorf("%v: %v", r, err)
		}
	}
}
