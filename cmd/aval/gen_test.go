package main

import (
	"errors"
	"os"
	"os/exec"
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

func TestGenNoTest(t *testing.T) {
	file, _ := genFile(t)
	withAdapter(t, "fake", fakeAdapter{units: fakeUnits("b")})
	if code, _, errOut := runCLI("gen", "fake", "-f", file, "--no-test", "b"); code != ExitOK {
		t.Fatalf("--no-test b: exit %d, stderr %q", code, errOut)
	}
	data, _ := os.ReadFile(file)
	if s := string(data); strings.Contains(s, "{name: fake.b}") || !strings.Contains(s, "  fake.b:\n") || !strings.Contains(s, "      - fake.b\n") {
		t.Fatalf("fake.b: want its fragment used by fake.a and no test:\n%s", s)
	}
	// --check compares the output of the same flags.
	if code, out, _ := runCLI("gen", "fake", "-f", file, "--check", "--no-test", "b"); code != ExitOK || !strings.Contains(out, "up to date") {
		t.Errorf("--check --no-test b: exit %d, stdout %q", code, out)
	}
	if code, out, _ := runCLI("gen", "fake", "-f", file, "--check"); code != ExitError || !strings.Contains(out, "+  - metadata: {name: fake.b}") {
		t.Errorf("--check without --no-test: exit %d, stdout %q", code, out)
	}
	// A pattern that matches no unit is a usage error; nothing is written.
	for _, args := range [][]string{{"--no-test", "c"}, {"--no-test", "b", "--no-test", "a*/x"}, {"--check", "--no-test", "c"}} {
		code, _, errOut := runCLI(append([]string{"gen", "fake", "-f", file}, args...)...)
		if after, _ := os.ReadFile(file); code != ExitUsage || !strings.Contains(errOut, "matches no unit") || string(after) != string(data) {
			t.Errorf("%v: exit %d, stderr %q", args, code, errOut)
		}
	}
}

// --list-tests prints what gen writes a test for under the same --no-test
// patterns, writes nothing, and fails like gen on a pattern that matches
// no unit.
func TestGenListTests(t *testing.T) {
	file, _ := genFile(t)
	withAdapter(t, "fake", fakeAdapter{units: fakeUnits("b")})
	for _, tc := range []struct {
		args []string
		out  string
	}{
		{nil, "a\nb\n"},
		{[]string{"--no-test", "b"}, "a\n"},
		{[]string{"--no-test", "*"}, ""},
	} {
		code, out, errOut := runCLI(append([]string{"gen", "fake", "-f", file, "--list-tests"}, tc.args...)...)
		if code != ExitOK || out != tc.out || errOut != "" {
			t.Errorf("%v: exit %d, stdout %q, stderr %q; want %q", tc.args, code, out, errOut, tc.out)
		}
	}
	if data, _ := os.ReadFile(file); string(data) != genSet {
		t.Fatal("--list-tests wrote the file")
	}
	withAdapter(t, "cyclic", fakeAdapter{units: fakeUnits("missing")})
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"fake", "--no-test", "c"}, ExitUsage, "--no-test c: matches no unit"},
		{[]string{"fake", "--check"}, ExitUsage, "--list-tests and --check"},
		{[]string{"cyclic"}, ExitError, `unknown dependency "missing"`},
	} {
		code, _, errOut := runCLI(append([]string{"gen", tc.args[0], "-f", file, "--list-tests"}, tc.args[1:]...)...)
		if code != tc.code || !strings.Contains(errOut, tc.want) {
			t.Errorf("%v: exit %d, stderr %q; want %d, %q", tc.args, code, errOut, tc.code, tc.want)
		}
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
		{[]string{"gen", "cobol"}, ExitUsage, `unknown language "cobol" (available: broken, cyclic, dotnet, fake, go, java, js, python, rust)`},
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
	if got := languages(); got != "dotnet, go, java, js, python, rust" {
		t.Errorf("languages() = %q, want dotnet, go, java, js, python, rust", got)
	}
}

