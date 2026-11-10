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
		c, err := contract.Load("aval.yaml", "")
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
		{"absolute key", []string{"init", "--name", "u", "--source", "src/**", "--cacheability", "hermetic", "--key", "/tmp/k.key", "--", "true"}, "must be a relative path"},
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
	for _, want := range []string{"source matches 12 files: src/a.txt, src/b, ", " and 2 more\n", "next: write a trust policy", "; non-deterministic results are never reused\n"} {
		if code != ExitOK || !strings.Contains(out, want) || strings.Contains(out, ".gitignore") {
			t.Fatalf("keyless: exit %d, want %q\n%s", code, want, out)
		}
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
	// A directory that --force cannot remove fails the write, and the key
	// pair written before it is removed again.
	for _, dir := range []string{"aval.yaml", "policy.yaml", ".gitignore"} {
		initProject(t)
		terminals(t, false, false)
		if err := os.MkdirAll(filepath.Join(dir, "x"), 0o755); err != nil {
			t.Fatal(err)
		}
		if code, _, errOut := runInitCLI("", force...); code != ExitError {
			t.Fatalf("%s is a directory: exit %d, %s", dir, code, errOut)
		}
		if _, err := os.Lstat("aval.key"); err == nil {
			t.Fatalf("%s: aval.key left behind", dir)
		}
	}
	// An unwritable directory fails the contract write.
	initProject(t)
	if err := os.Chmod(".", 0o555); err != nil {
		t.Fatal(err)
	}
	code, _, _ := runInitCLI("", "init", "--name", "u", "--source", "src/**", "--cacheability", "hermetic", "--keyless", "--", "true")
	_ = os.Chmod(".", 0o755)
	if code != ExitError {
		t.Fatalf("unwritable directory: exit %d", code)
	}
	// A key path that cannot be checked is an error, not "absent".
	initProject(t)
	if code, _, errOut := runInitCLI("", "init", "--name", "u", "--source", "src/**", "--cacheability", "hermetic", "--key", "src/a.txt/k", "--", "true"); code != ExitError || !strings.Contains(errOut, "not a directory") {
		t.Fatalf("key under a file: exit %d, %s", code, errOut)
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
	answers := "Bad Name\nunit\ngrep -q a src/a.txt\n\nlib/**\n../x\nsrc/**\n9\nhermetic\n../k.key\naval.key"
	code, out, errOut := runInitCLI(answers, "init")
	if code != ExitOK || errOut != "" {
		t.Fatalf("exit %d, %s", code, errOut)
	}
	golden(t, "init-prompts.golden.txt", out)
	if _, err := contract.Load("aval.yaml", ""); err != nil {
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

	// A class by number; an existing key file is refused at the prompt,
	// before anything is written.
	initProject(t)
	terminals(t, true, true)
	if err := os.WriteFile("aval.pub", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, _ = runInitCLI("2\naval.key\nk.key\n", "init", "--name", "u", "--source", "src/**", "--", "true"); code != ExitOK || !strings.Contains(out, "aval.pub already exists") {
		t.Fatalf("existing key: exit %d, %s", code, out)
	}

	for i := range 5 {
		initProject(t)
		terminals(t, true, true)
		in := strings.Join(strings.Split(answers, "\n")[:[]int{0, 2, 3, 7, 9}[i]], "\n")
		code, _, errOut := runInitCLI(in, "init")
		if code != ExitUsage || !strings.Contains(errOut, "init cancelled: nothing written") {
			t.Fatalf("cancel at %d: exit %d, %s", i+1, code, errOut)
		}
		if ents, _ := os.ReadDir("."); len(ents) != 1 {
			t.Fatalf("cancel at %d wrote files: %v", i+1, ents)
		}
	}
}

// --force replaces a symlink at policy.yaml or aval.yaml, never the file it
// points to.
func TestInitForceSymlink(t *testing.T) {
	initProject(t)
	terminals(t, false, false)
	for _, f := range []string{"policy.yaml", "aval.yaml"} {
		if err := os.WriteFile("target-"+f, []byte("mine\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("target-"+f, f); err != nil {
			t.Fatal(err)
		}
	}
	if code, _, errOut := runInitCLI("", append([]string{"init", "--force"}, initArgs[1:]...)...); code != ExitOK {
		t.Fatalf("exit %d, %s", code, errOut)
	}
	for _, f := range []string{"policy.yaml", "aval.yaml"} {
		if b, _ := os.ReadFile("target-" + f); string(b) != "mine\n" {
			t.Fatalf("%s written through the symlink", f)
		}
		if st, err := os.Lstat(f); err != nil || !st.Mode().IsRegular() {
			t.Fatalf("%s: %v %v", f, st, err)
		}
	}
}

// File names from the tree are escaped in the preview, so an escape
// sequence in a name never reaches the terminal.
func TestInitPreviewEscapes(t *testing.T) {
	initProject(t)
	terminals(t, false, true)
	if err := os.WriteFile("src/\x1b[2J", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runInitCLI("", initArgs...)
	if code != ExitOK || !strings.Contains(out, `src/\x1b[2J`) || strings.Contains(out, "\x1b[2J") {
		t.Fatalf("exit %d, %q", code, out)
	}
}

var setArgs = []string{"init", "--set", "--name", "unit", "--source", "src/**", "--cacheability", "hermetic", "--", "grep", "-q", "a", "src/a.txt"}

// --set writes a TestSet with one test and one fragment plus a placeholder
// keyless policy, golden byte for byte, that aval explain --all accepts.
func TestInitSet(t *testing.T) {
	initProject(t)
	terminals(t, false, false)
	code, out, errOut := runInitCLI("", setArgs...)
	if code != ExitOK || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	golden(t, "init-set.golden.txt", out)
	for f, g := range map[string]string{"aval.yaml": "init-set-aval.golden.yaml", setPolicy: "init-set-policy.golden.yaml"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		golden(t, g, string(b))
	}
	if code, out, errOut := runCLI("explain", "--all"); code != ExitOK {
		t.Fatalf("explain --all: exit %d\n%s%s", code, out, errOut)
	}
	cs, err := contract.LoadAll("aval.yaml")
	if err != nil || len(cs) != 1 || cs[0].Metadata.Name != "unit" || cs[0].Cacheability != contract.Hermetic || strings.Join(cs[0].Scope.Source, " ") != "src/**" {
		t.Fatalf("set %+v, %v", cs, err)
	}
	for _, f := range []string{"aval.key", "aval.pub", defaultPolicy} {
		if _, err := os.Lstat(f); err == nil {
			t.Errorf("--set wrote %s", f)
		}
	}
	if gi, _ := os.ReadFile(".gitignore"); string(gi) != ".aval/\n" {
		t.Fatalf(".gitignore %q", gi)
	}
}

// False hits: the placeholder identity --set writes trusts no real signer,
// not even one whose subject only contains the placeholder.
func TestInitSetPlaceholderTrustsNothing(t *testing.T) {
	initProject(t)
	terminals(t, false, false)
	if code, _, errOut := runInitCLI("", setArgs...); code != ExitOK {
		t.Fatalf("exit %d, %s", code, errOut)
	}
	p, err := policy.Load(setPolicy)
	if err != nil || len(p.Identities) != 1 {
		t.Fatalf("policy %+v, %v", p, err)
	}
	ids := p.Identities
	for _, s := range []string{
		"https://github.com/TrianaLab/aval/.github/workflows/ci.yaml@refs/heads/main",
		"https://github.com/OWNER/REPO/.github/workflows/ci.yaml@refs/heads/main-evil",
		"https://evil.example/https://github.com/OWNER/REPO/.github/workflows/ci.yaml@refs/heads/main",
		"https://githubXcom/OWNER/REPO/.github/workflows/ci.yaml@refs/heads/main",
	} {
		if _, ok := evidence.MatchIdentity(ids, "https://token.actions.githubusercontent.com", s); ok {
			t.Errorf("placeholder trusts %q", s)
		}
	}
}

// --set refuses to replace either file (exit 2) unless --force.
func TestInitSetNoOverwrite(t *testing.T) {
	for _, f := range []string{"aval.yaml", setPolicy} {
		initProject(t)
		terminals(t, false, false)
		if err := os.MkdirAll(filepath.Dir(setPolicy), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("mine\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := runInitCLI("", setArgs...)
		if code != ExitUsage || out != "" || !strings.Contains(errOut, f+" already exists; pass --force") {
			t.Fatalf("%s: exit %d, %s", f, code, errOut)
		}
		if b, _ := os.ReadFile(f); string(b) != "mine\n" {
			t.Fatalf("%s overwritten", f)
		}
		if code, _, errOut = runInitCLI("", append([]string{"init", "--force"}, setArgs[1:]...)...); code != ExitOK {
			t.Fatalf("%s --force: exit %d, %s", f, code, errOut)
		}
		if b, _ := os.ReadFile(f); string(b) == "mine\n" {
			t.Fatalf("%s not replaced with --force", f)
		}
	}
}

// --set excludes --key and --keyless and never asks for a key.
func TestInitSetFlags(t *testing.T) {
	for _, extra := range []string{"--key=k.key", "--keyless"} {
		initProject(t)
		if code, _, errOut := runInitCLI("", append([]string{"init", extra}, setArgs[1:]...)...); code != ExitUsage || !strings.Contains(errOut, "excludes --key and --keyless") {
			t.Fatalf("%s: exit %d, %s", extra, code, errOut)
		}
	}
	initProject(t)
	if code, _, errOut := runInitCLI("", "init", "--set"); code != ExitUsage || !strings.Contains(errOut, "missing --name, -- <command>, --source, --cacheability;") {
		t.Fatalf("missing: exit %d, %s", code, errOut)
	}
	initProject(t)
	terminals(t, true, true)
	code, out, _ := runInitCLI("unit\ntrue\nsrc/**\n1\n", "init", "--set")
	if code != ExitOK || !strings.Contains(out, "4/4 Cacheability") || strings.Contains(out, "Signing key") {
		t.Fatalf("prompts: exit %d\n%s", code, out)
	}
}

// A failed write after the policy removes the policy again (exit 1).
func TestInitSetWriteError(t *testing.T) {
	initProject(t)
	terminals(t, false, false)
	if err := os.MkdirAll("aval.yaml/x", 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runInitCLI("", append([]string{"init", "--force"}, setArgs[1:]...)...); code != ExitError {
		t.Fatalf("exit %d, %s", code, errOut)
	}
	if _, err := os.Lstat(setPolicy); err == nil {
		t.Fatal("policy left behind")
	}
	initProject(t)
	if err := os.WriteFile(".github", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runInitCLI("", setArgs...); code != ExitError {
		t.Fatalf(".github is a file: exit %d, %s", code, errOut)
	}
	initProject(t)
	if err := os.MkdirAll(".github", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", ".github/aval"); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runInitCLI("", setArgs...); code != ExitError {
		t.Fatalf(".github/aval is a dangling symlink: exit %d, %s", code, errOut)
	}
}
