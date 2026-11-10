package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/pkg/evidence"
)

const sha = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

// outputsContract writes a contract declaring outputs (a YAML list) whose
// runner.command is argv, and returns its path.
func outputsContract(t *testing.T, outputs string, argv ...string) string {
	t.Helper()
	if argv == nil {
		argv = []string{"true"}
	}
	p := contractWith(t, "hermetic", argv...)
	body, _ := os.ReadFile(p)
	if err := os.WriteFile(p, append(body, []byte("outputs: "+outputs+"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sized(t *testing.T, dir, rel string, size int64) {
	t.Helper()
	write(t, dir, rel, "")
	if err := os.Truncate(filepath.Join(dir, rel), size); err != nil {
		t.Fatal(err)
	}
}

func TestCollectOutputs(t *testing.T) {
	load := func(t *testing.T, outputs string, files map[string]string) (string, *contract.Contract) {
		p := outputsContract(t, outputs)
		for rel, body := range files {
			write(t, filepath.Dir(p), rel, body)
		}
		c, err := contract.Load(p)
		if err != nil {
			t.Fatal(err)
		}
		return p, c
	}
	file, c := load(t, `["**/*.xml"]`, map[string]string{"coverage.xml": "cov", "reports/junit.xml": "junit", ".git/x.xml": "git", ".aval/y.xml": "aval", "notes.md": "n"})
	got, err := collectOutputs(file, c)
	if err != nil || fmt.Sprint(got) != fmt.Sprint([]registry.Output{{Path: "coverage.xml", Data: []byte("cov")}, {Path: "reports/junit.xml", Data: []byte("junit")}}) {
		t.Fatalf("collectOutputs = %v %v", got, err)
	}

	cases := map[string]struct {
		outputs string
		setup   func(t *testing.T, dir string)
		want    string
	}{
		"missing":      {`[coverage.xml]`, nil, `outputs glob "coverage.xml" matched no files`},
		"an input":     {`["*.txt"]`, nil, "in.txt is an input of the test"},
		"the contract": {`["*.yaml"]`, nil, "aval.yaml is the contract"},
		// A gitfile (#199) is never published, like a .git directory.
		"gitfile":   {`[".git"]`, func(t *testing.T, dir string) { write(t, dir, ".git", "gitdir: /src/.git/worktrees/a\n") }, `outputs glob ".git" matched no files`},
		"backslash": {`["*.xml"]`, func(t *testing.T, dir string) { write(t, dir, `a\b.xml`, "x") }, "backslashes"},
		"symlink": {`[coverage.xml]`, func(t *testing.T, dir string) {
			if err := os.Symlink("in.txt", filepath.Join(dir, "coverage.xml")); err != nil {
				t.Fatal(err)
			}
		}, "not a regular file"},
		"too many files": {`["out/*"]`, func(t *testing.T, dir string) {
			for i := range registry.MaxOutputFiles + 1 {
				write(t, dir, fmt.Sprintf("out/%d", i), "x")
			}
		}, "more than 256 output files"},
		"file over the cap": {`[big.bin]`, func(t *testing.T, dir string) { sized(t, dir, "big.bin", registry.MaxOutputSize+1) }, "capped"},
		"total over the cap": {`["out/*"]`, func(t *testing.T, dir string) {
			for i := range registry.MaxOutputTotal/registry.MaxOutputSize + 1 {
				sized(t, dir, fmt.Sprintf("out/%d", i), registry.MaxOutputSize)
			}
		}, "capped"},
		"unreadable file": {`[coverage.xml]`, func(t *testing.T, dir string) {
			write(t, dir, "coverage.xml", "x")
			chmod(t, filepath.Join(dir, "coverage.xml"), 0)
		}, "permission denied"},
		"unreadable directory": {`["out/*"]`, func(t *testing.T, dir string) {
			write(t, dir, "out/a", "x")
			chmod(t, filepath.Join(dir, "out"), 0)
		}, "permission denied"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			file, c := load(t, tc.outputs, nil)
			if tc.setup != nil {
				tc.setup(t, filepath.Dir(file))
			}
			if got, err := collectOutputs(file, c); err == nil || !strings.Contains(err.Error(), tc.want) || got != nil {
				t.Fatalf("got %v %v, want error ~%q", got, err, tc.want)
			}
		})
	}
}

// chmod sets mode and restores it when the test ends, so TempDir cleanup
// works. Root reads anything, so the test is skipped as root.
func chmod(t *testing.T, p string, mode os.FileMode) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o755) })
}

