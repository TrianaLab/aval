package golang

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// header imports what the resolver cases use.
const header = `package p

import (
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	iou "io/ioutil"
)

var _, _, _, _, _ = ioutil.ReadFile, iou.ReadFile, path.Join, runtime.Caller, strings.TrimSpace
var _ testing.T
`

// parseSrc parses srcs as the files a_test.go, b.go, ... of one package.
func parseSrc(t testing.TB, srcs ...string) (*token.FileSet, []*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	var files []*ast.File
	for i, src := range srcs {
		name := string(rune('a'+i)) + ".go"
		if i == 0 {
			name = "a_test.go"
		}
		f, err := parser.ParseFile(fset, "pkg/"+name, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%v\n%s", err, src)
		}
		files = append(files, f)
	}
	return fset, files
}

// values returns the value of every read in srcs.
func values(t testing.TB, srcs ...string) []val {
	t.Helper()
	var out []val
	for _, r := range reads(parseSrc(t, srcs...)) {
		out = append(out, r.val)
	}
	return out
}

func TestResolve(t *testing.T) {
	chain := "func chain() {\n\tv0 := \"x\"\n"
	for i := 1; i <= maxDepth; i++ {
		chain += fmt.Sprintf("\tv%d := v%d\n", i, i-1)
	}
	chain += fmt.Sprintf("\tos.ReadFile(v%d)\n}\n", maxDepth)
	for _, tc := range []struct {
		name, src string
		more      []string // other files of the package
		want      val
	}{
		{"literal", `func f() { os.ReadFile("../../x") }`, nil, known("../../x")},
		{"ioutil, renamed", `func f() { iou.ReadFile("../x") }`, nil, known("../x")},
		{"constant and Join", `const d = "../fixtures"
func f() { os.ReadFile(filepath.Join(d, "a.json")) }`, nil, known("../fixtures/a.json")},
		{"constant in another file", `func f() { os.ReadDir(dir + "/sub") }`, []string{"package p\n\nconst dir = \"../data\"\n"}, known("../data/sub")},
		{"variable in another file", `func f() { os.ReadFile(path.Join(dir, "x")) }`, []string{"package p\n\nvar dir = \"../data\"\n"}, known("../data/x")},
		{"concatenation", `func f() { os.ReadFile("../" + "a" + ".txt") }`, nil, known("../a.txt")},
		{"backslashes", `func f() { os.ReadFile(filepath.Join("..\\a", "b")) }`, nil, known("../a/b")},
		{"Clean, Dir, FromSlash, ToSlash, Abs", `func f() {
	os.ReadFile(filepath.Abs(filepath.ToSlash(filepath.FromSlash(filepath.Clean(filepath.Dir("../a/b/c"))))))
}`, nil, known("../a/b")},
		{"conversion", `type name string
func f() { os.ReadFile(string(name("../a"))) }`, nil, known("../a")},
		{"every assignment", `func f() {
	p := "a"
	p = "../b"
	os.ReadFile(p)
}`, nil, known("../b", "a")},
		{"var without value", `func f() {
	var p string
	p = "../b"
	os.ReadFile(p)
}`, nil, known("../b")},
		{"several values", `func f() {
	a, b := "x", "../y"
	os.ReadFile(a)
	os.ReadFile(b)
}`, nil, known("../y")},
		{"range over a slice", `func f() {
	for _, n := range []string{"../a", "../b"} {
		os.ReadFile(n)
	}
}`, nil, known("../a", "../b")},
		{"range over a named slice", `func f() {
	files := []string{"../a", "../b"}
	for _, n := range files {
		os.ReadFile(filepath.Join("..", n))
	}
}`, nil, known("../../a", "../../b")},
		{"range over map keys", `func f() {
	for k := range map[string]int{"../a": 1} {
		os.ReadFile(k)
	}
}`, nil, known("../a")},
		{"range over an empty map", `func f() {
	for k := range map[string]int{} {
		os.ReadFile(k)
	}
}`, nil, known()},
		{"range index", `func f() {
	for i := range []string{"../a"} {
		os.ReadFile(string(rune(i)))
	}
}`, nil, val{}},
		{"range over something else", `func f(s []string) {
	for _, n := range s {
		os.ReadFile(n)
	}
}`, nil, val{cwd: true}},
		{"range over a call with a climbing literal", `func f() {
	for _, n := range strings.Fields("../a ../b") {
		os.ReadFile(n)
	}
}`, nil, val{esc: []string{"../a ../b"}}},
		{"table of structs", `func f() {
	for _, tc := range []struct{ name, file string }{{"a", "../a"}, {name: "b", file: "../b"}, {name: "c"}} {
		os.ReadFile(tc.file)
	}
}`, nil, known("", "../a", "../b")},
		{"table of pointers to a named struct", `type c struct{ file string }
func f() {
	cases := []*c{&c{file: "../a"}, {"../b"}}
	for _, tc := range cases {
		os.ReadFile(tc.file)
	}
}`, nil, known("../a", "../b")},
		{"struct literal", `func f() {
	cfg := struct{ dir string }{dir: "../x"}
	os.ReadDir(cfg.dir)
}`, nil, known("../x")},
		{"field of something else", `func f() {
	cfg := g()
	os.ReadDir(cfg.dir)
}`, nil, val{cwd: true}},
		{"field of a parameter", `func f(cfg struct{ dir string }) { os.ReadDir(cfg.dir) }`, nil, val{cwd: true}},
		{"field of a non-struct", `func f() {
	s := []string{"../a"}
	os.ReadDir(s.dir)
}`, nil, val{esc: []string{"../a"}}},
		{"field through a range over a non-literal", `func f(s []struct{ dir string }) {
	for _, tc := range s {
		os.ReadDir(tc.dir)
	}
}`, nil, val{cwd: true}},
		{"package function result", `func root() string { return "../.." }
func f() { os.ReadFile(filepath.Join(root(), "x")) }`, nil, known("../../x")},
		{"several results", `func root() (string, error) {
	if true {
		return "../a", nil
	}
	return g()
}
func g() (string, error) { return "../b", nil }
func f() {
	r, _ := root()
	os.ReadFile(r)
}`, nil, known("../a", "../b")},
		{"named results", `func root() (dir string) {
	dir = "../a"
	return
}
func f() { os.ReadFile(root()) }`, nil, known("../a")},
		{"result of a function without a body", `func root() string
func f() { os.ReadFile(root()) }`, nil, val{cwd: true}},
		{"result of a function without returns", `func root() string { panic(1) }
func f() { os.ReadFile(root()) }`, nil, val{}},
		{"recursive function", `func up(d string) string {
	if d == "" {
		return up("../a")
	}
	return d
}
func f() { os.ReadFile(up("")) }`, nil, val{esc: []string{"../a"}}},
		{"parameter from every call site", `func read(t *testing.T, name string) { os.ReadFile(filepath.Join("testdata", name)) }
func f(t *testing.T) {
	read(t, "a")
	read(t, "../b")
}`, nil, known("b", "testdata/a")},
		{"parameter of a function used as a value", `func read(name string) { os.ReadFile(name) }
func f() {
	read("a")
	g := read
	g("../b")
}`, nil, val{cwd: true}},
		{"variadic parameter", `func read(parts ...string) { os.ReadFile(filepath.Join(parts...)) }
func f() { read("..", "a") }`, nil, val{esc: []string{".."}}},
		{"variadic call to Join", `func f(parts []string) { os.ReadFile(filepath.Join(parts...)) }`, nil, val{cwd: true}},
		{"Join of a literal and a parameter", `func f(name string) { os.ReadFile(filepath.Join("..", name)) }`, nil, val{esc: []string{".."}, cwd: true}},
		{"op-assignment", `func f() {
	p := "a"
	p += "/../.."
	os.ReadFile(p)
}`, nil, val{esc: []string{"/../.."}}},
		{"loop", `func f() {
	d := "../a/b"
	for d != "." {
		d = filepath.Dir(d)
		os.ReadDir(d)
	}
}`, nil, val{esc: []string{"../a/b"}}},
		{"cycle", `func f() {
	a, b := "x", "y"
	a = b
	b = a
	os.ReadFile(a)
}`, nil, val{}},
		{"this source file", `func f() {
	_, file, _, _ := runtime.Caller(0)
	os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "x"))
}`, nil, known("../../x")},
		{"a caller's source file", `func f() {
	_, file, _, _ := runtime.Caller(1)
	os.ReadFile(file)
}`, nil, val{cwd: true}},
		{"working directory", `func f() {
	wd, _ := os.Getwd()
	os.ReadFile(filepath.Join(wd, "x"))
}`, nil, val{cwd: true}},
		{"executable", `func f() {
	exe, _ := os.Executable()
	os.ReadFile(exe)
}`, nil, val{cwd: true}},
		{"temporary directory", `func f(t *testing.T) { os.ReadFile(filepath.Join(t.TempDir(), "x")) }`, nil, val{}},
		{"device", `func f() { os.Open(os.DevNull) }`, nil, val{}},
		{"climbing literal in a call", `func f() { os.ReadFile(strings.TrimSpace("../x")) }`, nil, val{esc: []string{"../x"}}},
		{"method parameter", `type s struct{}
func (s) f(name string) { os.ReadFile(name) }`, nil, val{cwd: true}},
		{"too many values", `func f() {
	for _, a := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"} {
		for _, b := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"} {
			os.ReadFile(a + b)
		}
	}
}`, nil, val{}},
		{"too many alternatives", `func f(n int) {
	p := ""
	` + strings.Repeat("if n > 0 { p = \"../a\" + \"x\" }\n\tif n > 1 { p = \"b\" }\n\t", 40) + `
	for range n { p = p + "1" }
	os.ReadFile(p)
}`, nil, val{esc: []string{"../ax"}}},
		{"too deep", chain, nil, val{cwd: true}},
		{"non-string literal", `func f() { os.ReadFile(1) }`, nil, val{}},
		{"glob", `func f() { filepath.Glob("../a/*.txt") }`, nil, known("../a/*.txt")},
		{"other call", `func f() { os.Remove("../a") }`, nil, nil_},
		{"method of a call", `func f() { g().ReadFile("../a") }`, nil, nil_},
		{"read twice", `func f() {
	d := "../a"
	os.ReadFile(d)
	os.ReadFile(d)
}`, nil, known("../a")},
		{"too many assignments", "func f() {\n\tp := \"\"\n" + func() string {
			s := ""
			for i := range maxValues + 1 {
				s += fmt.Sprintf("\tp = \"x%d\"\n", i)
			}
			return s
		}() + "\tos.ReadFile(p)\n}", nil, val{}},
		{"package-level variables from a call", `var r, _ = root()
func root() (string, error) { return "../a", nil }
func f() { os.ReadFile(r) }`, nil, known("../a")},
		{"second result through a call", `func g() (error, string) { return nil, "../b" }
func root() (error, string) { return g() }
func f() {
	_, r := root()
	os.ReadFile(r)
}`, nil, known("../b")},
		{"function with a closure", `func root() string {
	f := func() string { return "x" }
	_ = f
	return "../a"
}
func f() { os.ReadFile(root()) }`, nil, known("../a")},
		{"func literal", `func f() { os.ReadFile(func() string { return "../x" }()) }`, nil, val{cwd: true}},
		{"blank parameter", `func read(_ int, name string) { os.ReadFile(name) }
func f() { read(1, "../a") }`, nil, known("../a")},
		{"range keys over a parameter", `func f(m map[string]int) {
	for k := range m {
		os.ReadFile(k)
	}
}`, nil, val{cwd: true}},
		{"literal cycle", `var cs = ds
var ds = cs
func f() {
	for _, c := range cs {
		os.ReadFile(c.file)
	}
}`, nil, val{}},
		{"field of a call", `func f() { os.ReadDir(g().dir) }`, nil, val{cwd: true}},
		{"field of an undefined type", `func f() {
	for _, tc := range []undefined{{file: "../a"}} {
		os.ReadFile(tc.file)
	}
}`, nil, val{esc: []string{"../a"}}},
		{"body of a redeclared function", `func g() {}`, []string{"package p\n\nimport \"os\"\n\nfunc g(name string) {\n\tfor _, tc := range []struct{ f string }{{\"../a\"}} {\n\t\tos.ReadFile(tc.f + name)\n\t}\n}\n"}, val{esc: []string{"../a"}, cwd: true}},
		{"dot import", `func f() { ReadFile("../a") }`, []string{"package p\n\nimport . \"os\"\n\nfunc g() { ReadFile(\"../b\") }\n"}, known("../b")},
		{"dot import shadowed", `func f() {}`, []string{"package p\n\nimport . \"os\"\n\nfunc ReadFile(string) {}\n\nfunc g() { ReadFile(\"../b\") }\n"}, nil_},
		{"t.Chdir", `func f(t *testing.T) { t.Chdir("../..") }`, nil, known("../..")},
		{"os.Chdir back", `func f() {
	orig, _ := os.Getwd()
	os.Chdir(orig)
}`, nil, val{}},
		{"environment", `func f() { os.ReadFile(filepath.Join(os.Getenv("ROOT"), "x")) }`, nil, val{cwd: true}},
		{"written through a pointer", `func set(p *string) { *p = "../b" }
func f() {
	p := "a"
	set(&p)
	os.ReadFile(p)
}`, nil, val{cwd: true}},
		{"element changed", `func f() {
	files := []string{"a"}
	files[0] = "../b"
	for _, n := range files {
		os.ReadFile(n)
	}
}`, nil, val{esc: []string{"../b"}}},
		{"field changed", `func f() {
	c := struct{ dir string }{dir: "a"}
	c.dir = "../b"
	os.ReadDir(c.dir)
}`, nil, val{esc: []string{"../b"}}},
		{"function in two files", `func f() { os.ReadFile(root()) }`, []string{"package p\n\nfunc root() string { return \"../a\" }\n", "package p\n\nfunc root() string\n"}, val{esc: []string{"../a"}, cwd: true}},
		{"no argument", `func f() { os.ReadFile() }`, nil, nil_},
		{"closure parameter", `func f() {
	read := func(p string) { os.ReadFile(p) }
	read("../../fixtures/x.json")
}`, nil, val{cwd: true}},
		{"function of another package", `func f() { os.ReadFile(filepath.Join(runtime.GOROOT(), "x")) }`, nil, val{cwd: true}},
		{"variable of another package", `func f() { os.ReadFile(runtime.GOOS + "/x") }`, nil, val{esc: []string{"/x"}, cwd: true}},
		{"selector of another package in a string call", `func f() { os.ReadFile(strings.TrimSpace(runtime.GOOS)) }`, nil, val{cwd: true}},
		{"method call", `func f(c *config) { os.ReadFile(c.path()) }`, nil, val{cwd: true}},
		{"t.Chdir to an unknown directory", `func f(t *testing.T) {
	wd, _ := os.Getwd()
	t.Chdir(filepath.Dir(filepath.Dir(wd)))
}`, nil, val{cwd: true}},
		{"os.Chdir back to a reassigned variable", `func f() {
	orig, _ := os.Getwd()
	orig = filepath.Dir(orig)
	os.Chdir(orig)
}`, nil, val{cwd: true}},
		{"Chdir to a variable", `func f() {
	d := "../a"
	os.Chdir(d)
}`, nil, known("../a")},
		{"variable never assigned", `func f() {
	var p string
	os.ReadFile(p)
}`, nil, val{}},
		{"test name", `func f(b *testing.B) { os.ReadFile(filepath.Join("testdata", b.Name()+".golden")) }`, nil, val{}},
		{"testing.TB", `func f(tb testing.TB) { os.ReadFile(filepath.Join(tb.TempDir(), "x")) }`, nil, val{}},
		{"other method of a testing variable", `func f(t *testing.T) { os.ReadFile(t.Other()) }`, nil, val{cwd: true}},
		{"temporary files", `func f() {
	d, _ := os.MkdirTemp("", "x")
	tmp, _ := os.CreateTemp(d, "y")
	os.ReadFile(tmp.Name())
	os.ReadFile(filepath.Join(os.TempDir(), "z"))
}`, nil, val{}},
		{"Name of something else", `func f() {
	g, _ := os.Open("a")
	os.ReadFile(g.Name())
}`, nil, val{cwd: true}},
		{"Name of a non-call", `func f(fi os.FileInfo) { os.ReadFile(fi.Name()) }`, nil, val{cwd: true}},
		{"separator", `func f() { os.ReadFile(".." + string(filepath.Separator) + "x" + string(os.PathSeparator) + "y") }`, nil, known("../x/y")},
		{"Name of a selector", `func f() { os.ReadFile(c.f.Name()) }`, nil, val{cwd: true}},
		{"template files", `func f() {}`, []string{"package p\n\nimport \"html/template\"\n\nfunc g() { template.ParseFiles(\"../a.html\", \"../b.html\") }\n"}, known("../b.html")},
		{"template glob", `func f() {}`, []string{"package p\n\nimport \"text/template\"\n\nfunc g() { template.ParseGlob(\"../t/*.html\") }\n"}, known("../t/*.html")},
		{"http.Dir", `func f() {}`, []string{"package p\n\nimport \"net/http\"\n\nfunc g() { http.Dir(\"../static\") }\n"}, known("../static")},
	} {
		got := values(t, append([]string{header + tc.src}, tc.more...)...)
		switch {
		case reflect.DeepEqual(tc.want, nil_):
			if len(got) != 0 {
				t.Errorf("%s: reads %+v, want none", tc.name, got)
			}
		case len(got) == 0:
			t.Errorf("%s: no read found", tc.name)
		case !equal(got[len(got)-1], tc.want):
			t.Errorf("%s: %+v, want %+v", tc.name, got[len(got)-1], tc.want)
		}
	}
}

