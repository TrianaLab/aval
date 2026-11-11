package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// envContract writes a hermetic contract that declares GOTOOLCHAIN and MODE
// in environment.env and returns its path.
func envContract(t *testing.T) string {
	t.Helper()
	c := writeContract(t, "hermetic")
	body, _ := os.ReadFile(c)
	if err := os.WriteFile(c, append(body, "environment:\n  env: [MODE, GOTOOLCHAIN]\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	return c
}

// splitRun runs c as the suite job does, with GOTOOLCHAIN=local and a
// secret MODE, then unsets both as in a publish job with other variables.
// It returns the result, report and env files under the contract's .aval/.
func splitRun(t *testing.T, c string) (result, report, env string) {
	t.Helper()
	out := filepath.Join(filepath.Dir(c), ".aval")
	result, report, env = filepath.Join(out, "result.json"), filepath.Join(out, "report.json"), filepath.Join(out, "env.txt")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("MODE", "s3cret-value")
	if code, _, stderr := runCLI("run", "-f", c, "--result-file", result, "--report-file", report, "--env-file", env); code != ExitOK {
		t.Fatalf("run: exit %d, stderr %q", code, stderr)
	}
	for _, k := range []string{"GOTOOLCHAIN", "MODE"} {
		if err := os.Unsetenv(k); err != nil { // t.Setenv restores them
			t.Fatal(err)
		}
	}
	return result, report, env
}

// pushEnv runs aval push for c with the result, report and env files.
func pushEnv(t *testing.T, c, result, report, env string) (int, string, string) {
	t.Helper()
	t.Setenv(idTokenEnv, "token") // aval run unsets it
	return runCLI("push", "-f", c, "--registry", "reg.example/r", "--result-file", result, "--report-file", report, "--env-file", env)
}

// Issue #247: a result recorded with GOTOOLCHAIN=local and published with
// it unset pushes with the env file, and still exits 1 without it.
func TestPushEnvFileSplitJob(t *testing.T) {
	signed := fakePublish(t, nil, nil)
	c := envContract(t)
	result, report, env := splitRun(t, c)
	if code, _, stderr := pushWith(t, c, result, report); code != ExitError || !strings.Contains(stderr, "run the test again") || len(*signed) != 0 {
		t.Fatalf("without --env-file: exit %d, stderr %q, signed %d", code, stderr, len(*signed))
	}
	if code, stdout, stderr := pushEnv(t, c, result, report, env); code != ExitOK || !strings.Contains(stdout, "pushed reg.example/r@sha256:bb") || len(*signed) != 1 {
		t.Fatalf("with --env-file: exit %d, stdout %q, stderr %q, signed %d", code, stdout, stderr, len(*signed))
	}
}

// False hit: a valid env file cannot cover a source file that differs in
// the publish checkout.
func TestPushEnvFileSourceDiffers(t *testing.T) {
	signed := fakePublish(t, nil, nil)
	writes := countPublish(t)
	c := envContract(t)
	result, report, env := splitRun(t, c)
	if err := os.WriteFile(filepath.Join(filepath.Dir(c), "in.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := pushEnv(t, c, result, report, env); code != ExitError || !strings.Contains(stderr, "run the test again") || len(*signed) != 0 || *writes != 0 {
		t.Fatalf("exit %d, stderr %q, signed %d, writes %d", code, stderr, len(*signed), *writes)
	}
}

// False hits: an env file that is not exactly the one run wrote for
// environment.env, or whose hash was edited, publishes nothing.
func TestPushEnvFileRejects(t *testing.T) {
	signed := fakePublish(t, nil, nil)
	writes := countPublish(t)
	c := envContract(t)
	result, report, env := splitRun(t, c)
	raw, err := os.ReadFile(env)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("env file %q: want a version and two env lines", raw)
	}
	hash := strings.Fields(lines[1])[2]
	join := func(l ...string) string { return strings.Join(l, "\n") + "\n" }
	cases := map[string]struct{ body, want string }{
		"extra name":        {join(lines[0], lines[1], lines[2], "env OTHER unset"), "env file names"},
		"missing name":      {join(lines[0], lines[1]), "env file names"},
		"duplicate":         {join(lines[0], lines[1], lines[1], lines[2]), "given twice"},
		"unsorted":          {join(lines[0], lines[2], lines[1]), "env file names"},
		"other version":     {join("aval-fingerprint/v1", lines[1], lines[2]), "manifest version"},
		"non-hex hash":      {join(lines[0], "env GOTOOLCHAIN "+strings.Repeat("z", 64), lines[2]), "env file line 2"},
		"uppercase hash":    {join(lines[0], "env GOTOOLCHAIN "+strings.ToUpper(hash), lines[2]), "env file line 2"},
		"short hash":        {join(lines[0], "env GOTOOLCHAIN "+hash[:63], lines[2]), "env file line 2"},
		"value in clear":    {join(lines[0], "env GOTOOLCHAIN local", lines[2]), "env file line 2"},
		"extra field":       {join(lines[0], lines[1]+" x", lines[2]), "env file line 2"},
		"other line kind":   {join(lines[0], "source "+hash+" - in.txt", lines[1], lines[2]), "env file line 2"},
		"empty":             {"", "does not end in a newline"},
		"no final newline":  {strings.TrimSuffix(join(lines...), "\n"), "does not end in a newline"},
		"edited hash":       {join(lines[0], "env GOTOOLCHAIN "+strings.Repeat("0", 64), lines[2]), "run the test again"},
		"hash set to unset": {join(lines[0], "env GOTOOLCHAIN unset", lines[2]), "run the test again"},
	}
	for name, tc := range cases {
		p := filepath.Join(filepath.Dir(env), "bad.txt")
		if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		if code, _, stderr := pushEnv(t, c, result, report, p); code != ExitError || !strings.Contains(stderr, tc.want) || len(*signed) != 0 || *writes != 0 {
			t.Errorf("%s: exit %d, stderr %q, signed %d, writes %d; want 1, ~%q, nothing pushed", name, code, stderr, len(*signed), *writes, tc.want)
		}
	}
	if code, _, stderr := pushEnv(t, c, result, report, filepath.Join(t.TempDir(), "none.txt")); code != ExitError || !strings.Contains(stderr, "--env-file") || len(*signed) != 0 {
		t.Fatalf("missing env file: exit %d, stderr %q", code, stderr)
	}
}

// An env file inside the contract scope and outside .aval/ is an input:
// push exits 2 as for --result-file, and so does a later run.
func TestEnvFileInScope(t *testing.T) {
	signed := fakePublish(t, nil, nil)
	c := envContract(t)
	result, report, env := splitRun(t, c)
	inScope := filepath.Join(filepath.Dir(c), "env.txt") // scope.source is *.txt
	raw, _ := os.ReadFile(env)
	if err := os.WriteFile(inScope, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := pushEnv(t, c, result, report, inScope); code != ExitUsage || !strings.Contains(stderr, "is an input of the test") || len(*signed) != 0 {
		t.Fatalf("push: exit %d, stderr %q", code, stderr)
	}
	if code, _, stderr := runCLI("run", "-f", c, "--result-file", result, "--env-file", inScope); code != ExitUsage || !strings.Contains(stderr, "--env-file") {
		t.Fatalf("run: exit %d, stderr %q", code, stderr)
	}
}

// run does not overwrite a file at --env-file that is not an env file, and
// overwrites the one an earlier run wrote.
func TestRunEnvFileOverwrite(t *testing.T) {
	c := writeContract(t, "hermetic")
	dir := t.TempDir()
	result, env := filepath.Join(dir, "result.json"), filepath.Join(dir, "notes")
	if err := os.WriteFile(env, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCLI("run", "-f", c, "--result-file", result, "--env-file", env); code != ExitUsage || !strings.Contains(stderr, "is not an env file") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if b, _ := os.ReadFile(env); string(b) != "keep me\n" {
		t.Fatalf("overwritten: %q", b)
	}
	env = filepath.Join(dir, "env.txt")
	for range 2 {
		if code, _, stderr := runCLI("run", "-f", c, "--result-file", result, "--env-file", env); code != ExitOK {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
	}
	if b, _ := os.ReadFile(env); string(b) != "aval-fingerprint/v2\n" {
		t.Fatalf("env file of a contract without environment.env: %q", b)
	}
	// An env file that cannot be written fails the recording.
	if code, _, stderr := runCLI("run", "-f", c, "--result-file", result, "--env-file", filepath.Join(result, "env.txt")); code != ExitError || !strings.Contains(stderr, "recording the env file") {
		t.Fatalf("unwritable: exit %d, stderr %q", code, stderr)
	}
	// An empty --env-file writes none.
	if code, _, stderr := runCLI("run", "-f", c, "--result-file", result, "--env-file", ""); code != ExitOK {
		t.Fatalf("empty: exit %d, stderr %q", code, stderr)
	}
}

// The env file holds no values, only unset or a sha256 per name, and its
// env lines are exactly the env section aval explain prints for the same
// environment (golden).
func TestEnvFileMatchesExplain(t *testing.T) {
	c := envContract(t)
	_, _, env := splitRun(t, c)
	raw, err := os.ReadFile(env)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "s3cret-value") || strings.Contains(string(raw), "local") {
		t.Fatalf("env file holds a value: %q", raw)
	}
	line := regexp.MustCompile(`^env [A-Z_]+ (unset|[0-9a-f]{64})$`)
	got := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if got[0] != "aval-fingerprint/v2" {
		t.Fatalf("version line %q", got[0])
	}
	for _, l := range got[1:] {
		if !line.MatchString(l) {
			t.Fatalf("env file line %q", l)
		}
	}
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("MODE", "s3cret-value")
	code, stdout, stderr := runCLI("explain", "-f", c)
	if code != ExitOK {
		t.Fatalf("explain: exit %d, stderr %q", code, stderr)
	}
	var want []string
	for l := range strings.Lines(stdout) {
		if strings.HasPrefix(l, "env ") {
			want = append(want, strings.TrimSuffix(l, "\n"))
		}
	}
	if strings.Join(got[1:], "\n") != strings.Join(want, "\n") || len(want) != 2 {
		t.Fatalf("env file lines %q, explain env section %q", got[1:], want)
	}
}