// fakeOutputs replaces publishing and fetching outputs. It returns the files
// published and stores them for fetchOutputs; fetchErr replaces the fetch.
func fakeOutputs(t *testing.T, pushErr, fetchErr error) *[]registry.Output {
	t.Helper()
	p, f := pushOutputs, fetchOutputs
	t.Cleanup(func() { pushOutputs, fetchOutputs = p, f })
	var pushed []registry.Output
	pushOutputs = func(_ context.Context, _, _ string, files []registry.Output) (*evidence.Outputs, error) {
		pushed = files
		if pushErr != nil {
			return nil, pushErr
		}
		return outputsOf(files), nil
	}
	fetchOutputs = func(_ context.Context, _ string, o *evidence.Outputs) ([]registry.Output, error) {
		if fetchErr != nil {
			return nil, fetchErr
		}
		var out []registry.Output
		for _, f := range o.Files {
			out = append(out, registry.Output{Path: f.Path, Data: []byte("bytes of " + f.Path)})
		}
		return out, nil
	}
	return &pushed
}

func outputsOf(files []registry.Output) *evidence.Outputs {
	o := &evidence.Outputs{Manifest: sha}
	for _, f := range files {
		o.Files = append(o.Files, evidence.OutputFile{Path: f.Path, Digest: sha, Size: int64(len(f.Data))})
	}
	return o
}

func listed(paths ...string) *evidence.Outputs {
	var files []registry.Output
	for _, p := range paths {
		files = append(files, registry.Output{Path: p})
	}
	return outputsOf(files)
}

