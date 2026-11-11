package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// countRegistry fails the test's registry seams and counts every call.
func countRegistry(t *testing.T) *int {
	t.Helper()
	l, f, r := lookup, fetch, pushRecord
	t.Cleanup(func() { lookup, fetch, pushRecord = l, f, r })
	calls := 0
	lookup = func(context.Context, string, string) ([]registry.Descriptor, error) { calls++; return nil, nil }
	fetch = func(context.Context, registry.Descriptor) (*registry.Attestation, error) { calls++; return nil, nil }
	pushRecord = func(context.Context, string, evidence.RunRecord) (registry.Descriptor, error) {
		calls++
		return registry.Descriptor{}, nil
	}
	return &calls
}

// aval explain prints run --explain's manifest under a header with the
// digest last, explain --digest prints what --fingerprint-only prints, and
// neither touches the registry, even with $AVAL_REGISTRY set.
func TestExplainMatchesRunExplain(t *testing.T) {
	calls := countRegistry(t)
	t.Setenv(registryEnv, "registry.example/acme/r")
	t.Setenv(policyEnv, "p.yaml")
	c := writeContract(t, "hermetic")

	_, manifest, _ := runCLI("run", "-f", c, "--explain", "--fingerprint-only")
	_, digest, _ := runCLI("run", "-f", c, "--fingerprint-only")
	code, stdout, stderr := runCLI("explain", "-f", c)
	lines := strings.SplitAfter(manifest, "\n")
	want := "fingerprint manifest for unit (aval-fingerprint/v2)\n" + strings.Join(lines[:len(lines)-2], "") + "fingerprint " + digest
	if code != ExitOK || stdout != want || stderr != "" {
		t.Fatalf("explain: exit %d, stdout %q, stderr %q; want stdout %q", code, stdout, stderr, want)
	}
	code, stdout, stderr = runCLI("explain", "-f", c, "--digest")
	if code != ExitOK || stdout != digest || !strings.HasPrefix(digest, "sha256:") || stderr != "" {
		t.Fatalf("explain --digest: exit %d, stdout %q, stderr %q; want %q", code, stdout, stderr, digest)
	}
	if *calls != 0 {
		t.Fatalf("explain made %d registry calls", *calls)
	}
}

