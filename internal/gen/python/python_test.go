package python

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/fingerprint"
	"github.com/TrianaLab/aval/internal/gen"
)

var update = flag.Bool("update", false, "rewrite testdata/golden.yaml")

func needPython(t *testing.T) {
	t.Helper()
	if _, err := interpreter(); err != nil {
		t.Fatalf("aval gen python tests need Python 3.11 or later on PATH: %v", err)
	}
}

// fixture copies testdata/repo to a temporary directory.
func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/repo")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func write(t *testing.T, root, file, data string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(file))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// generate runs the adapter on root and rewrites root/aval.yaml with its units.
func generate(t *testing.T, a *Adapter, root string) string {
	t.Helper()
	units, err := a.Units(root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "aval.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := gen.Rewrite(data, "python", units)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestGolden(t *testing.T) {
	needPython(t)
	root := fixture(t)
	// Never inputs: a virtual environment, caches, hidden directories.
	write(t, root, "venv/pyvenv.cfg", "home = /usr\n")
	write(t, root, "venv/lib/site.py", "this is not python")
	write(t, root, ".venv/lib/x.py", "nor this")
	write(t, root, "src/acme/__pycache__/x.py", "nor this")
	write(t, root, "web/node_modules/gyp/x.py", "print 'python 2'")
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := generate(t, &Adapter{}, root)
	if *update {
		if err := os.WriteFile("testdata/golden.yaml", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/golden.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("output differs from testdata/golden.yaml (go test -update):\n%s", gen.Diff("golden.yaml", "got", want, []byte(got)))
	}
	if _, err := contract.ParseAll([]byte(got)); err != nil {
		t.Fatal(err)
	}
	if again := generate(t, &Adapter{}, root); again != got {
		t.Error("a second run gave another file")
	}
}

func TestUnits(t *testing.T) {
	needPython(t)
	units, err := (&Adapter{}).Units("testdata/repo")
	if err != nil {
		t.Fatal(err)
	}
	deps := map[string][]string{}
	for _, u := range units {
		deps[u.ID] = u.Deps
	}
	want := map[string][]string{
		"acme":       {"acme-api"},  // declared and imported
		"acme-api":   {"acme-core"}, // declared as acme.core, PEP 503 normalized
		"acme-cli":   {"acme-api"},  // imported only
		"acme-core":  nil,
		"lint-rules": nil, // poetry, its dependencies are external
	}
	for id, d := range want {
		if got, ok := deps[id]; !ok || !slices.Equal(got, d) {
			t.Errorf("%s deps %v, want %v", id, got, d)
		}
	}
	if len(units) != len(want) {
		t.Errorf("%d units, want %d", len(units), len(want))
	}
}

func TestCommandWithoutUV(t *testing.T) {
	needPython(t)
	root := t.TempDir()
	write(t, root, "pyproject.toml", "[project]\nname = \"solo\"\n")
	units, err := (&Adapter{}).Units(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(units[0].Command, " "); got != "python -m pytest ." || !slices.Equal(units[0].Source, []string{"pyproject.toml"}) {
		t.Errorf("command %q, source %v", got, units[0].Source)
	}
}

func TestStrictImports(t *testing.T) {
	needPython(t)
	if _, err := (&Adapter{StrictImports: true}).Units("testdata/repo"); err == nil || err.Error() != "packages/cli/acme_cli/__init__.py:6: a dynamic import aval gen cannot resolve (--strict-imports)" {
		t.Fatalf("err %v", err)
	}
	// A string literal resolves like an import statement.
	root := t.TempDir()
	write(t, root, "a/pyproject.toml", "[project]\nname = \"a\"\n")
	write(t, root, "a/a.py", "import importlib\nimportlib.import_module('b.x')\n__import__('.rel')\n")
	write(t, root, "b/pyproject.toml", "[project]\nname = \"b\"\n")
	write(t, root, "b/b/__init__.py", "")
	units, err := (&Adapter{StrictImports: true}).Units(root)
	if err != nil || !slices.Equal(units[0].Deps, []string{"b"}) {
		t.Fatalf("units %v, err %v", units, err)
	}
}

func TestFlags(t *testing.T) {
	a := &Adapter{}
	fs := pflag.NewFlagSet("python", pflag.ContinueOnError)
	a.Flags(fs)
	if err := fs.Parse([]string{"--strict-imports"}); err != nil || !a.StrictImports {
		t.Fatalf("err %v, strict %v", err, a.StrictImports)
	}
}

// digest fingerprints the generated test name in set over root.
func digest(t *testing.T, set, root, name string) string {
	t.Helper()
	cs, err := contract.ParseAll([]byte(set))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Metadata.Name == name {
			fp, err := fingerprint.ComputeFS(os.DirFS(root), c, fingerprint.Env{})
			if err != nil {
				t.Fatal(err)
			}
			return fp.Digest
		}
	}
	t.Fatalf("no test %s", name)
	return ""
}

func TestFalseHitTransitiveDependency(t *testing.T) {
	needPython(t)
	root := fixture(t)
	set := generate(t, &Adapter{}, root)
	before := digest(t, set, root, "python.acme-cli")
	// acme-cli imports acme-api, which depends on acme-core.
	write(t, root, "packages/core/src/acme_core/__init__.py", "def answer():\n    return 43\n")
	if digest(t, set, root, "python.acme-cli") == before {
		t.Error("python.acme-cli reused after its transitive dependency acme-core changed")
	}
	write(t, root, "packages/core/src/acme_core/__init__.py", "def answer():\n    return 42\n")
	write(t, root, "tools/lint/lint_rules.py", "import os\n") // an unrelated unit
	if digest(t, set, root, "python.acme-cli") != before {
		t.Error("python.acme-cli changed after the unrelated unit lint-rules changed")
	}
	// The root project owns its files, not those of the nested projects.
	acme := digest(t, set, root, "python.acme")
	write(t, root, "packages/cli/acme_cli/__init__.py", "x = 1\n")
	if digest(t, set, root, "python.acme") != acme {
		t.Error("python.acme changed after acme-cli, which it does not depend on, changed")
	}
	// A new file in a new directory of a leaf project is in its scope.
	cli := digest(t, set, root, "python.acme-cli")
	write(t, root, "packages/cli/tests/test_new.py", "def test_new():\n    assert False\n")
	if digest(t, set, root, "python.acme-cli") == cli {
		t.Error("python.acme-cli reused after a new test file was added to it")
	}
}

// A new local import is drift: the generated file differs and the diff
// names the new fragment.
func TestFalseHitNewImportIsDrift(t *testing.T) {
	needPython(t)
	root := fixture(t)
	set := generate(t, &Adapter{}, root)
	if err := os.WriteFile(filepath.Join(root, "aval.yaml"), []byte(set), 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, root, "packages/cli/acme_cli/lint.py", "from lint_rules import check\n")
	now := generate(t, &Adapter{}, root)
	if d := gen.Diff("aval.yaml", "generated", []byte(set), []byte(now)); !strings.Contains(d, "+      - python.lint-rules") {
		t.Errorf("diff does not show python.lint-rules joining python.acme-cli:\n%s", d)
	}
}

func TestFailClosed(t *testing.T) {
	needPython(t)
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"no project", map[string]string{"pyproject.toml": "[tool.uv.workspace]\nmembers = []\n"}, "no pyproject.toml with a project name"},
		{"bad toml", map[string]string{"pyproject.toml": "[project\n"}, "python: pyproject.toml: "},
		{"bad type", map[string]string{"pyproject.toml": "[project]\nname = \"a\"\ndependencies = \"b\"\n"}, "pyproject.toml: json: cannot unmarshal"},
		{"bad name", map[string]string{"pyproject.toml": "[project]\nname = \"a b\"\n"}, `pyproject.toml: invalid project name "a b"`},
		{"syntax error", map[string]string{"pyproject.toml": "[project]\nname = \"a\"\n", "a.py": "def (\n"}, "python: a.py: invalid syntax"},
	} {
		root := t.TempDir()
		for f, data := range tc.files {
			write(t, root, f, data)
		}
		if _, err := (&Adapter{}).Units(root); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err %v, want %q", tc.name, err, tc.want)
		}
	}
	if runtime.GOOS != "windows" { // an unreadable manifest, an unreadable directory
		root := t.TempDir()
		write(t, root, "pyproject.toml", "[project]\nname = \"a\"\n")
		_ = os.Chmod(filepath.Join(root, "pyproject.toml"), 0)
		if _, err := (&Adapter{}).Units(root); err == nil || !strings.Contains(err.Error(), "python: pyproject.toml: ") {
			t.Errorf("unreadable manifest: err %v", err)
		}
		write(t, root, "sub/x", "")
		_ = os.Chmod(filepath.Join(root, "sub"), 0)
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "sub"), 0o755) })
		if _, err := (&Adapter{}).Units(root); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("unreadable directory: err %v", err)
		}
	}
}

