package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/policy"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// initProject is a fresh current directory with one source file.
func initProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("TERM", "xterm")
	t.Setenv(outputEnv, "")
	if err := os.MkdirAll("src", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("src/a.txt", []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// terminals sets whether stdin and stdout count as terminals.
func terminals(t *testing.T, in, out bool) {
	t.Helper()
	term, tty := isTerminal, isTTY
	t.Cleanup(func() { isTerminal, isTTY = term, tty })
	isTerminal = func() bool { return in }
	isTTY = func(io.Writer) bool { return out }
}

// runInitCLI runs aval with stdin.
func runInitCLI(stdin string, args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	root := newRootCmd()
	root.SetArgs(args)
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&out)
	root.SetErr(&errOut)
	err := root.Execute()
	printErrors(&errOut, err)
	return exitCode(err), out.String(), errOut.String()
}

var keyID = regexp.MustCompile(`key id [A-Za-z0-9+/=]+`)

// golden compares got, with the key id masked, to testdata/name.
func golden(t *testing.T, name, got string) {
	t.Helper()
	got = keyID.ReplaceAllString(got, "key id <kid>")
	p := filepath.Join(wd, "testdata", name)
	if *update {
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s:\n%s\nwant:\n%s", name, got, want)
	}
}

// wd is the package directory, before any test changes it.
var wd, _ = os.Getwd()

var initArgs = []string{"init", "--name", "unit", "--source", "src/**", "--cacheability", "hermetic", "--key", "aval.key", "--", "grep", "-q", "a", "src/a.txt"}

// The flag form writes files that internal/contract and internal/policy
// load, ignores the key and .aval/, and prints the plain or styled lines.
func TestInitFlags(t *testing.T) {
	for _, styled := range []bool{false, true} {
		initProject(t)
		terminals(t, false, styled)
		code, out, errOut := runInitCLI("", initArgs...)
		if code != ExitOK || errOut != "" {
			t.Fatalf("exit %d, stderr %q", code, errOut)
		}
		name := "init.golden.txt"
		if styled {
			name = "init-styled.golden.txt"
		}
		golden(t, name, out)
		c, err := contract.Load("aval.yaml")
		if err != nil || c.Metadata.Name != "unit" || c.Cacheability != contract.Hermetic || strings.Join(c.Runner.Command, " ") != "grep -q a src/a.txt" {
			t.Fatalf("contract %+v, %v", c, err)
		}
		if p, err := policy.Load(defaultPolicy); err != nil || len(p.Identities) != 1 || p.Identities[0].TrustLevel != evidence.TrustDeveloper {
			t.Fatalf("policy %+v, %v", p, err)
		}
		if st, err := os.Stat("aval.key"); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("aval.key: %v %v", st, err)
		}
		if gi, _ := os.ReadFile(".gitignore"); string(gi) != "aval.key\n.aval/\n" {
			t.Fatalf(".gitignore %q", gi)
		}
	}
}

