package fingerprint

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/contract"
)

// The env file of a fingerprint gives the same digest in a process with
// other variables, and only the env section comes from it (#247).
func TestComputeRecordedRoundTrip(t *testing.T) {
	f := newFixture(t)
	want, err := f.compute()
	if err != nil {
		t.Fatal(err)
	}
	if got := want.EnvFile(); got != Version+"\nenv DB_IMAGE "+sum([]byte("postgres:16"))+"\nenv MODE unset\n" {
		t.Fatalf("EnvFile() = %q", got)
	}
	env, err := ParseEnvFile([]byte(want.EnvFile()))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DB_IMAGE", "other") // ignored
	got, err := ComputeRecorded(context.Background(), f.dir, f.c, env)
	if err != nil || got.Digest != want.Digest {
		t.Fatalf("recorded: %v %s, want %s", err, got.Digest, want.Digest)
	}
	// False hit: a source file that differs is still hashed from dir.
	f.write("main.go", "package main // changed\n")
	if got, err := ComputeRecorded(context.Background(), f.dir, f.c, env); err != nil || got.Digest == want.Digest {
		t.Fatalf("changed source: %v, digest unchanged", err)
	}
}

// Recorded names must be exactly the sorted, deduplicated environment.env.
func TestComputeRecordedNames(t *testing.T) {
	f := newFixture(t)
	h := strings.Repeat("a", 64)
	for name, env := range map[string][]string{
		"missing": {"env DB_IMAGE " + h},
		"extra":   {"env DB_IMAGE " + h, "env MODE unset", "env X unset"},
		"other":   {"env DB_IMAGE " + h, "env MODF unset"},
		"order":   {"env MODE unset", "env DB_IMAGE " + h},
		"none":    nil,
	} {
		if _, err := ComputeRecorded(context.Background(), f.dir, f.c, env); err == nil || !strings.Contains(err.Error(), "env file names") {
			t.Errorf("%s: %v, want a names error", name, err)
		}
	}
	// Duplicates in environment.env count once; no env and an empty file match.
	f.c.Environment.Env = []string{"MODE", "DB_IMAGE", "MODE"}
	if _, err := ComputeRecorded(context.Background(), f.dir, f.c, []string{"env DB_IMAGE " + h, "env MODE unset"}); err != nil {
		t.Fatal(err)
	}
	f.c.Environment.Env = nil
	want, _ := f.compute()
	if got, err := ComputeRecorded(context.Background(), f.dir, f.c, nil); err != nil || got.Digest != want.Digest {
		t.Fatalf("no env: %v %s, want %s", err, got.Digest, want.Digest)
	}
}

// With a prefix pattern the env file records one line per matched set
// variable, push takes it back to the same digest, and a file that names a
// variable no entry covers, drops an exact name or a matched one, or adds
// an unset pattern line does not give the run's digest (#253, #247).
func TestComputeRecordedPatterns(t *testing.T) {
	f := newFixture(t)
	f.c.Environment.Env = []string{"APP_*", "MODE"}
	f.env = map[string]string{"APP_A": "1", "APP_B": "2", "APPX": "3", "DB_IMAGE": "x"}
	want, err := f.compute()
	if err != nil {
		t.Fatal(err)
	}
	a, b := "env APP_A "+sum([]byte("1")), "env APP_B "+sum([]byte("2"))
	if got := want.EnvFile(); got != Version+"\n"+a+"\n"+b+"\nenv MODE unset\n" {
		t.Fatalf("EnvFile() = %q", got)
	}
	env, err := ParseEnvFile([]byte(want.EnvFile()))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_C", "set only in the publish job") // not hashed: the file is the run's
	if got, err := ComputeRecorded(context.Background(), f.dir, f.c, env); err != nil || got.Digest != want.Digest {
		t.Fatalf("recorded: %v %s, want %s", err, got.Digest, want.Digest)
	}
	h := strings.Repeat("a", 64)
	for name, env := range map[string][]string{
		"name no entry covers":  {a, b, "env APPX " + h, "env MODE unset"},
		"name a pattern misses": {a, b, "env MODE unset", "env OTHER " + h},
		"unset pattern line":    {a, b, "env APP_C unset", "env MODE unset"},
		"exact name missing":    {a, b},
		"unsorted":              {b, a, "env MODE unset"},
		"other case":            {a, b, "env MODE unset", "env app_c " + h},
	} {
		if _, err := ComputeRecorded(context.Background(), f.dir, f.c, env); err == nil || !strings.Contains(err.Error(), "env file names") {
			t.Errorf("%s: %v, want a names error", name, err)
		}
	}
	// False hit: a matched variable left out of the file, or one the run
	// did not have, is accepted by name but changes the digest.
	for name, env := range map[string][]string{
		"matched name dropped": {a, "env MODE unset"},
		"matched name added":   {a, b, "env APP_C " + h, "env MODE unset"},
	} {
		if got, err := ComputeRecorded(context.Background(), f.dir, f.c, env); err != nil || got.Digest == want.Digest {
			t.Errorf("%s: %v, digest unchanged", name, err)
		}
	}
}