// nil_ marks a case that has no read.
var nil_ = val{esc: []string{"\x00none"}}

func equal(a, b val) bool {
	return a.known == b.known && a.cwd == b.cwd && slices.Equal(a.ss, b.ss) && slices.Equal(a.esc, b.esc)
}

// A literal whose type the checker did not record has no underlying type.
func TestUnderlyingNil(t *testing.T) {
	if underlying(nil) != nil {
		t.Error("underlying(nil) is not nil")
	}
}

// More than maxValues climbing literals are too many to check one by one:
// the value fails closed.
func TestUnknownTooManyEscapes(t *testing.T) {
	vs := make([]val, maxValues+1)
	for i := range vs {
		vs[i] = known(fmt.Sprintf("../%d", i))
	}
	if v := unknown(vs...); !v.cwd {
		t.Errorf("unknown of %d climbing values: %+v", len(vs), v)
	}
}

// Package and external test package are checked apart, and a name declared
// twice (files for two platforms) unions both values.
func TestResolvePackages(t *testing.T) {
	got := values(t,
		header+`func f() { os.ReadFile(dir) }`,
		"package p\n\nconst dir = \"../linux\"\n",
		"package p\n\nconst dir = \"../windows\"\n",
		"package p_test\n\nimport \"os\"\n\nconst dir = \"../x\"\n\nfunc g() { os.ReadFile(dir) }\n",
	)
	if len(got) != 2 || !equal(got[0], known("../linux", "../windows")) || !equal(got[1], known("../x")) {
		t.Errorf("reads %+v", got)
	}
}