func TestPythonFails(t *testing.T) {
	root := t.TempDir()
	write(t, root, "pyproject.toml", "[project]\nname = \"a\"\n")
	t.Setenv("PATH", t.TempDir())
	if _, err := (&Adapter{}).Units(root); err == nil || !strings.Contains(err.Error(), "python3 not found on PATH") {
		t.Errorf("no python: err %v", err)
	}
}

func TestPython310(t *testing.T) {
	py, err := interpreter()
	if err != nil {
		t.Fatalf("aval gen python tests need Python 3.11 or later on PATH: %v", err)
	}
	// facts.py refuses an interpreter without tomllib.
	out, _ := exec.Command(py, "-I", "-c", strings.Replace(factsPy, "(3, 11)", "(99, 0)", 1)).CombinedOutput()
	if !strings.Contains(string(out), "needs Python 3.11 or later") {
		t.Errorf("output %q", out)
	}
}

// Paths named with glob metacharacters get escaped globs, so a change to
// those files changes the generated test's fingerprint; a name no contract
// glob can express fails closed.
func TestFalseHitMetacharacterPaths(t *testing.T) {
	needPython(t)
	root := t.TempDir()
	for f, data := range map[string]string{
		"aval.yaml":                "apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults:\n  cacheability: hermetic\n",
		"pyproject.toml":           "[project]\nname = \"top\"\n",
		"x[1].py":                  "x = 1\n",
		"x1.py":                    "x = 1\n",
		"d*/m.py":                  "m = 1\n",
		"libs/a[1]/pyproject.toml": "[project]\nname = \"a\"\n",
		"libs/a[1]/a.py":           "a = 1\n",
		"libs/a?/pyproject.toml":   "[project]\nname = \"b\"\n",
		"libs/a?/b.py":             "b = 1\n",
		"libs/a1/pyproject.toml":   "[project]\nname = \"c\"\n",
		"libs/a1/c.py":             "c = 1\n",
	} {
		write(t, root, f, data)
	}
	set := generate(t, &Adapter{}, root)
	for _, g := range []string{"x[[]1].py", "d[*]/**", "libs/a[[]1]/**", "libs/a[?]/**"} {
		if !strings.Contains(set, g) {
			t.Fatalf("no escaped glob %s:\n%s", g, set)
		}
	}
	for _, tc := range []struct{ file, test string }{
		{"x[1].py", "python.top"}, {"d*/m.py", "python.top"},
		{"libs/a[1]/a.py", "python.a"}, {"libs/a?/b.py", "python.b"},
	} {
		before, other := digest(t, set, root, tc.test), digest(t, set, root, "python.c")
		write(t, root, tc.file, "changed = 1\n")
		if digest(t, set, root, tc.test) == before || digest(t, set, root, "python.c") != other {
			t.Errorf("%s changed: want only %s to change", tc.file, tc.test)
		}
	}
	if runtime.GOOS == "windows" { // no backslash in a file name
		return
	}
	write(t, root, `a\b.txt`, "")
	if _, err := (&Adapter{}).Units(root); err == nil || !strings.Contains(err.Error(), `project top: "a\\b.txt": a contract glob cannot express`) {
		t.Errorf("error %v; want one naming a\\b.txt", err)
	}
}