// run --push publishes the outputs first and signs their digests; any
// output that breaks a rule publishes nothing, and the local result is
// still recorded and reported.
func TestRunPushOutputs(t *testing.T) {
	signed := fakePublish(t, nil, nil)
	pushed := fakeOutputs(t, nil, nil)
	c := outputsContract(t, "[coverage.xml]", "sh", "-c", "printf cov > coverage.xml")
	result := filepath.Join(t.TempDir(), "r.json")
	code, stdout, stderr := runCLI("run", "-f", c, "--registry", "reg.example/r", "--push", "--result-file", result)
	if code != ExitOK || !strings.Contains(stdout, "pushed reg.example/r@sha256:bb") || len(*signed) != 1 || fmt.Sprint(*pushed) != fmt.Sprint([]registry.Output{{Path: "coverage.xml", Data: []byte("cov")}}) {
		t.Fatalf("exit %d, stdout %q, stderr %q, pushed %v", code, stdout, stderr, *pushed)
	}
	p, err := evidence.Parse((*signed)[0].Predicate)
	if err != nil || p.Execution.Outputs == nil || p.Execution.Outputs.Files[0] != (evidence.OutputFile{Path: "coverage.xml", Digest: sha, Size: 3}) {
		t.Fatalf("signed predicate %+v %v", p, err)
	}

	for name, tc := range map[string]struct {
		argv    []string
		pushErr error
		want    string
	}{
		"missing output": {[]string{"true"}, nil, `result not published: outputs glob "coverage.xml" matched no files`},
		"push error":     {[]string{"sh", "-c", "printf cov > coverage.xml"}, errors.New("denied"), "publishing outputs: denied"},
	} {
		t.Run(name, func(t *testing.T) {
			signed := fakePublish(t, nil, nil)
			pushed := fakeOutputs(t, tc.pushErr, nil)
			c := outputsContract(t, "[coverage.xml]", tc.argv...)
			code, stdout, stderr := runCLI("run", "-f", c, "--registry", "reg.example/r", "--push", "--result-file", result)
			if code != ExitError || !strings.Contains(stderr, tc.want) || len(*signed) != 0 || (tc.pushErr == nil) != (*pushed == nil) || !strings.Contains(stdout, "ran unit: passed") {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			if _, err := os.Stat(result); err != nil {
				t.Fatalf("local result not recorded: %v", err)
			}
		})
	}
}

// A failed login comes before the upload: no outputs without a result.
func TestRunPushOutputsLoginFirst(t *testing.T) {
	pushed := fakeOutputs(t, nil, nil)
	fakeLogin(t, true, "", errors.New("closed"))
	t.Setenv(idTokenEnv, "")
	c := outputsContract(t, "[coverage.xml]", "sh", "-c", "printf cov > coverage.xml")
	if code, _, stderr := runCLI("run", "-f", c, "--registry", "reg.example/r", "--push", "--result-file", filepath.Join(t.TempDir(), "r.json")); code != ExitError || !strings.Contains(stderr, "interactive Sigstore login: closed") || *pushed != nil {
		t.Fatalf("failed login: exit %d, stderr %q, pushed %v", code, stderr, *pushed)
	}
}

// A verified result restores its outputs instead of running the test;
// verify fetches and checks them but writes nothing.
func TestReuseRestoresOutputs(t *testing.T) {
	fakeOutputs(t, nil, nil)
	fakeCandidates(t, nil, map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner, outputs: listed("coverage.xml", "reports/junit.xml")}})
	argv, ran := marker(t, 0)
	c := outputsContract(t, `[coverage.xml, "reports/*.xml"]`, argv...)
	dir := filepath.Dir(c)

	code, stdout, _ := runCLI("verify", "-f", c, "--registry", "reg.example/r", "--policy", "p.yaml")
	if _, err := os.Stat(filepath.Join(dir, "coverage.xml")); code != ExitOK || !os.IsNotExist(err) {
		t.Fatalf("verify: exit %d, stdout %q, coverage.xml %v", code, stdout, err)
	}

	write(t, dir, "coverage.xml", "stale")
	code, stdout, stderr := runCLI("run", "-f", c, "--registry", "reg.example/r", "--policy", "p.yaml", "--result-file", filepath.Join(dir, ".aval", "r.json"))
	if code != ExitOK || ran() || !strings.Contains(stdout, "restored coverage.xml\nrestored reports/junit.xml\nreused passed result\n") {
		t.Fatalf("run: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	for _, rel := range []string{"coverage.xml", "reports/junit.xml"} {
		got, _ := os.ReadFile(filepath.Join(dir, rel))
		fi, _ := os.Stat(filepath.Join(dir, rel))
		if string(got) != "bytes of "+rel || fi.Mode().Perm() != 0o644 {
			t.Fatalf("%s = %q, mode %v", rel, got, fi.Mode())
		}
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*", ".aval-restore-*")); len(left) > 0 {
		t.Fatalf("staged files left: %v", left)
	}
}

