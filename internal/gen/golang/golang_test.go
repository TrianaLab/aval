package golang

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"golang.org/x/mod/modfile"

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
	shared, err := a.Shared(root)
	if err != nil {
		t.Fatal(err)
	}
	out, err := gen.Rewrite(data, "go", units, shared)
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
		{"fixtures/order.json", "go.internal.orders", true},       // read by a test through ../../fixtures
		{"fixtures/order.json", "go.shop", true},                  // a dependent of orders
		{"fixtures/order.json", "go.internal.money", false},       // a dependency of orders
		{"fixtures/order.json", "go.web", false},                  // unrelated
		{"fixtures/pages/home.html", "go.web", true},              // under a directory a test reads
		{"fixtures/pages/home.html", "go.internal.orders", false}, // unrelated
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

// A file added later under a directory a test reads is in scope without
// regenerating.
func TestFalseHitNewFileInReadDirectory(t *testing.T) {
	root := shop(t)
	set := generate(t, &Adapter{}, root)
	before := digest(t, set, root, "go.web")
	write(t, filepath.Join(root, "fixtures/pages/new.html"), "<p>new</p>\n")
	if digest(t, set, root, "go.web") == before {
		t.Error("fixtures/pages/new.html added but the fingerprint of go.web did not change")
	}
}