// A fingerprint error is exit 1 with aval: fingerprint:, as in run
// --explain; an invalid contract is a usage error; explain takes no
// registry flag.
func TestExplainErrors(t *testing.T) {
	noSource := writeContract(t, "hermetic")
	if err := os.Remove(filepath.Join(filepath.Dir(noSource), "in.txt")); err != nil {
		t.Fatal(err)
	}
	// An exact glob naming a .git file matches nothing: .git is never an
	// input (#199), and the glob fails like any other that matches no file.
	gitfile := writeContract(t, "hermetic")
	data, err := os.ReadFile(gitfile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitfile, []byte(strings.Replace(string(data), `["*.txt"]`, `["*.txt", "pkg/.git"]`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(filepath.Dir(gitfile), "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(gitfile), "pkg", ".git"), []byte("gitdir: ../.git/modules/pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args   []string
		code   int
		stderr string
	}{
		{[]string{"explain", "-f", noSource}, ExitError, "aval: fingerprint: "},
		{[]string{"explain", "-f", gitfile}, ExitError, `aval: fingerprint: source glob "pkg/.git" matched no files`},
		{[]string{"explain", "-f", noSource, "--digest"}, ExitError, "aval: fingerprint: "},
		{[]string{"run", "-f", noSource, "--explain"}, ExitError, "aval: fingerprint: "},
		{[]string{"explain", "-f", filepath.Join(t.TempDir(), "aval.yaml")}, ExitUsage, "aval: "},
		{[]string{"explain", "--registry", "r.example/x"}, ExitUsage, "unknown flag: --registry"},
		{[]string{"explain", "x"}, ExitUsage, "unknown command"},
	} {
		code, stdout, stderr := runCLI(tc.args...)
		if code != tc.code || stdout != "" || !strings.HasPrefix(stderr, tc.stderr) && !strings.Contains(stderr, tc.stderr) {
			t.Errorf("aval %q: exit %d, stdout %q, stderr %q; want exit %d, stderr ~%q", tc.args, code, stdout, stderr, tc.code, tc.stderr)
		}
	}
}

// A mutable reference is listed on stderr, so stdout stays the manifest.
func TestExplainListsFindingsOnStderr(t *testing.T) {
	c := mutableContract(t, "hermetic", "", "true")
	_, want, _ := runCLI("run", "-f", c, "--fingerprint-only")
	code, stdout, stderr := runCLI("explain", "-f", c)
	if code != ExitOK || !strings.HasSuffix(stdout, "fingerprint "+want) || !strings.Contains(stderr, "mutable: ") || strings.Contains(stdout, "mutable: ") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// normalized reads the JSON file at p without the fields that differ
// between two runs (times and durations), then removes it.
func normalized(t *testing.T, p string) string {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		return "absent"
	}
	_ = os.Remove(p)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	for _, k := range []string{"startedAt", "finishedAt", "testDurationMs"} {
		delete(m, k)
	}
	out, _ := json.Marshal(m)
	return string(out)
}

// Every deprecated name does what its new name does: same stdout, same
// files, same exit code. Its deprecation notice is the only difference, on
// stderr.
func TestDeprecatedAliases(t *testing.T) {
	took := regexp.MustCompile(` in [0-9.]+(ns|µs|ms|s)\b`) // the summary and runner frame time the test
	fakeRecord(t, nil)
	fakeCandidates(t, fmt.Errorf("x: %w", evidence.ErrNotFound), nil)
	c := writeContract(t, "hermetic")
	dir := t.TempDir()
	res, rep, rec := filepath.Join(dir, "result.json"), filepath.Join(dir, "report.json"), filepath.Join(dir, "record.json")
	if code, _, stderr := runCLI("run", "-f", c, "--result-file", res, "--stats-file", rec); code != ExitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	recBody, _ := os.ReadFile(rec)
	_ = os.Remove(rec)
	reg := []string{"--registry", "registry.example/acme/r"}
	cases := []struct {
		name     string
		old, new []string // arguments before the shared ones
		args     []string
		notice   string
		restore  bool // write rec again before each run
	}{
		{"run --result", []string{"run", "--result", res}, []string{"run", "--result-file", res}, []string{"-f", c}, "Flag --result has been deprecated, use --result-file\n", false},
		{"run --report", []string{"run", "--report", rep}, []string{"run", "--report-file", rep}, []string{"-f", c, "--result-file", res}, "Flag --report has been deprecated, use --report-file\n", false},
		{"run --record-file", []string{"run", "--record-file", rec}, []string{"run", "--stats-file", rec}, []string{"-f", c, "--result-file", res}, "Flag --record-file has been deprecated, use --stats-file\n", false},
		{"run --record", []string{"run", "--record"}, []string{"run", "--stats"}, append([]string{"-f", c, "--result-file", res}, reg...), "Flag --record has been deprecated, use --stats\n", false},
		{"run --repository", []string{"run", "--repository", "acme/orders"}, []string{"run", "--source-repo", "acme/orders"}, []string{"-f", c, "--result-file", res, "--stats-file", rec}, "Flag --repository has been deprecated, use --source-repo\n", false},
		{"run --record without registry", []string{"run", "--record"}, []string{"run", "--stats"}, []string{"-f", c}, "Flag --record has been deprecated, use --stats\n", false},
		{"push --result", []string{"push", "--result", filepath.Join(dir, "none.json")}, []string{"push", "--result-file", filepath.Join(dir, "none.json")}, append([]string{"-f", c, "--key", "k"}, reg...), "Flag --result has been deprecated, use --result-file\n", false},
		{"record", []string{"record", "--record-file", rec}, []string{"stats", "publish", rec}, reg, "Command \"record\" is deprecated, use 'aval stats publish FILE' instead\n", true},
		{"record --repository", []string{"record", "--record-file", rec, "--repository", "acme/orders"}, []string{"record", "--record-file", rec, "--source-repo", "acme/orders"}, reg, "Flag --repository has been deprecated, use --source-repo\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			type result struct {
				code                          int
				stdout, stderr, res, rep, rec string
			}
			exec := func(args []string) result {
				if tc.restore {
					if err := os.WriteFile(rec, recBody, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				code, stdout, stderr := runCLI(append(args, tc.args...)...)
				stdout, stderr = took.ReplaceAllString(stdout, " in D"), took.ReplaceAllString(stderr, " in D")
				r := result{code, stdout, stderr, normalized(t, res), normalized(t, rep), ""}
				if !tc.restore {
					r.rec = normalized(t, rec)
				}
				return r
			}
			o, n := exec(tc.old), exec(tc.new)
			if o.stderr != tc.notice+n.stderr || strings.Contains(o.stdout, "deprecated") {
				t.Fatalf("stderr %q, want %q", o.stderr, tc.notice+n.stderr)
			}
			if o.stderr = n.stderr; o != n {
				t.Fatalf("old %+v\nnew %+v", o, n)
			}
		})
	}
}

// A kind: TestSet of one test explains like the standalone kind: Test; a
// TestSet of many, an invalid one, or one that scope.contracts points at
// exits 2 (#206).
func TestExplainTestSet(t *testing.T) {
	dir := t.TempDir()
	test := "apiVersion: aval.trianalab.io/v1\nkind: Test\nmetadata: {name: unit}\nrunner: {command: [x]}\nscope: {source: [in.txt]}\ncacheability: hermetic\n"
	set := "apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults: {scope: {source: [in.txt]}, cacheability: hermetic}\ntests:\n- {metadata: {name: unit}, runner: {command: [x]}}\n"
	for name, data := range map[string]string{
		".git/HEAD": "ref: refs/heads/main\n", "in.txt": "in\n", "test.yaml": test, "one.yaml": set,
		"many.yaml": set + "- {metadata: {name: other}, runner: {command: [y]}}\n",
		"bad.yaml":  set + "- {metadata: {name: other}, runner: {command: [y]}, use: [nope]}\n",
		"ref.yaml":  strings.Replace(test, "source: [in.txt]", "source: [in.txt], contracts: [one.yaml]", 1),
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := func(name string) string { return filepath.Join(dir, name) }
	_, want, _ := runCLI("explain", "-f", f("test.yaml"), "--digest")
	if code, got, stderr := runCLI("explain", "-f", f("one.yaml"), "--digest"); code != ExitOK || got != want {
		t.Fatalf("one-test TestSet: exit %d, %q (stderr %q), want %q", code, got, stderr, want)
	}
	for file, msg := range map[string]string{
		"many.yaml": "the TestSet holds 2 tests (unit, other)",
		"bad.yaml":  `use: unknown fragment "nope"`,
		"ref.yaml":  "one.yaml is a kind: TestSet; scope.contracts takes a kind: Test",
	} {
		if code, _, stderr := runCLI("explain", "-f", f(file)); code != ExitUsage || !strings.Contains(stderr, msg) {
			t.Errorf("%s: exit %d, stderr %q; want exit 2 with %q", file, code, stderr, msg)
		}
	}
}

// explain names each scope.optionalSource glob that matched no file on
// stderr, so a typo there stays visible; the manifest is unchanged (#257).
func TestExplainUnmatchedOptional(t *testing.T) {
	c := writeContract(t, "hermetic")
	data, err := os.ReadFile(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c, []byte(strings.Replace(string(data), `source: ["*.txt"]`, "source: [\"*.txt\"]\n  optionalSource: [\"*.yaml\", \"**/*testdata*.json\"]", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCLI("explain", "-f", c)
	want := "optional: scope.optionalSource glob \"**/*testdata*.json\" matched no files yet\n"
	if code != ExitOK || stderr != want || !strings.Contains(stdout, " - aval.yaml\n") {
		t.Fatalf("explain: exit %d, stdout %q, stderr %q; want stderr %q", code, stdout, stderr, want)
	}
}

// A matrix substitutes {var} into toolchain values: each leg explains its
// own value (#255).
func TestExplainMatrixToolchain(t *testing.T) {
	dir := t.TempDir()
	set := `apiVersion: aval.trianalab.io/v1
kind: TestSet
defaults: {scope: {source: [a.txt]}, cacheability: hermetic}
tests:
  - metadata: {name: "unit-{os}"}
    runner: {command: [echo, "{os}"]}
    matrix: {os: [linux, darwin]}
    environment: {toolchain: {platform: "{os}"}}
`
	for name, data := range map[string]string{"a.txt": "a\n", "aval.yaml": set} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, goos := range []string{"linux", "darwin"} {
		code, out, stderr := runCLI("explain", "-f", filepath.Join(dir, "aval.yaml"), "--test", "unit-"+goos)
		if code != ExitOK || !strings.Contains(out, "toolchain platform="+goos+"\n") {
			t.Errorf("unit-%s: exit %d, stderr %q:\n%s", goos, code, stderr, out)
		}
	}
}