// aval gen js on the adapter's fixture is up to date; an unreadable
// manifest exits 1 and writes nothing.
func TestGenJS(t *testing.T) {
	file := filepath.Join("..", "..", "internal", "gen", "js", "testdata", "monorepo", "aval.yaml")
	if code, out, errOut := runCLI("gen", "js", "-f", file, "--check"); code != ExitOK || !strings.Contains(out, "up to date") {
		t.Fatalf("fixture: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	dir := t.TempDir()
	set := filepath.Join(dir, "aval.yaml")
	plain := strings.Replace(genSet, "  scope: {root: ..}\n", "", 1)
	_ = os.WriteFile(set, []byte(plain), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte("{}"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte("{"), 0o644)
	code, _, errOut := runCLI("gen", "js", "-f", set)
	if data, _ := os.ReadFile(set); code != ExitError || !strings.Contains(errOut, "package.json: unexpected end of JSON input") || string(data) != plain {
		t.Errorf("broken package.json: exit %d, stderr %q, file %q", code, errOut, data)
	}
}

func TestGenJava(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join("..", "..", "internal", "gen", "java", "testdata", "gradle")
	if err := os.CopyFS(dir, os.DirFS(fixture)); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "aval.yaml")
	if code, _, errOut := runCLI("gen", "java", "-f", file); code != ExitOK {
		t.Fatalf("gen java: exit %d, stderr %q", code, errOut)
	}
	got, _ := os.ReadFile(file)
	want, _ := os.ReadFile(filepath.Join(fixture, "aval.golden.yaml"))
	if string(got) != string(want) {
		t.Errorf("gen java wrote:\n%s", got)
	}
	_ = os.Remove(filepath.Join(dir, "settings.gradle.kts")) // no build left: exit 1, nothing written
	if code, _, errOut := runCLI("gen", "java", "-f", file); code != ExitError || !strings.Contains(errOut, "gen java: ") {
		t.Errorf("no build: exit %d, stderr %q", code, errOut)
	}
	if after, _ := os.ReadFile(file); string(after) != string(got) {
		t.Errorf("no build: the file changed:\n%s", after)
	}
}

func TestGenAdapterFlags(t *testing.T) {
	file, _ := genFile(t)
	withAdapter(t, "fake", fakeAdapter{units: fakeUnits()})
	code, _, errOut := runCLI("gen", "fake", "-f", file, "--strict-imports")
	if code != ExitUsage || !strings.Contains(errOut, "gen fake: --strict-imports is a python flag") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
}

// Without Python, gen python fails (exit 1) and writes nothing.
func TestGenPythonNoTool(t *testing.T) {
	file, dir := genFile(t)
	_ = os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname = \"a\"\n"), 0o644)
	t.Setenv("PATH", t.TempDir())
	code, _, errOut := runCLI("gen", "python", "-f", file, "--strict-imports")
	if data, _ := os.ReadFile(file); code != ExitError || !strings.Contains(errOut, "gen python: python3 not found on PATH") || string(data) != genSet {
		t.Errorf("exit %d, stderr %q, file %q", code, errOut, data)
	}
}

// aval gen rust fails closed: no cargo, or a manifest cargo cannot read,
// exits 1 and leaves the TestSet as it was.
func TestGenRustFailClosed(t *testing.T) {
	cases := map[string]string{"missing tool": "cargo metadata: exec:"}
	if _, err := exec.LookPath("cargo"); err == nil {
		cases["unreadable manifest"] = "cargo metadata: exit status"
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "aval.yaml")
			_ = os.WriteFile(file, []byte("apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults: {cacheability: hermetic}\n"), 0o644)
			_ = os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package\n"), 0o644)
			if name == "missing tool" {
				t.Setenv("PATH", "")
			}
			before, _ := os.ReadFile(file)
			code, _, errOut := runCLI("gen", "rust", "-f", file)
			if after, _ := os.ReadFile(file); code != ExitError || !strings.Contains(errOut, want) || string(after) != string(before) {
				t.Errorf("exit %d, stderr %q, file changed %v; want exit 1 and %q", code, errOut, string(after) != string(before), want)
			}
		})
	}
}