// A read the adapter cannot resolve, that may leave the package directory,
// fails closed and names the file, the line and the escape hatch;
// --go-allow-dynamic accepts it, and a pattern that matches no package is
// an error.
func TestDynamicRead(t *testing.T) {
	root := shop(t)
	write(t, filepath.Join(root, "web/dynamic_test.go"), "package web\n\nimport (\n\t\"os\"\n\t\"path/filepath\"\n\t\"testing\"\n)\n\nfunc TestDynamic(t *testing.T) {\n\tname := t.Name()\n\t_, _ = os.ReadFile(filepath.Join(\"../fixtures\", name))\n}\n")
	_, err := (&Adapter{}).Units(root)
	if want := "package example.com/shop/web: web/dynamic_test.go:11: os.ReadFile reads a path that it cannot resolve and that may leave the package directory; declare what it reads by hand and pass --go-allow-dynamic web"; err == nil || err.Error() != want {
		t.Fatalf("a dynamic read: %v\nwant %s", err, want)
	}
	a := &Adapter{}
	fs := pflag.NewFlagSet("go", pflag.ContinueOnError)
	a.Flags(fs)
	if err := fs.Parse([]string{"--go-allow-dynamic", "w*"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Units(root); err != nil {
		t.Errorf("--go-allow-dynamic w*: %v", err)
	}
	for _, tc := range []struct{ pattern, want string }{
		{"nope/**", "--go-allow-dynamic nope/**: matches no package"},
		{"/abs", "--go-allow-dynamic: "},
	} {
		if _, err := (&Adapter{AllowDynamic: []string{tc.pattern}}).Units(root); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("--go-allow-dynamic %s: %v, want %q", tc.pattern, err, tc.want)
		}
	}
}

// A read whose path comes from a closure parameter, another package or a
// Chdir to a computed directory may read fixtures/order.json: each fails
// closed instead of leaving it out of the scope (a false hit on edit).
func TestFalseHitUnresolvedRead(t *testing.T) {
	for _, body := range []string{
		"read := func(p string) { _, _ = os.ReadFile(p) }\n\tread(\"../fixtures/order.json\")",
		"_, _ = os.ReadFile(filepath.Join(testutil.Root(), \"fixtures\", \"order.json\"))",
		"wd, _ := os.Getwd()\n\tt.Chdir(filepath.Dir(wd))\n\t_, _ = os.ReadFile(\"fixtures/order.json\")",
	} {
		root := shop(t)
		write(t, filepath.Join(root, "web/unresolved_test.go"), "package web\n\nimport (\n\t\"os\"\n\t\"path/filepath\"\n\t\"testing\"\n\n\t\"example.com/shop/internal/testutil\"\n)\n\nvar _ = filepath.Join\nvar _ = testutil.Equal\n\nfunc TestUnresolved(t *testing.T) {\n\t"+body+"\n}\n")
		if _, err := (&Adapter{}).Units(root); err == nil || !strings.Contains(err.Error(), "web/unresolved_test.go:") || !strings.Contains(err.Error(), "cannot resolve") {
			t.Errorf("%s: %v, want an error naming web/unresolved_test.go", body, err)
		}
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
	if _, err := (&Adapter{}).unit(dir, pkg{Dir: ".", GoFiles: []string{"a.go"}}, nil, nil, false); err == nil || !strings.Contains(err.Error(), "a.go:3:10") {
		t.Errorf("a file that does not parse: %v", err)
	}
	if _, err := (&Adapter{}).unit(dir, pkg{Dir: ".", GoFiles: []string{"gone.go"}}, nil, nil, false); err == nil || !strings.Contains(err.Error(), "gone.go") {
		t.Errorf("a file that cannot be read: %v", err)
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

// go.module: a change to go.mod (its go or toolchain directive included),
// go.sum, go.work or go.work.sum changes the fingerprint of every
// generated test, and a version bump is drift that --check reports.
func TestFalseHitShared(t *testing.T) {
	root := shop(t)
	write(t, filepath.Join(root, "go.sum"), "example.com/x v1.0.0 h1:x=\n")
	set := generate(t, &Adapter{}, root)
	if !strings.Contains(set, "        - go.sum\n") {
		t.Fatalf("go.sum is not in go.module:\n%s", set)
	}
	tests := []string{"go.internal.money", "go.internal.orders", "go.internal.testutil", "go.shop", "go.web"}
	for _, tc := range []struct{ file, data, drift string }{
		{"go.mod", "module example.com/shop\n\ngo 1.23\n", `{go: "1.23"}`},
		{"go.mod", "module example.com/shop\n\ngo 1.22\n\ntoolchain go1.22.5\n", `{go: 1.22.5}`},
		{"go.sum", "example.com/x v1.0.1 h1:y=\n", ""},
	} {
		file := filepath.Join(root, tc.file)
		old, _ := os.ReadFile(file)
		before := map[string]string{}
		for _, n := range tests {
			before[n] = digest(t, set, root, n)
		}
		write(t, file, tc.data)
		for _, n := range tests {
			if digest(t, set, root, n) == before[n] {
				t.Errorf("%s changed to %q but the fingerprint of %s did not", tc.file, tc.data, n)
			}
		}
		if now := generate(t, &Adapter{}, root); tc.drift != "" && !strings.Contains(gen.Diff("old", "new", []byte(set), []byte(now)), "+      toolchain: "+tc.drift) {
			t.Errorf("%s: the version bump is not drift:\n%s", tc.file, gen.Diff("old", "new", []byte(set), []byte(now)))
		}
		write(t, file, string(old))
	}
}

// In a workspace go.module takes the version from go.work and lists
// go.work, go.work.sum and each module's go.mod and go.sum.
func TestFalseHitSharedWorkspace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ws")
	write(t, filepath.Join(root, "go.work"), "go 1.22\n\ntoolchain go1.22.5\n\nuse (\n\t.\n\t./a\n\t./b\n)\n")
	write(t, filepath.Join(root, "go.mod"), "module example.com/ws\n\ngo 1.22\n")
	write(t, filepath.Join(root, "ws.go"), "package ws\n\nimport _ \"example.com/a\"\n")
	write(t, filepath.Join(root, "go.work.sum"), "example.com/x v1.0.0 h1:x=\n")
	write(t, filepath.Join(root, "a/go.mod"), "module example.com/a\n\ngo 1.22\n")
	write(t, filepath.Join(root, "a/go.sum"), "example.com/x v1.0.0 h1:x=\n")
	write(t, filepath.Join(root, "a/a.go"), "package a\n\nimport _ \"example.com/b\"\n")
	write(t, filepath.Join(root, "b/go.mod"), "module example.com/b\n\ngo 1.21\n")
	write(t, filepath.Join(root, "b/b.go"), "package b\n")
	write(t, filepath.Join(root, "aval.yaml"), "apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults:\n  cacheability: hermetic\n")
	s, err := (&Adapter{}).Shared(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"go.work", "go.work.sum", "go.mod", "a/go.mod", "a/go.sum", "b/go.mod"}; !slices.Equal(s.Dependencies, want) || s.Toolchain["go"] != "1.22.5" {
		t.Fatalf("dependencies %q, toolchain %q", s.Dependencies, s.Toolchain)
	}
	set := generate(t, &Adapter{}, root)
	for _, file := range []string{"go.work", "go.work.sum", "a/go.mod", "a/go.sum", "b/go.mod"} {
		before := digest(t, set, root, "go.a")
		f := filepath.Join(root, filepath.FromSlash(file))
		old, _ := os.ReadFile(f)
		write(t, f, string(old)+"// changed\n")
		if digest(t, set, root, "go.a") == before {
			t.Errorf("%s changed but the fingerprint of go.a did not", file)
		}
		write(t, f, string(old))
	}
}

func TestGoVersion(t *testing.T) {
	for _, tc := range []struct {
		g    *modfile.Go
		tc   *modfile.Toolchain
		want string
	}{
		{nil, nil, ""},
		{&modfile.Go{Version: "1.22"}, nil, "1.22"},
		{&modfile.Go{Version: "1.22"}, &modfile.Toolchain{Name: "go1.22.5"}, "1.22.5"},
		{&modfile.Go{Version: "1.22"}, &modfile.Toolchain{Name: "default"}, "1.22"},
	} {
		if got := goVersion(tc.g, tc.tc); got != tc.want {
			t.Errorf("goVersion(%v, %v) = %q, want %q", tc.g, tc.tc, got, tc.want)
		}
	}
}

// Shared fails closed: no version, an unreadable file, a file outside the
// root or a path no glob can express is an error, never a guess.
func TestSharedErrors(t *testing.T) {
	noVersion := t.TempDir()
	write(t, filepath.Join(noVersion, "go.mod"), "module example.com/x\n")
	badMod := t.TempDir()
	write(t, filepath.Join(badMod, "go.mod"), "module example.com/x\n\ngo 1.22\n\nbogus\n")
	badWork := t.TempDir()
	write(t, filepath.Join(badWork, "go.work"), "go 1.22\n\nbogus\n")
	outside := filepath.Join(t.TempDir(), "ws")
	write(t, filepath.Join(outside, "root/go.work"), "go 1.22\n\nuse ../other\n")
	write(t, filepath.Join(outside, "other/go.mod"), "module example.com/other\n\ngo 1.22\n")
	for _, tc := range []struct{ root, want string }{
		{filepath.Join(t.TempDir(), "none"), "no such file"},
		{t.TempDir(), "no go.mod or go.work"},
		{noVersion, "cannot read the Go version"},
		{badMod, "unknown directive: bogus"},
		{badWork, "unknown directive: bogus"},
		{filepath.Join(outside, "root"), "is outside the contract root"},
	} {
		if s, err := (&Adapter{}).Shared(tc.root); err == nil || !strings.Contains(err.Error(), tc.want) || s != nil {
			t.Errorf("%s: shared %v, error %v; want %q", tc.root, s, err, tc.want)
		}
	}

	t.Setenv("PATH", t.TempDir())
	if _, err := (&Adapter{}).Shared(fixture); err == nil || !strings.Contains(err.Error(), "go env: ") {
		t.Errorf("no go on PATH: %v", err)
	}
	if runtime.GOOS == "windows" {
		return // the fake go below is a POSIX script
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir()) // go reports canonical paths
	write(t, filepath.Join(dir, `a\b/go.mod`), "module example.com/x\n\ngo 1.22\n")
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	for _, tc := range []struct{ out, want string }{
		{"{", "go env: unexpected end of JSON input"},
		{`{"GOWORK": "/nonexistent/go.work"}`, "no such file"},
		{`{"GOMOD": "/nonexistent/go.mod"}`, "no such file"},
		{`{"GOMOD": "` + dir + `/a\\b/go.mod"}`, "cannot express"},
	} {
		write(t, filepath.Join(bin, "go"), "#!/bin/sh\nprintf '%s\\n' '"+tc.out+"'\n")
		_ = os.Chmod(filepath.Join(bin, "go"), 0o755)
		if _, err := (&Adapter{}).Shared(dir); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("go env printing %s: %v; want %q", tc.out, err, tc.want)
		}
	}
}