func TestReadScope(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "fixtures/a.json"), "{}")
	write(t, filepath.Join(root, "fixtures/sub/b.txt"), "b")
	write(t, filepath.Join(root, "docs/x[1].md"), "x")
	write(t, filepath.Join(root, "pkg/p.go"), "package p\n")
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	test := token.Position{Filename: "pkg/p_test.go", Line: 3}
	lib := token.Position{Filename: "pkg/p.go", Line: 4}
	p := pkg{Dir: "pkg", Name: "p"}
	for _, tc := range []struct {
		name  string
		p     pkg
		rd    read
		allow bool
		want  []string
		err   string
	}{
		{"file", p, read{pos: test, val: known("../fixtures/a.json")}, false, []string{"fixtures/a.json"}, ""},
		{"directory", p, read{pos: test, val: known("../fixtures")}, false, []string{"fixtures/**"}, ""},
		{"root", p, read{pos: test, val: known("..")}, false, []string{"**"}, ""},
		{"inside the package", p, read{pos: test, val: known("p.go")}, false, []string{"pkg/p.go"}, ""},
		{"metacharacters", p, read{pos: test, val: known("../docs/x[1].md")}, false, []string{"docs/x[[]1].md"}, ""},
		{"glob", p, read{pos: test, glob: true, val: known("../fixtures/sub/*.txt", "../docs/*/x", "*.go", "../fixtures/a.json")}, false,
			[]string{"fixtures/sub/**", "docs/**", "pkg/**", "fixtures/a.json"}, ""},
		{"missing", p, read{pos: test, val: known("../nope", "")}, false, nil, ""},
		{"empty directory", p, read{pos: test, val: known("../empty")}, false, nil, ""},
		{"absolute", p, read{pos: test, call: "os.ReadFile", val: known("/etc/passwd")}, false, nil,
			"pkg/p_test.go:3: os.ReadFile reads /etc/passwd, outside the contract root; declare what it reads by hand and pass --go-allow-dynamic pkg"},
		{"outside the root", p, read{pos: test, call: "os.ReadFile", val: known("../../x")}, false, nil, "reads ../../x, outside the contract root"},
		{"library", p, read{pos: lib, call: "os.ReadFile", val: known("../fixtures/a.json")}, false, nil,
			"pkg/p.go:4: os.ReadFile reads ../fixtures/a.json relative to the working directory, the directory of whichever package's test calls it"},
		{"main", p, read{pos: lib, main: true, val: known("../fixtures/a.json")}, false, []string{"fixtures/a.json"}, ""},
		{"working directory", p, read{pos: test, call: "os.Open", val: val{cwd: true}}, false, nil, "os.Open reads a path that it cannot resolve and that may leave the package directory"},
		{"climbing literal that names a directory", p, read{pos: test, val: val{esc: []string{"../fixtures/"}}}, false, nil, "cannot resolve"},
		{"climbing literal that names nothing", p, read{pos: test, val: val{esc: []string{"../evil", "/etc", "../../x"}}}, false, nil, ""},
		{"allowed", p, read{pos: test, val: val{cwd: true}}, true, nil, ""},
		{"allowed, resolved files still added", p, read{pos: test, val: known("/etc", "../fixtures/a.json")}, true, []string{"fixtures/a.json"}, ""},
	} {
		got, err := readScope(root, "pkg", tc.p, []read{tc.rd}, tc.allow)
		if tc.err == "" && err != nil || tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) || !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q, %v; want %q, %q", tc.name, got, err, tc.want, tc.err)
		}
	}
}