// On Windows a pattern line names the variable upper-cased, as envLines
// writes it; a lower-case spelling is a name no entry gives.
func TestRecordedEnvWindows(t *testing.T) {
	defer func(old bool) { foldEnv = old }(foldEnv)
	foldEnv = true
	h := strings.Repeat("a", 64)
	entries := []string{"app_*", "Mode"}
	if _, err := recordedEnv([]string{"env APP_A " + h, "env Mode unset"}, entries); err != nil {
		t.Fatal(err)
	}
	if _, err := recordedEnv([]string{"env Mode unset", "env app_a " + h}, entries); err == nil {
		t.Fatal("lower-case pattern line accepted on Windows")
	}
	// A matching prefix with a mixed-case suffix reaches the envKey guard.
	if _, err := recordedEnv([]string{"env APP_a " + h, "env Mode unset"}, entries); err == nil {
		t.Fatal("mixed-case pattern line accepted on Windows")
	}
}

func TestParseEnvFile(t *testing.T) {
	h := strings.Repeat("0", 64)
	for name, tc := range map[string]struct{ in, want string }{
		"ok":            {Version + "\nenv A unset\nenv B " + h + "\n", ""},
		"only version":  {Version + "\n", ""},
		"no newline":    {Version, "does not end in a newline"},
		"blank line":    {Version + "\n\n", "line 2"},
		"old version":   {"aval-fingerprint/v1\n", "manifest version"},
		"no name":       {Version + "\nenv  unset\n", "line 2"},
		"uppercase hex": {Version + "\nenv A " + strings.Repeat("A", 64) + "\n", "line 2"},
		"long hash":     {Version + "\nenv A " + h + "0\n", "line 2"},
		"odd hex":       {Version + "\nenv A " + h[:63] + "\n", "line 2"},
		"duplicate":     {Version + "\nenv A unset\nenv A " + h + "\n", "A given twice"},
		"toolchain":     {Version + "\ntoolchain go=1.25\n", "line 2"},
		"crlf":          {Version + "\r\nenv A unset\r\n", "manifest version"},
	} {
		_, err := ParseEnvFile([]byte(tc.in))
		if (tc.want == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

// A contract in scope.contracts still hashes environment.env from the
// process: the env file covers the top contract only.
func TestComputeRecordedReferencedContract(t *testing.T) {
	files := monorepoFiles()
	files["shared/aval.yaml"] += "environment: {env: [SHARED]}\n"
	files["frontend/aval.yaml"] += "environment: {env: [TOP]}\n"
	root := writeTree(t, files)
	p := filepath.Join(root, "frontend", "aval.yaml")
	c, err := contract.Load(p, "")
	if err != nil {
		t.Fatal(err)
	}
	digest := func(shared string) string {
		t.Helper()
		t.Setenv("SHARED", shared)
		fp, err := ComputeRecorded(context.Background(), filepath.Dir(p), c, []string{"env TOP unset"})
		if err != nil {
			t.Fatal(err)
		}
		return fp.Digest
	}
	if digest("a") == digest("b") {
		t.Fatal("a referenced contract's environment.env was taken from the env file")
	}
}

// An accepted env file is exactly its version line and env lines, each a
// distinct name with unset or a sha256: nothing else gets through.
func FuzzParseEnvFile(f *testing.F) {
	f.Add([]byte(Version + "\nenv A unset\nenv B " + strings.Repeat("0", 64) + "\n"))
	f.Add([]byte(Version + "\n"))
	f.Add([]byte(Version + "\nenv A local\n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		env, err := ParseEnvFile(raw)
		if err != nil {
			return
		}
		if got := strings.Join(append([]string{Version}, env...), "\n") + "\n"; got != string(raw) {
			t.Fatalf("accepted %q, lines %q", raw, env)
		}
		seen := map[string]bool{}
		for _, l := range env {
			f := strings.Split(l, " ")
			if len(f) != 3 || f[0] != "env" || seen[f[1]] || f[2] != "unset" && len(f[2]) != 64 {
				t.Fatalf("accepted line %q", l)
			}
			seen[f[1]] = true
		}
	})
}