// A [patch] path outside every member, invisible to cargo metadata
// --no-deps, exits 1, names the file and writes nothing.
func TestGenRustPatchOutsideMembers(t *testing.T) {
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo not installed")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "aval.yaml")
	set := "apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults: {cacheability: hermetic}\n"
	_ = os.MkdirAll(filepath.Join(dir, "a", "src"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "a", "src", "lib.rs"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "a", "Cargo.toml"), []byte("[package]\nname = \"a\"\nversion = \"0.1.0\"\nedition = \"2021\"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[workspace]\nmembers = [\"a\"]\nresolver = \"2\"\n[patch.crates-io]\nserde = { path = \"vendor/serde\" }\n"), 0o644)
	_ = os.WriteFile(file, []byte(set), 0o644)
	code, _, errOut := runCLI("gen", "rust", "-f", file)
	if after, _ := os.ReadFile(file); code != ExitError || !strings.Contains(errOut, "Cargo.toml: path override vendor/serde is not inside a workspace member") || string(after) != set {
		t.Errorf("exit %d, stderr %q, file %q", code, errOut, after)
	}
}

// aval gen go writes the golden file of the Go fixture; without go on PATH
// it exits 1 and writes nothing.
func TestGenGo(t *testing.T) {
	const fixture = "../../internal/gen/golang/testdata/shop"
	dir := filepath.Join(t.TempDir(), "shop")
	if err := os.CopyFS(dir, os.DirFS(fixture)); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "aval.yaml")
	before, _ := os.ReadFile(file)
	t.Setenv("PATH", t.TempDir())
	if code, _, errOut := runCLI("gen", "go", "-f", file); code != ExitError || !strings.Contains(errOut, "gen go: go list:") {
		t.Errorf("no go: exit %d, stderr %q", code, errOut)
	}
	if data, _ := os.ReadFile(file); string(data) != string(before) {
		t.Error("written without go")
	}
}

func TestGenGoWrites(t *testing.T) {
	const fixture = "../../internal/gen/golang/testdata/shop"
	dir := filepath.Join(t.TempDir(), "shop")
	if err := os.CopyFS(dir, os.DirFS(fixture)); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "aval.yaml")
	if code, out, errOut := runCLI("gen", "go", "-f", file); code != ExitOK || !strings.Contains(out, "wrote the go entries for 5 units") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	got, _ := os.ReadFile(file)
	want, _ := os.ReadFile(filepath.Join(dir, "aval.golden.yaml"))
	if string(got) != string(want) {
		t.Errorf("written file differs from the golden:\n%s", gen.Diff("golden", "got", want, got))
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

// fakeSharer is a fakeAdapter whose shared fragment fails to read.
type fakeSharer struct{ fakeAdapter }

func (fakeSharer) Shared(string) (*gen.Shared, error) { return nil, errors.New("no version") }

// A shared fragment that cannot be read exits 1 and writes nothing.
func TestGenSharedError(t *testing.T) {
	file, _ := genFile(t)
	withAdapter(t, "fake", fakeSharer{fakeAdapter{units: fakeUnits()}})
	if code, _, errOut := runCLI("gen", "fake", "-f", file); code != ExitError || !strings.Contains(errOut, "gen fake: no version") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
	if data, _ := os.ReadFile(file); string(data) != genSet {
		t.Error("written although the shared fragment failed")
	}
}

// A toolchain bump in go.mod is drift in go.module that --check reports.
func TestGenGoSharedDrift(t *testing.T) {
	const fixture = "../../internal/gen/golang/testdata/shop"
	dir := filepath.Join(t.TempDir(), "shop")
	if err := os.CopyFS(dir, os.DirFS(fixture)); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "aval.yaml")
	if code, _, errOut := runCLI("gen", "go", "-f", file); code != ExitOK {
		t.Fatalf("gen go: exit %d, stderr %q", code, errOut)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/shop\n\ngo 1.22\n\ntoolchain go1.22.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := runCLI("gen", "go", "-f", file, "--check"); code != ExitError || !strings.Contains(out, "+      toolchain: {go: 1.22.5}") {
		t.Errorf("--check after a toolchain bump: exit %d, stdout %q", code, out)
	}
}
