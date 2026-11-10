package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/registry"
)

// outputsDirStory runs a test that writes coverage.out and reports/j.xml
// with --outputs-dir, then deletes them from the contract root, as a fresh
// checkout in the publish job has none. It returns the contract, the result
// and the outputs directory.
func outputsDirStory(t *testing.T) (c, result, dir string) {
	t.Helper()
	c = outputsContract(t, `[coverage.out, "reports/*.xml"]`, "sh", "-c", "printf cov > coverage.out && mkdir -p reports && printf j > reports/j.xml")
	tmp := t.TempDir()
	result, dir = filepath.Join(tmp, "result.json"), filepath.Join(tmp, "outputs")
	if code, _, stderr := runCLI("run", "-f", c, "--result-file", result, "--outputs-dir", dir); code != ExitOK {
		t.Fatalf("run: exit %d, stderr %q", code, stderr)
	}
	root := filepath.Dir(c)
	for _, p := range []string{"coverage.out", "reports"} {
		if err := os.RemoveAll(filepath.Join(root, p)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(idTokenEnv, "token") // aval run unsets it
	return c, result, dir
}

// The outputs travel from run to push through the directory, and push
// publishes exactly the bytes in it: content that differs from what the run
// wrote is what gets published and signed, under its own digest (the
// registry digests the bytes it is given).
func TestOutputsDirCarriesOutputs(t *testing.T) {
	signed := fakePublish(t, nil, nil)
	pushed := fakeOutputs(t, nil, nil)
	c, result, dir := outputsDirStory(t)
	push := func() (int, string) {
		code, _, stderr := runCLI("push", "-f", c, "--registry", "reg.example/r", "--result-file", result, "--outputs-dir", dir)
		return code, stderr
	}
	if code, stderr := push(); code != ExitOK || len(*signed) != 1 ||
		fmt.Sprint(*pushed) != fmt.Sprint([]registry.Output{{Path: "coverage.out", Data: []byte("cov")}, {Path: "reports/j.xml", Data: []byte("j")}}) {
		t.Fatalf("push: exit %d, stderr %q, pushed %v", code, stderr, *pushed)
	}
	write(t, dir, "coverage.out", "tampered")
	if code, stderr := push(); code != ExitOK || string((*pushed)[0].Data) != "tampered" {
		t.Fatalf("tampered push: exit %d, stderr %q, pushed %v", code, stderr, *pushed)
	}
	// Without --outputs-dir push reads the checkout, which has no outputs.
	if code, _, stderr := runCLI("push", "-f", c, "--registry", "reg.example/r", "--result-file", result); code != ExitError || !strings.Contains(stderr, `outputs glob "coverage.out" matched no files`) {
		t.Fatalf("push from the root: exit %d, stderr %q", code, stderr)
	}
}

// Anything in the directory that is not a declared output, a symlink, a
// missing output or a directory that is no directory publishes nothing.
func TestOutputsDirRefuses(t *testing.T) {
	outside := t.TempDir()
	write(t, outside, "secret", "s")
	link := func(target, rel string) func(*testing.T, string) {
		return func(t *testing.T, dir string) {
			_ = os.RemoveAll(filepath.Join(dir, rel))
			if err := os.Symlink(target, filepath.Join(dir, rel)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, tc := range map[string]struct {
		edit func(t *testing.T, dir string)
		want string
	}{
		"undeclared file":         {func(t *testing.T, dir string) { write(t, dir, "notes.txt", "n") }, "notes.txt, which is not a declared output"},
		"undeclared directory":    {func(t *testing.T, dir string) { write(t, dir, "extra/coverage.out", "x") }, "extra, which is not a declared output"},
		"undeclared in a glob":    {func(t *testing.T, dir string) { write(t, dir, "reports/j.txt", "x") }, "reports/j.txt, which is not a declared output"},
		"an input":                {func(t *testing.T, dir string) { write(t, dir, "in.txt", "x") }, "in.txt, which is not a declared output"},
		".aval entry":             {func(t *testing.T, dir string) { write(t, dir, ".aval/coverage.out", "x") }, ".aval, which is not a declared output"},
		".git entry":              {func(t *testing.T, dir string) { write(t, dir, ".git", "gitdir: x") }, ".git, which is not a declared output"},
		"symlink out":             {link(filepath.Join(outside, "secret"), "coverage.out"), "coverage.out is not a regular file"},
		"symlinked directory":     {link(outside, "reports"), "reports, which is not a declared output"},
		"missing output":          {func(t *testing.T, dir string) { _ = os.Remove(filepath.Join(dir, "coverage.out")) }, `outputs glob "coverage.out" matched no files`},
		"directory is symlink":    {func(t *testing.T, dir string) { _ = os.RemoveAll(dir); link(outside, "")(t, dir) }, "not a directory"},
		"directory is a file":     {func(t *testing.T, dir string) { _ = os.RemoveAll(dir); write(t, filepath.Dir(dir), "outputs", "x") }, "not a directory"},
		"directory is missing":    {func(t *testing.T, dir string) { _ = os.RemoveAll(dir) }, "no such file"},
		"contract has no outputs": {nil, "the contract declares no outputs"},
	} {
		t.Run(name, func(t *testing.T) {
			signed := fakePublish(t, nil, nil)
			pushed := fakeOutputs(t, nil, nil)
			c, result, dir := outputsDirStory(t)
			if tc.edit != nil {
				tc.edit(t, dir)
			} else {
				body, _ := os.ReadFile(c)
				write(t, filepath.Dir(c), "aval.yaml", strings.Split(string(body), "outputs:")[0])
				if code, _, stderr := runCLI("run", "-f", c, "--result-file", result); code != ExitOK {
					t.Fatalf("run: exit %d, stderr %q", code, stderr)
				}
				t.Setenv(idTokenEnv, "token")
			}
			code, _, stderr := runCLI("push", "-f", c, "--registry", "reg.example/r", "--result-file", result, "--outputs-dir", dir)
			if code != ExitError || !strings.Contains(stderr, "result not published") || !strings.Contains(stderr, tc.want) || len(*signed) != 0 || *pushed != nil {
				t.Fatalf("exit %d, stderr %q, want 1 and ~%q, nothing published", code, stderr, tc.want)
			}
		})
	}
}

// run refuses a non-empty --outputs-dir, or a file, before the test runs, warns when
// the outputs cannot be copied, and copies nothing for a contract without
// outputs.
func TestRunOutputsDir(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stale", "x")
	argv, ran := marker(t, 0)
	c := outputsContract(t, "[coverage.out]", argv...)
	if code, _, stderr := runCLI("run", "-f", c, "--outputs-dir", dir, "--result-file", filepath.Join(t.TempDir(), "r.json")); code != ExitUsage || !strings.Contains(stderr, "is not empty") || ran() {
		t.Fatalf("non-empty: exit %d, stderr %q", code, stderr)
	}
	if code, _, stderr := runCLI("run", "-f", c, "--outputs-dir", filepath.Join(dir, "stale"), "--result-file", filepath.Join(t.TempDir(), "r.json")); code != ExitUsage || !strings.Contains(stderr, "not a directory") || ran() {
		t.Fatalf("a file: exit %d, stderr %q", code, stderr)
	}
	empty := filepath.Join(t.TempDir(), "o")
	if code, _, stderr := runCLI("run", "-f", c, "--outputs-dir", empty, "--result-file", filepath.Join(t.TempDir(), "r.json")); code != ExitOK || !strings.Contains(stderr, "outputs not copied to "+empty) {
		t.Fatalf("missing output: exit %d, stderr %q", code, stderr)
	}
	locked := t.TempDir()
	chmod(t, locked, 0o555)
	cov := outputsContract(t, "[coverage.out]", "sh", "-c", "printf cov > coverage.out")
	if code, _, stderr := runCLI("run", "-f", cov, "--outputs-dir", filepath.Join(locked, "o"), "--result-file", filepath.Join(t.TempDir(), "r.json")); code != ExitOK || !strings.Contains(stderr, "permission denied") {
		t.Fatalf("unwritable: exit %d, stderr %q", code, stderr)
	}
	plain := contractWith(t, "hermetic", "true")
	if code, _, stderr := runCLI("run", "-f", plain, "--outputs-dir", empty, "--result-file", filepath.Join(t.TempDir(), "r.json")); code != ExitOK || stderr != "" {
		t.Fatalf("no outputs: exit %d, stderr %q", code, stderr)
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Fatalf("outputs dir created without outputs: %v", err)
	}
}