// No defaults and no inference: without a terminal a missing value is exit
// 2 naming the flags, a glob matching nothing or an invalid one is exit 2,
// and the test command is never run. Nothing is written in any of them.
func TestInitNoDefaults(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no source", []string{"init", "--name", "u", "--cacheability", "hermetic", "--key", "k.key", "--", "true"}, "missing --source;"},
		{"no cacheability", []string{"init", "--name", "u", "--source", "src/**", "--key", "k.key", "--", "true"}, "missing --cacheability;"},
		{"nothing", []string{"init"}, "missing --name, -- <command>, --source, --cacheability, --key or --keyless;"},
		{"empty glob", []string{"init", "--name", "u", "--source", "src/**", "--source", "lib/**", "--cacheability", "hermetic", "--key", "k.key", "--", "touch", "traced"}, `source glob "lib/**" matched no files`},
		{"invalid glob", []string{"init", "--name", "u", "--source", "../x", "--cacheability", "hermetic", "--keyless", "--", "true"}, `scope.source[0]: "../x"`},
		{"invalid class", []string{"init", "--name", "u", "--source", "src/**", "--cacheability", "cached", "--keyless", "--", "true"}, "invalid contract"},
		{"key and keyless", []string{"init", "--name", "u", "--source", "src/**", "--cacheability", "hermetic", "--key", "k.key", "--keyless", "--", "true"}, "exclude each other"},
		{"argument before --", []string{"init", "true"}, `unexpected argument "true"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			initProject(t)
			terminals(t, false, false)
			code, out, errOut := runInitCLI("", c.args...)
			if code != ExitUsage || out != "" || !strings.Contains(errOut, c.want) {
				t.Fatalf("exit %d, stdout %q, stderr %q; want %d and %q", code, out, errOut, ExitUsage, c.want)
			}
			if ents, _ := os.ReadDir("."); len(ents) != 1 {
				t.Fatalf("wrote files: %v", ents)
			}
		})
	}
	// A terminal on one side only still never prompts.
	initProject(t)
	terminals(t, true, false)
	if code, _, _ := runInitCLI("", "init"); code != ExitUsage {
		t.Fatalf("stdin terminal only: exit %d", code)
	}
}

// Existing files are kept without --force and replaced with it.
func TestInitNoOverwrite(t *testing.T) {
	for _, f := range []string{"aval.yaml", "policy.yaml", "aval.key", "aval.pub"} {
		initProject(t)
		terminals(t, false, false)
		if err := os.WriteFile(f, []byte("mine\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, _, errOut := runInitCLI("", initArgs...)
		if code != ExitError || !strings.Contains(errOut, f+" already exists; pass --force") {
			t.Fatalf("%s: exit %d, %s", f, code, errOut)
		}
		if b, _ := os.ReadFile(f); string(b) != "mine\n" {
			t.Fatalf("%s overwritten", f)
		}
		if ents, _ := os.ReadDir("."); len(ents) != 2 {
			t.Fatalf("%s: wrote files: %v", f, ents)
		}
		if code, _, errOut = runInitCLI("", append(initArgs[:1:1], append([]string{"--force"}, initArgs[1:]...)...)...); code != ExitOK {
			t.Fatalf("%s --force: exit %d, %s", f, code, errOut)
		}
		if b, _ := os.ReadFile(f); string(b) == "mine\n" {
			t.Fatalf("%s not replaced with --force", f)
		}
	}
	// A second --force run adds nothing to .gitignore.
	if code, _, _ := runInitCLI("", append([]string{"init", "--force"}, initArgs[1:]...)...); code != ExitOK {
		t.Fatal(code)
	}
	if gi, _ := os.ReadFile(".gitignore"); string(gi) != "aval.key\n.aval/\n" {
		t.Fatalf(".gitignore %q", gi)
	}
}

// --keyless writes the contract only; a key name without .key gets it; an
// existing .gitignore keeps its lines and gains only the missing ones; a
// long preview is cut.
func TestInitVariants(t *testing.T) {
	initProject(t)
	terminals(t, false, false)
	if err := os.WriteFile(".gitignore", []byte("/.aval/"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range 11 {
		if err := os.WriteFile(filepath.Join("src", string(rune('b'+i))), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	code, out, _ := runInitCLI("", "init", "--name", "u", "--source", "src/**", "--cacheability", "non-deterministic", "--keyless", "--", "touch", "traced")
	if _, err := os.Stat("traced"); err == nil {
		t.Fatal("init ran the test command")
	}
	if code != ExitOK || !strings.Contains(out, "source matches 12 files: src/a.txt, src/b, ") || !strings.Contains(out, " and 2 more\n") || !strings.Contains(out, "next: write a trust policy") || strings.Contains(out, ".gitignore") {
		t.Fatalf("keyless: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(defaultPolicy); err == nil {
		t.Fatal("keyless wrote a policy")
	}
	code, out, _ = runInitCLI("", "init", "--force", "--name", "u", "--source", "src/a.txt", "--cacheability", "hermetic", "--key", "ci", "--", "true")
	if code != ExitOK || !strings.Contains(out, "wrote ci.key ") || !strings.HasSuffix(out, "--key ci.key\n") {
		t.Fatalf("key ci: exit %d\n%s", code, out)
	}
	if gi, _ := os.ReadFile(".gitignore"); string(gi) != "/.aval/\nci.key\n" {
		t.Fatalf(".gitignore %q", gi)
	}
}

// Write failures are errors (exit 1).
func TestInitWriteErrors(t *testing.T) {
	force := append([]string{"init", "--force"}, initArgs[1:]...)
	for _, dir := range []string{"aval.yaml", "policy.yaml", ".gitignore"} {
		initProject(t)
		terminals(t, false, false)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if code, _, errOut := runInitCLI("", force...); code != ExitError {
			t.Fatalf("%s is a directory: exit %d, %s", dir, code, errOut)
		}
	}
	initProject(t)
	if code, _, _ := runInitCLI("", "init", "--name", "u", "--source", "src/**", "--cacheability", "hermetic", "--key", "missing/k.key", "--", "true"); code != ExitError {
		t.Fatalf("key in a missing directory: exit %d", code)
	}
	initProject(t)
	if err := os.WriteFile(".gitignore", nil, 0o444); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runInitCLI("", initArgs...); code != ExitError {
		t.Fatalf("read-only .gitignore: exit %d", code)
	}
}

// On a terminal init asks for each missing value in numbered questions,
// asks again after a blank or invalid answer, and end of input cancels.
func TestInitPrompts(t *testing.T) {
	initProject(t)
	terminals(t, true, true)
	answers := "unit\ngrep -q a src/a.txt\n\nlib/**\n../x\nsrc/**\n9\n1\naval.key"
	code, out, errOut := runInitCLI(answers, "init")
	if code != ExitOK || errOut != "" {
		t.Fatalf("exit %d, %s", code, errOut)
	}
	golden(t, "init-prompts.golden.txt", out)
	if _, err := contract.Load("aval.yaml"); err != nil {
		t.Fatal(err)
	}

	initProject(t)
	terminals(t, true, true)
	if code, _, _ = runInitCLI("keyless\n", "init", "--name", "u", "--source", "src/**", "--cacheability", "hermetic", "--", "true"); code != ExitOK {
		t.Fatalf("keyless answer: exit %d", code)
	}
	if _, err := os.Stat(defaultPolicy); err == nil {
		t.Fatal("keyless answer wrote a policy")
	}

	initProject(t)
	terminals(t, true, true)
	if code, out, _ = runInitCLI("u\n", "init", "--source", "src/**", "--cacheability", "hermetic", "--key", "k.key", "--", "true"); code != ExitOK || !strings.HasPrefix(out, "1/1 Test name") {
		t.Fatalf("name only: exit %d, %s", code, out)
	}

	for i := range 5 {
		initProject(t)
		terminals(t, true, true)
		in := strings.Join(strings.Split(answers, "\n")[:[]int{0, 1, 2, 6, 8}[i]], "\n")
		code, _, errOut := runInitCLI(in, "init")
		if code != ExitUsage || !strings.Contains(errOut, "init cancelled: nothing written") {
			t.Fatalf("cancel at %d: exit %d, %s", i+1, code, errOut)
		}
		if ents, _ := os.ReadDir("."); len(ents) != 1 {
			t.Fatalf("cancel at %d wrote files: %v", i+1, ents)
		}
	}
}