// FuzzResolve checks that the resolver never panics and that its values
// stay bounded and deterministic, whatever the path expression.
func FuzzResolve(f *testing.F) {
	for _, s := range []string{`"../x"`, `filepath.Join("..", d, "x")`, `d + "/a"`, `tc.file`, `root()`, `p`,
		`filepath.Join(t.TempDir(), "x")`, `strings.TrimSpace("../a")`, `string(rune(1))`, `filepath.Dir(filepath.Abs("../a"))`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, expr string) {
		src := header + `const d = "../data"
func root() string { return "../.." }
func f(t *testing.T) {
	p := "a"
	p = p + "/.."
	for _, tc := range []struct{ file string }{{"../a"}, {"b"}} {
		os.ReadFile(` + expr + `)
	}
}
`
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "pkg/a_test.go", src, parser.SkipObjectResolution)
		if err != nil {
			return
		}
		a := reads(fset, []*ast.File{file})
		b := reads(fset, []*ast.File{file})
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("not deterministic: %+v and %+v", a, b)
		}
		for _, r := range a {
			if len(r.val.ss) > maxValues || len(r.val.esc) > maxValues || r.val.known && (r.val.cwd || len(r.val.esc) > 0) {
				t.Fatalf("%s: %+v", expr, r.val)
			}
		}
	})
}

// A //go:build ignore generator of package main in a library's directory
// reads relative to where it runs, not to a caller's test.
func TestReadsMain(t *testing.T) {
	rs := reads(parseSrc(t, header+`func f() { os.ReadFile("a") }`, "//go:build ignore\n\npackage main\n\nimport \"os\"\n\nfunc main() { os.ReadFile(\"b\") }\n"))
	if len(rs) != 2 || !rs[0].main || rs[1].main { // package main sorts first
		t.Errorf("reads %+v", rs)
	}
}