// Outputs that do not match the contract are a verification failure: the
// test runs, exits 5, and nothing is written. A sibling candidate that
// checks out is still reused, with the SECURITY line.
func TestOutputsVerificationFailures(t *testing.T) {
	cases := map[string]struct {
		contractOutputs string // "" for none
		listed          *evidence.Outputs
		fetchErr        error
	}{
		"result without outputs":        {"[coverage.xml]", nil, nil},
		"outputs the contract lacks":    {"", listed("coverage.xml"), nil},
		"path outside the globs":        {"[coverage.xml]", listed("other.xml"), nil},
		"glob without a path":           {"[coverage.xml, junit.xml]", listed("coverage.xml"), nil},
		"path that is an input":         {`["**"]`, listed("in.txt"), nil},
		"path under .git":               {`["**"]`, listed(".git/hooks/pre-commit"), nil},
		"gitfile":                       {`["**"]`, listed(".git"), nil},
		"path that is the contract":     {`["*.yaml"]`, listed("aval.yaml"), nil},
		"path under .aval":              {`["**"]`, listed(".aval/result.json"), nil},
		"stored outputs differ (fetch)": {"[coverage.xml]", listed("coverage.xml"), &evidence.VerificationError{Reason: "outputs: layer 0 does not match"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fakeOutputs(t, nil, tc.fetchErr)
			fakeCandidates(t, nil, map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner, outputs: tc.listed}})
			argv, ran := marker(t, 0)
			c := contractWith(t, "hermetic", argv...)
			if tc.contractOutputs != "" {
				c = outputsContract(t, tc.contractOutputs, argv...)
			}
			report := filepath.Join(t.TempDir(), "report.json")
			code, stdout, stderr := runCLI("run", "-f", c, "--registry", "reg.example/r", "--policy", "p.yaml", "--result-file", filepath.Join(t.TempDir(), "r.json"), "--report-file", report)
			if r := readReport(t, report); code != ExitVerificationFailed || !ran() || r.Outcome != evidence.OutcomeVerificationFailed || !strings.Contains(stderr, "SECURITY") || strings.Contains(stdout, "restored") {
				t.Fatalf("exit %d, report %+v, stdout %q, stderr %q", code, r, stdout, stderr)
			}
		})
	}

	fakeOutputs(t, nil, nil)
	fakeCandidates(t, nil, map[string]candidate{
		"sha256:aa": {trust: evidence.TrustRunner, outputs: listed("../escape.xml")},
		"sha256:bb": {trust: evidence.TrustRunner, outputs: listed("coverage.xml")},
	})
	c := outputsContract(t, "[coverage.xml]")
	code, stdout, stderr := runCLI("run", "-f", c, "--registry", "reg.example/r", "--policy", "p.yaml", "--result-file", filepath.Join(t.TempDir(), "r.json"))
	// The rejected sibling blocks reuse (#195): the test runs and exits 5.
	if code != ExitVerificationFailed || strings.Contains(stdout, "restored") || !strings.Contains(stdout, "ran unit: passed") || !strings.Contains(stderr, "SECURITY: 1 attestation(s)") {
		t.Fatalf("sibling: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(c)), "escape.xml")); !os.IsNotExist(err) {
		t.Fatalf("escaped the contract directory: %v", err)
	}
}

// A restore that cannot write safely runs the test instead (invariant 3)
// and exits 1; it never writes through a symlink.
func TestRestoreRefuses(t *testing.T) {
	cases := map[string]struct {
		paths []string
		setup func(t *testing.T, dir string)
		want  string
	}{
		"symlinked file": {[]string{"coverage.xml"}, func(t *testing.T, dir string) {
			if err := os.Symlink(filepath.Join(dir, "in.txt"), filepath.Join(dir, "coverage.xml")); err != nil {
				t.Fatal(err)
			}
		}, "is a symlink"},
		"symlinked directory": {[]string{"reports/junit.xml"}, func(t *testing.T, dir string) {
			if err := os.Symlink(t.TempDir(), filepath.Join(dir, "reports")); err != nil {
				t.Fatal(err)
			}
		}, "is a symlink"},
		"directory in the way": {[]string{"coverage.xml"}, func(t *testing.T, dir string) { write(t, dir, "coverage.xml/x", "") }, "not a regular file"},
		"file in the way":      {[]string{"reports/junit.xml"}, func(t *testing.T, dir string) { write(t, dir, "reports", "") }, "not a directory"},
		"read-only directory": {[]string{"reports/junit.xml"}, func(t *testing.T, dir string) {
			write(t, dir, "reports/keep", "")
			chmod(t, filepath.Join(dir, "reports"), 0o555)
		}, "permission denied"},
		"conflicting paths": {[]string{"reports", "reports/junit.xml"}, nil, "restoring outputs"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fakeOutputs(t, nil, nil)
			fakeCandidates(t, nil, map[string]candidate{"sha256:aa": {trust: evidence.TrustRunner, outputs: listed(tc.paths...)}})
			argv, ran := marker(t, 0)
			globs, _ := json.Marshal(tc.paths)
			c := outputsContract(t, string(globs), argv...)
			if tc.setup != nil {
				tc.setup(t, filepath.Dir(c))
			}
			code, stdout, stderr := runCLI("run", "-f", c, "--registry", "reg.example/r", "--policy", "p.yaml", "--result-file", filepath.Join(t.TempDir(), "r.json"))
			if code != ExitError || !ran() || !strings.Contains(stderr, "restoring outputs: ") || !strings.Contains(stderr, tc.want) || strings.Contains(stdout, "reused") {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			if b, _ := os.ReadFile(filepath.Join(filepath.Dir(c), "in.txt")); string(b) != "x" {
				t.Fatalf("wrote through a symlink: in.txt = %q", b)
			}
		})
	}
}
