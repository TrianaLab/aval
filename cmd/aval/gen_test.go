package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/gen"
)

// fakeAdapter returns units, or err, and records the root it was given.
type fakeAdapter struct {
	units []gen.Unit
	err   error
	root  *string
}

func (f fakeAdapter) Units(root string) ([]gen.Unit, error) {
	if f.root != nil {
		*f.root = root
	}
	return f.units, f.err
}

func withAdapter(t *testing.T, name string, a gen.Adapter) {
	t.Helper()
	adapters[name] = a
	t.Cleanup(func() { delete(adapters, name) })
}

const genSet = "apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults:\n  scope: {root: ..}\n  cacheability: hermetic\n"

func fakeUnits(deps ...string) []gen.Unit {
	return []gen.Unit{
		{ID: "a", Source: []string{"a/**"}, Deps: deps, Command: []string{"make", "a"}},
		{ID: "b", Source: []string{"b/**"}, Command: []string{"make", "b"}},
	}
}

// genFile writes genSet to <tmp>/sub/aval.yaml and returns its path and <tmp>.
func genFile(t *testing.T) (file, dir string) {
	t.Helper()
	dir = t.TempDir()
	file = filepath.Join(dir, "sub", "aval.yaml")
	_ = os.Mkdir(filepath.Dir(file), 0o755)
	if err := os.WriteFile(file, []byte(genSet), 0o600); err != nil {
		t.Fatal(err)
	}
	return file, dir
}

func TestGen(t *testing.T) {
	file, dir := genFile(t)
	var root string
	withAdapter(t, "fake", fakeAdapter{units: fakeUnits("b"), root: &root})
	code, out, errOut := runCLI("gen", "fake", "-f", file)
	if code != ExitOK || out != file+": wrote the fake entries for 2 units\n" || errOut != "" {
		t.Fatalf("gen: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	if root != dir {
		t.Errorf("adapter root %q, want %q (defaults.scope.root)", root, dir)
	}
	data, _ := os.ReadFile(file)
	if !strings.HasPrefix(string(data), genSet) || !strings.Contains(string(data), "use:\n      - fake.a\n      - fake.b\n") {
		t.Fatalf("written:\n%s", data)
	}
	if info, _ := os.Stat(file); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600 kept", info.Mode().Perm())
	}
	if code, out, _ := runCLI("gen", "fake", "-f", file); code != ExitOK || out != file+": the fake entries are up to date\n" {
		t.Errorf("second run: exit %d, stdout %q", code, out)
	}
}

func TestGenCheck(t *testing.T) {
	file, _ := genFile(t)
	withAdapter(t, "fake", fakeAdapter{units: fakeUnits("b")})
	// Before generating: out of date, exit 1, a diff, nothing written.
	code, out, errOut := runCLI("gen", "fake", "-f", file, "--check")
	if code != ExitError || !strings.Contains(out, "+  - metadata: {name: fake.a}") || !strings.Contains(errOut, "is out of date: run aval gen fake") {
		t.Fatalf("--check: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	if data, _ := os.ReadFile(file); string(data) != genSet {
		t.Fatal("--check wrote the file")
	}
	runCLI("gen", "fake", "-f", file)
	if code, out, _ := runCLI("gen", "fake", "-f", file, "--check"); code != ExitOK || out != file+": the fake entries are up to date\n" {
		t.Errorf("up to date: exit %d, stdout %q", code, out)
	}
	// a no longer depends on b: drift.
	withAdapter(t, "fake", fakeAdapter{units: fakeUnits()})
	if code, out, _ := runCLI("gen", "fake", "-f", file, "--check"); code != ExitError || !strings.Contains(out, "-      - fake.b") {
		t.Errorf("drift: exit %d, stdout %q", code, out)
	}
}

func TestGenErrors(t *testing.T) {
	dir := t.TempDir()
	set := filepath.Join(dir, "aval.yaml")
	_ = os.WriteFile(set, []byte(strings.Replace(genSet, "  scope: {root: ..}\n", "", 1)), 0o644)
	test := filepath.Join(dir, "test.yaml")
	_ = os.WriteFile(test, []byte("kind: Test\n"), 0o644)
	used := filepath.Join(dir, "used.yaml")
	_ = os.WriteFile(used, []byte(genSet+"tests:\n  - metadata: {name: e2e}\n    use: [fake.gone]\n    runner: {command: [x]}\n"), 0o644)
	withAdapter(t, "fake", fakeAdapter{units: fakeUnits()})
	withAdapter(t, "broken", fakeAdapter{err: errors.New("tool not found")})
	withAdapter(t, "cyclic", fakeAdapter{units: fakeUnits("missing")})
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"gen"}, ExitUsage, "accepts 1 arg"},
		{[]string{"gen", "cobol"}, ExitUsage, `unknown language "cobol" (available: broken, cyclic, dotnet, fake)`},
		{[]string{"gen", "fake", "-f", filepath.Join(dir, "none.yaml")}, ExitUsage, "aval init --set"},
		{[]string{"gen", "fake", "-f", test}, ExitUsage, "kind: TestSet"},
		{[]string{"gen", "broken", "-f", set}, ExitError, "gen broken: tool not found"},
		{[]string{"gen", "cyclic", "-f", set}, ExitError, `unknown dependency "missing"`},
		{[]string{"gen", "fake", "-f", used}, ExitUsage, `unknown fragment "fake.gone"`},
	} {
		code, _, errOut := runCLI(tc.args...)
		if code != tc.code || !strings.Contains(errOut, tc.want) {
			t.Errorf("%v: exit %d, stderr %q; want %d, %q", tc.args, code, errOut, tc.code, tc.want)
		}
	}
	if runtime.GOOS != "windows" { // a read-only directory: the write fails, the file stays
		_ = os.Chmod(dir, 0o500)
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		code, _, errOut := runCLI("gen", "fake", "-f", set)
		if before, _ := os.ReadFile(set); code != ExitError || !strings.Contains(errOut, "gen fake:") || strings.Contains(string(before), "fake.a") {
			t.Errorf("read-only: exit %d, stderr %q", code, errOut)
		}
	}
}

func TestGenLanguages(t *testing.T) {
	if got := languages(); got != "dotnet" {
		t.Errorf("languages() = %q", got)
	}
	saved := adapters
	adapters = map[string]gen.Adapter{}
	t.Cleanup(func() { adapters = saved })
	if got := languages(); got != "none yet" {
		t.Errorf("languages() = %q with no adapters", got)
	}
}

// aval gen dotnet fails closed: an unreadable project file exits 1 and
// leaves the TestSet as it was.
func TestGenDotnet(t *testing.T) {
	file, dir := genFile(t)
	if err := os.WriteFile(filepath.Join(dir, "App.csproj"), []byte(`<Project Sdk="Microsoft.NET.Sdk"/>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := runCLI("gen", "dotnet", "-f", file); code != ExitOK || !strings.Contains(out, "wrote the dotnet entries for 1 units") {
		t.Fatalf("gen dotnet: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	written, _ := os.ReadFile(file)
	if !strings.Contains(string(written), "runner: {command: [dotnet, test, App.csproj]}") {
		t.Fatalf("written:\n%s", written)
	}
	if err := os.WriteFile(filepath.Join(dir, "App.csproj"), []byte(`<Project>`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCLI("gen", "dotnet", "-f", file)
	if after, _ := os.ReadFile(file); code != ExitError || !strings.Contains(errOut, "gen dotnet: App.csproj: XML syntax error") || string(after) != string(written) {
		t.Errorf("malformed project: exit %d, stderr %q", code, errOut)
	}
}
