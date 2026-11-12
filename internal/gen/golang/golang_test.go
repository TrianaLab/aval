package golang

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/fingerprint"
	"github.com/TrianaLab/aval/internal/gen"
)

const fixture = "testdata/shop"

// shop copies the fixture to a temporary directory and returns it.
func shop(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "shop")
	if err := os.CopyFS(dir, os.DirFS(fixture)); err != nil {
		t.Fatal(err)
	}
	return dir
}

// generate runs the adapter on root and rewrites root/aval.yaml with it.
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
	out, err := gen.Rewrite(data, "go", units)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func write(t *testing.T, file, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGolden(t *testing.T) {
	want, err := os.ReadFile(filepath.Join(fixture, "aval.golden.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 { // the same output on every run
		if got := generate(t, &Adapter{}, fixture); got != string(want) {
			t.Fatalf("generated aval.yaml differs from %s/aval.golden.yaml:\n%s", fixture, gen.Diff("golden", "got", want, []byte(got)))
		}
	}
	// A symlinked root (macOS temp dirs) gives the same relative globs.
	link := filepath.Join(t.TempDir(), "link")
	abs, _ := filepath.Abs(fixture)
	if err := os.Symlink(abs, link); err == nil {
		if got := generate(t, &Adapter{}, link); got != string(want) {
			t.Errorf("through a symlink:\n%s", gen.Diff("golden", "got", want, []byte(got)))
		}
	}
}

func TestCover(t *testing.T) {
	a := &Adapter{}
	fs := pflag.NewFlagSet("go", pflag.ContinueOnError)
	a.Flags(fs)
	if err := fs.Parse([]string{"--go-cover"}); err != nil || !a.Cover {
		t.Fatalf("--go-cover: err %v, Cover %v", err, a.Cover)
	}
	units, err := a.Units(fixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range units {
		c := u.Command
		if len(c) != 4 || c[0] != "sh" || c[2] != coverScript || (c[3] != "./"+u.ID && c[3] != ".") {
			t.Errorf("unit %s: command %q", u.ID, c)
		}
	}
}

// digest fingerprints the test name of set over the files under root.
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

// A change to a transitive local dependency re-runs the dependent test,
// through code, test-only imports and embedded files; a change to an
// unrelated unit does not.
func TestFalseHitTransitive(t *testing.T) {
	root := shop(t)
	set := generate(t, &Adapter{}, root)
	for _, tc := range []struct {
		file, test string
		changes    bool
	}{
		{"internal/money/money.go", "go.shop", true},                  // shop -> orders -> money
		{"internal/testutil/testutil.go", "go.internal.orders", true}, // a test-only import
		{"internal/orders/testdata/order.txt", "go.internal.orders", true},
		{"web/static/index.html", "go.web", true},                 // go:embed
		{"internal/money/money.go", "go.web", true},               // imported by a file under a build tag
		{"web/static/index.html", "go.internal.orders", false},    // unrelated
		{"web/web.go", "go.shop", false},                          // unrelated
		{"internal/orders/orders.go", "go.internal.money", false}, // a dependent, not a dependency
	} {
		before := digest(t, set, root, tc.test)
		file := filepath.Join(root, filepath.FromSlash(tc.file))
		old, _ := os.ReadFile(file)
		write(t, file, string(old)+"\n// changed\n")
		if changed := digest(t, set, root, tc.test) != before; changed != tc.changes {
			t.Errorf("%s changed: fingerprint of %s changed = %v, want %v", tc.file, tc.test, changed, tc.changes)
		}
		write(t, file, string(old))
	}
}

// A file embedded only by a file for another OS is still in scope.
func TestFalseHitEmbedOtherOS(t *testing.T) {
	root := shop(t)
	write(t, filepath.Join(root, "web/win.txt"), "windows\n")
	write(t, filepath.Join(root, "web/assets_windows.go"), "package web\n\nimport _ \"embed\"\n\n//go:embed win.txt\nvar win string\n")
	set := generate(t, &Adapter{}, root)
	before := digest(t, set, root, "go.web")
	write(t, filepath.Join(root, "web/win.txt"), "changed\n")
	if digest(t, set, root, "go.web") == before {
		t.Error("web/win.txt changed but the fingerprint of go.web did not")
	}
}

// A file added later under an embedded directory is embedded too, so it
// is in scope without regenerating.
func TestFalseHitNewEmbeddedFile(t *testing.T) {
	root := shop(t)
	set := generate(t, &Adapter{}, root)
	before := digest(t, set, root, "go.web")
	write(t, filepath.Join(root, "web/static/new.css"), "body{}\n")
	if digest(t, set, root, "go.web") == before {
		t.Error("web/static/new.css added but the fingerprint of go.web did not change")
	}
}

// A glob character in a file name is escaped: the scope names that file,
// not a pattern that misses it, and a name a glob cannot express fails
// closed.
func TestFalseHitMetacharacterName(t *testing.T) {
	root := shop(t)
	write(t, filepath.Join(root, "web/a[1].h"), "int a;\n")
	write(t, filepath.Join(root, "web/a1.h"), "int b;\n")
	set := generate(t, &Adapter{}, root)
	if !strings.Contains(set, "web/a[[]1].h") {
		t.Fatalf("no escaped glob for web/a[1].h in:\n%s", set)
	}
	before := digest(t, set, root, "go.web")
	write(t, filepath.Join(root, "web/a[1].h"), "int c;\n")
	if digest(t, set, root, "go.web") == before {
		t.Error("web/a[1].h changed but the fingerprint of go.web did not")
	}
	if runtime.GOOS == "windows" {
		return // a backslash is a separator there
	}
	write(t, filepath.Join(root, "web/b\\c.h"), "int d;\n")
	if _, err := (&Adapter{}).Units(root); err == nil || !strings.Contains(err.Error(), "cannot express") {
		t.Errorf("a backslash in a file name: err = %v", err)
	}
}

// A new local import, even in a file under a build tag, is drift that
// aval gen go --check reports.
func TestFalseHitNewImportIsDrift(t *testing.T) {
	root := shop(t)
	want, _ := os.ReadFile(filepath.Join(root, "aval.golden.yaml"))
	write(t, filepath.Join(root, "internal/money/web_e2e_test.go"), "//go:build e2e\n\npackage money\n\nimport _ \"example.com/shop/web\"\n")
	got := generate(t, &Adapter{}, root)
	if d := gen.Diff("golden", "got", want, []byte(got)); !strings.Contains(d, "+      - go.web") {
		t.Errorf("the new import money -> web is not in the diff:\n%s", d)
	}
}

func TestErrors(t *testing.T) {
	// A module whose dependency is replaced by a local directory.
	replaced := shop(t)
	write(t, filepath.Join(replaced, "go.mod"), "module example.com/shop\n\ngo 1.22\n\nrequire example.com/lib v0.0.0\n\nreplace example.com/lib => ./lib\n")
	write(t, filepath.Join(replaced, "lib/go.mod"), "module example.com/lib\n\ngo 1.22\n")
	write(t, filepath.Join(replaced, "lib/lib.go"), "package lib\n")
	write(t, filepath.Join(replaced, "web/lib.go"), "package web\n\nimport _ \"example.com/lib\"\n")

	// A workspace whose root module imports a module outside the root.
	outside := filepath.Join(t.TempDir(), "ws")
	write(t, filepath.Join(outside, "root/go.work"), "go 1.22\n\nuse (\n\t.\n\t../other\n)\n")
	write(t, filepath.Join(outside, "root/go.mod"), "module example.com/root\n\ngo 1.22\n")
	write(t, filepath.Join(outside, "root/root.go"), "package root\n\nimport _ \"example.com/other\"\n")
	write(t, filepath.Join(outside, "other/go.mod"), "module example.com/other\n\ngo 1.22\n")
	write(t, filepath.Join(outside, "other/other.go"), "package other\n")

	// An import of a local package that does not exist, under a build tag.
	gone := shop(t)
	write(t, filepath.Join(gone, "web/gone_e2e_test.go"), "//go:build e2e\n\npackage web\n\nimport _ \"example.com/shop/gone\"\n")

	// A file under a build tag that does not parse.
	broken := shop(t)
	write(t, filepath.Join(broken, "web/broken_e2e_test.go"), "//go:build e2e\n\npackage web\n\nimport (\n")

	for _, tc := range []struct{ root, want string }{
		{filepath.Join(t.TempDir(), "none"), "no such file"},
		{t.TempDir(), "go list: exit status 1: "},
		{replaced, "module example.com/lib is replaced by a local directory"},
		{filepath.Join(outside, "root"), "is outside the contract root"},
		{gone, "package example.com/shop/web: imports example.com/shop/gone of a main module"},
		{broken, "broken_e2e_test.go:5:10: "}, // from go list or from the parser, depending on go list's cache
	} {
		units, err := (&Adapter{}).Units(tc.root)
		if err == nil || !strings.Contains(err.Error(), tc.want) || units != nil {
			t.Errorf("%s: units %v, error %v; want %q", tc.root, units, err, tc.want)
		}
	}
}

// A file go list accepted (its cache can skip files under a build tag) but
// that does not parse is an error.
func TestParseError(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.go"), "package a\n\nimport (\n")
	if _, err := (&Adapter{}).unit(dir, pkg{Dir: ".", GoFiles: []string{"a.go"}}, nil, nil); err == nil || !strings.Contains(err.Error(), "a.go:3:10") {
		t.Errorf("a file that does not parse: %v", err)
	}
}

func TestToolErrors(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := (&Adapter{}).Units(fixture); err == nil || !strings.Contains(err.Error(), "executable file not found") {
		t.Errorf("no go on PATH: %v", err)
	}
	bin := t.TempDir()
	fake, script := filepath.Join(bin, "go"), "#!/bin/sh\necho '{'\n"
	if runtime.GOOS == "windows" { // exec.LookPath finds go.bat through PATHEXT
		fake, script = filepath.Join(bin, "go.bat"), "@echo {\r\n"
	}
	write(t, fake, script)
	_ = os.Chmod(fake, 0o755)
	t.Setenv("PATH", bin)
	if _, err := (&Adapter{}).Units(fixture); err == nil || !strings.Contains(err.Error(), "go list: unexpected EOF") {
		t.Errorf("truncated go list output: %v", err)
	}
}
