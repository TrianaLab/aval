package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// testSetProject writes a git working tree with a TestSet of three tests
// (unit, deps whose dependency is missing, mut whose allowMutable glob
// matches nothing) and a kind: Test, and makes it the working directory.
func testSetProject(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for name, data := range map[string]string{
		".git/HEAD": "ref: refs/heads/main\n",
		"in.txt":    "in\n",
		"aval.yaml": `apiVersion: aval.trianalab.io/v1
kind: TestSet
defaults: {scope: {source: [in.txt]}, cacheability: hermetic}
tests:
- {metadata: {name: unit}, runner: {command: ["true"]}}
- {metadata: {name: deps}, runner: {command: ["true"]}, scope: {dependencies: [missing.lock]}}
- {metadata: {name: mut}, runner: {command: ["true"]}, scope: {allowMutable: [{ref: x, reason: y, files: ["nope/*"]}]}}
`,
		"ok.yaml": `apiVersion: aval.trianalab.io/v1
kind: TestSet
defaults: {scope: {source: [in.txt]}, cacheability: hermetic}
tests:
- {metadata: {name: a}, runner: {command: ["true"]}}
- {metadata: {name: b}, runner: {command: ["true"]}}
`,
		"test.yaml": "apiVersion: aval.trianalab.io/v1\nkind: Test\nmetadata: {name: unit}\nrunner: {command: [\"true\"]}\nscope: {source: [in.txt]}\ncacheability: hermetic\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
}

// testCommands are the commands that take --test, with the flags they need
// to reach loading the contract.
func testCommands(t *testing.T) [][]string {
	key, _ := devKey(t, t.TempDir(), "k")
	return [][]string{{"run", "--no-reuse"}, {"explain"}, {"verify", "--registry", "localhost:1/x"}, {"push", "--registry", "localhost:1/x", "--key", key}}
}

// readResult reads the result run recorded at p.
func readResult(t *testing.T, p string) *evidence.Predicate {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	r, err := evidence.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Regression (#207): without --test a kind: Test runs as before, with its
// result in .aval/result.json and no report or run record; --test with its
// name moves the outputs to .aval/<name>/; any other name exits 2.
func TestRunKindTestWithoutTestFlag(t *testing.T) {
	testSetProject(t)
	if code, _, stderr := runCLI("run", "-f", "test.yaml", "--no-reuse"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if r := readResult(t, ".aval/result.json"); r.Test.ID != "unit" {
		t.Fatalf("result for %q", r.Test.ID)
	}
	if _, err := os.Stat(".aval/unit"); !os.IsNotExist(err) {
		t.Fatalf(".aval/unit exists without --test: %v", err)
	}
	if code, _, stderr := runCLI("run", "-f", "test.yaml", "--no-reuse", "-t", "unit"); code != ExitOK {
		t.Fatalf("--test unit: exit %d: %s", code, stderr)
	}
	for _, f := range []string{"result", "report", "record"} {
		if _, err := os.Stat(".aval/unit/" + f + ".json"); err != nil {
			t.Fatal(err)
		}
	}
	for _, cmd := range testCommands(t) {
		code, _, stderr := runCLI(append(cmd, "-f", "test.yaml", "--test", "orders")...)
		if code != ExitUsage || !strings.Contains(stderr, `test.yaml: no test named "orders"; its tests: unit`) {
			t.Errorf("%v: exit %d, %s", cmd, code, stderr)
		}
	}
}

// A TestSet of more than one test needs --test on every command (#207): no
// implicit run-all.
func TestTestSetNeedsTestFlag(t *testing.T) {
	testSetProject(t)
	for _, cmd := range testCommands(t) {
		code, _, stderr := runCLI(append(cmd, "-f", "ok.yaml")...)
		if code != ExitUsage || !strings.Contains(stderr, "the TestSet holds 2 tests (a, b); select one with --test") {
			t.Errorf("%v: exit %d, %s", cmd, code, stderr)
		}
	}
	if _, err := os.Stat(".aval"); !os.IsNotExist(err) {
		t.Fatalf("a refused run wrote outputs: %v", err)
	}
}

// mustRun runs the CLI and fails the test unless it exits 0.
func mustRun(t *testing.T, args ...string) {
	t.Helper()
	if code, _, stderr := runCLI(args...); code != ExitOK {
		t.Fatalf("%v: exit %d: %s", args, code, stderr)
	}
}

// exists reports whether something is at p.
func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// Two tests run one after the other write separate .aval/<name>/ and do not
// overwrite each other; a run removes the stale .aval/<name>/ of its own
// test (#207).
func TestRunTestFlagSeparatesOutputs(t *testing.T) {
	testSetProject(t)
	if err := os.MkdirAll(".aval/a", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".aval/a/stale.txt", []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "run", "-f", "ok.yaml", "--no-reuse", "--test", "a")
	mustRun(t, "run", "-f", "ok.yaml", "--no-reuse", "--test", "b")
	a, b := readResult(t, ".aval/a/result.json"), readResult(t, ".aval/b/result.json")
	if a.Test.ID != "a" || b.Test.ID != "b" || a.Execution.Fingerprint == b.Execution.Fingerprint {
		t.Fatalf("a %s %s, b %s %s", a.Test.ID, a.Execution.Fingerprint, b.Test.ID, b.Execution.Fingerprint)
	}
	var rep evidence.Report
	if raw, err := os.ReadFile(".aval/b/report.json"); err != nil || json.Unmarshal(raw, &rep) != nil || rep.TestID != "b" {
		t.Fatalf("report of b: %v %+v", err, rep)
	}
	if exists(".aval/a/stale.txt") {
		t.Fatal("stale file of a kept")
	}
}

// With --test an output flag that is set, even through its deprecated
// alias, still wins; --fingerprint-only writes and removes nothing (#207).
func TestRunTestFlagExplicitOutputs(t *testing.T) {
	testSetProject(t)
	mustRun(t, "run", "-f", "ok.yaml", "--no-reuse", "-t", "b")
	mustRun(t, "run", "-f", "ok.yaml", "--no-reuse", "-t", "a", "--result-file", "out/a.json")
	mustRun(t, "run", "-f", "ok.yaml", "--no-reuse", "-t", "a", "--result", "out/a2.json")
	mustRun(t, "run", "-f", "ok.yaml", "-t", "b", "--fingerprint-only")
	if readResult(t, "out/a.json").Test.ID != "a" || readResult(t, "out/a2.json").Test.ID != "a" || exists(".aval/a/result.json") || !exists(".aval/b/result.json") {
		t.Fatal("an explicit output flag did not win, or --fingerprint-only removed outputs")
	}
}

// push --test reads the result and report of that test from .aval/<name>/
// (#207): after a run of a, push --test b finds no result of b.
func TestPushTestFlagDefaults(t *testing.T) {
	testSetProject(t)
	key, _ := devKey(t, t.TempDir(), "k")
	if code, _, stderr := runCLI("run", "-f", "ok.yaml", "--no-reuse", "-t", "a"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	code, _, stderr := runCLI("push", "-f", "ok.yaml", "-t", "b", "--registry", "localhost:1/x", "--key", key)
	if code != ExitError || !strings.Contains(stderr, filepath.Join(".aval", "b", "report.json")) {
		t.Fatalf("push b: exit %d: %s", code, stderr)
	}
	// False hit: the result of a never publishes as b.
	code, _, stderr = runCLI("push", "-f", "ok.yaml", "-t", "b", "--registry", "localhost:1/x", "--key", key, "--report-file", "", "--result-file", ".aval/a/result.json")
	if code != ExitError || !strings.Contains(stderr, `recorded result is for test "a"`) {
		t.Fatalf("push b with the result of a: exit %d: %s", code, stderr)
	}
	// With --report-file set to nothing, the result path is the one of b.
	code, _, stderr = runCLI("push", "-f", "ok.yaml", "-t", "b", "--registry", "localhost:1/x", "--key", key, "--report-file", "")
	if code != ExitError || !strings.Contains(stderr, filepath.Join(".aval", "b", "result.json")) {
		t.Fatalf("push b without report: exit %d: %s", code, stderr)
	}
}

// explain --test selects a test; --expand prints it as a standalone kind:
// Test that loads to the same contract and fingerprint (#207).
func TestExplainTestFlag(t *testing.T) {
	testSetProject(t)
	_, want, _ := runCLI("explain", "-f", "test.yaml", "--digest")
	if code, got, stderr := runCLI("explain", "-f", "aval.yaml", "-t", "unit", "--digest"); code != ExitOK || got != want {
		t.Fatalf("exit %d, %q (stderr %q), want %q", code, got, stderr, want)
	}
	code, expanded, stderr := runCLI("explain", "-f", "ok.yaml", "-t", "b", "--expand")
	if code != ExitOK || !strings.HasPrefix(expanded, "apiVersion: aval.trianalab.io/v1\nkind: Test\nmetadata:\n  name: b\n") {
		t.Fatalf("exit %d, %q (stderr %q)", code, expanded, stderr)
	}
	if err := os.WriteFile("b.yaml", []byte(expanded), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := contract.Load("b.yaml", "")
	if err != nil || c.Metadata.Name != "b" {
		t.Fatalf("%v, %v", c, err)
	}
	_, a, _ := runCLI("explain", "-f", "b.yaml", "--digest")
	if _, b, _ := runCLI("explain", "-f", "ok.yaml", "-t", "b", "--digest"); a != b {
		t.Fatalf("expanded %s, set %s", a, b)
	}
	if code, _, stderr := runCLI("explain", "-f", "ok.yaml", "--expand"); code != ExitUsage || !strings.Contains(stderr, "select one with --test") {
		t.Fatalf("--expand without --test: exit %d, %s", code, stderr)
	}
}

// explain --all checks every test, prints one line per valid test, names
// every failing one and exits 2 when one is invalid; -o json is a golden.
// It runs nothing (#207).
func TestExplainAll(t *testing.T) {
	testSetProject(t)
	code, stdout, stderr := runCLI("explain", "--all")
	if code != ExitUsage || stdout != "unit sha256:d836e547a6ec34a756bead7f9e46c4ea32e2f5585eab021a30a347f6e0a3f422\n" ||
		stderr != "aval: deps: fingerprint: dependency glob \"missing.lock\" matched no files\n"+
			"aval: mut: aval.yaml: invalid contract: scope.allowMutable[0].files[0]: glob \"nope/*\" matches no file in scope\n" {
		t.Fatalf("exit %d\nstdout %q\nstderr %q", code, stdout, stderr)
	}
	code, stdout, _ = runCLI("explain", "--all", "-o", "json")
	if code != ExitUsage || exists(".aval") {
		t.Fatalf("json: exit %d, or it wrote outputs", code)
	}
	golden(t, "explain-all.golden.json", stdout)
}

// explain --all exits 1 when only fingerprints cannot be computed, 0 on a
// valid file of either kind, and lists every expansion problem of every
// test before any fingerprint (#207).
func TestExplainAllExitCodes(t *testing.T) {
	testSetProject(t)
	head := "apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults: {scope: {source: [in.txt]}, cacheability: hermetic}\ntests:\n"
	for name, data := range map[string]string{
		"deps.yaml": head + "- {metadata: {name: x}, runner: {command: [\"true\"]}, scope: {dependencies: [missing.lock]}}\n",
		"bad.yaml":  head + "- {metadata: {name: x}, use: [nope]}\n- {metadata: {name: y}, use: [gone]}\n",
	} {
		if err := os.WriteFile(name, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		file   string
		code   int
		stdout string
		stderr []string
	}{
		{"deps.yaml", ExitError, "", []string{"aval: x: fingerprint:"}},
		{"bad.yaml", ExitUsage, "", []string{`tests[0] (x): use: unknown fragment "nope"`, `tests[1] (y): use: unknown fragment "gone"`}},
		{"ok.yaml", ExitOK, "a sha256:", nil},
		{"test.yaml", ExitOK, "unit sha256:", nil},
	} {
		code, stdout, stderr := runCLI("explain", "--all", "-f", tc.file)
		bad := code != tc.code || !strings.HasPrefix(stdout, tc.stdout) || (tc.stdout == "") != (stdout == "")
		for _, m := range tc.stderr {
			bad = bad || !strings.Contains(stderr, m)
		}
		if bad {
			t.Errorf("%s: exit %d, %q, %s", tc.file, code, stdout, stderr)
		}
	}
}

func TestExplainFlagConflicts(t *testing.T) {
	testSetProject(t)
	for args, msg := range map[string]string{
		"--all -t unit":             "--all excludes --test, --expand and --digest",
		"--all --expand":            "--all excludes",
		"--all --digest":            "--all excludes",
		"-o json":                   "--output json needs --all",
		"-o yaml --all":             `--output must be text or json, got "yaml"`,
		"-t unit --expand --digest": "--expand and --digest exclude each other",
	} {
		if code, _, stderr := runCLI(append([]string{"explain"}, strings.Fields(args)...)...); code != ExitUsage || !strings.Contains(stderr, msg) {
			t.Errorf("%s: exit %d, %s", args, code, stderr)
		}
	}
}

// A stale .aval/<name>/ that cannot be removed stops the run before
// anything runs (exit 1).
func TestRunTestFlagStaleNotRemovable(t *testing.T) {
	testSetProject(t)
	if err := os.MkdirAll(".aval/a/x", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(".aval/a", 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(".aval/a", 0o755) })
	if code, stdout, stderr := runCLI("run", "-f", "ok.yaml", "--no-reuse", "-t", "a"); code != ExitError || stdout != "" || !strings.Contains(stderr, "removing the outputs of the last run") {
		t.Fatalf("exit %d, %q, %s", code, stdout, stderr)
	}
}

// A symlinked .aval is never followed: the run stops before it removes
// anything, so the target's <name>/ survives (exit 1).
func TestRunTestFlagSymlinkedOutputDir(t *testing.T) {
	testSetProject(t)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, ".aval"); err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := runCLI("run", "-f", "ok.yaml", "--no-reuse", "-t", "a"); code != ExitError || stdout != "" || !strings.Contains(stderr, ".aval is a symlink") {
		t.Fatalf("exit %d, %q, %s", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(outside, "a")); err != nil {
		t.Fatalf("target removed: %v", err)
	}
}
