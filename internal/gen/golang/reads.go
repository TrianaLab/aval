package golang

import (
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/TrianaLab/aval/internal/gen"
)

// readCalls are the functions that read a path, by import path and name.
// The path is their first argument (every argument of ParseFiles).
var readCalls = map[string]map[string]bool{
	"os": {"ReadFile": true, "Open": true, "OpenFile": true, "ReadDir": true, "Stat": true, "Lstat": true,
		"DirFS": true, "Readlink": true, "OpenRoot": true, "OpenInRoot": true},
	"io/ioutil":     {"ReadFile": true, "ReadDir": true},
	"path/filepath": {"Walk": true, "WalkDir": true, "Glob": true},
	"html/template": {"ParseFiles": true, "ParseGlob": true},
	"text/template": {"ParseFiles": true, "ParseGlob": true},
	"net/http":      {"Dir": true},
}

// stringPkgs are the packages whose functions build a string only from
// their arguments: an unknown result keeps what the arguments may escape
// through. A call to any other package may return any path.
var stringPkgs = map[string]bool{"strings": true, "fmt": true, "strconv": true, "path": true, "path/filepath": true}

// pathFuncs are the functions of path and path/filepath the resolver
// evaluates, with the number of arguments they take (-1: any).
var pathFuncs = map[string]int{"Join": -1, "Clean": 1, "FromSlash": 1, "ToSlash": 1, "Abs": 1, "Dir": 1}

// anyPath are the functions whose result can name any file: the working
// directory, the executable and the environment.
var anyPath = map[string]bool{"os.Getwd": true, "os.Executable": true, "os.Getenv": true, "os.LookupEnv": true, "os.ExpandEnv": true}

// maxValues bounds the values one expression can resolve to; past it the
// expression is unknown.
const maxValues = 64

// read is one call that reads a path: where it is, what it calls and the
// path's value.
type read struct {
	pos  token.Position
	call string // such as os.ReadFile
	main bool   // in a file of package main, which no test of another package calls
	glob bool   // filepath.Glob: the value is a pattern
	val  val
}

// val is the value of a string expression: every value it can take, or
// unknown.
type val struct {
	ss    []string // the values, when known
	known bool
	// esc are, for an unknown value, the literals and values with a ".."
	// segment it is built from: one that names a file or directory of the
	// contract root outside the package means the value may read it.
	esc []string
	// cwd: unknown, and built from the working directory, the executable's
	// path or a caller's source file, so it may read anything.
	cwd bool
}

func known(ss ...string) val { return val{ss: ss, known: true} }

// climbs reports whether a path leaves the directory it is relative to,
// or is absolute.
func climbs(s string) bool {
	s = strings.ReplaceAll(s, `\`, "/")
	return strings.HasPrefix(s, "/") || slices.Contains(strings.Split(s, "/"), "..")
}

// unknown is the value of an expression built from vs that the resolver
// cannot evaluate: it keeps what each of them may escape through.
func unknown(vs ...val) val {
	var out val
	for _, v := range vs {
		out.cwd = out.cwd || v.cwd
		out.esc = append(out.esc, v.esc...)
		if v.known {
			out.esc = append(out.esc, slices.DeleteFunc(slices.Clone(v.ss), func(s string) bool { return !climbs(s) })...)
		}
	}
	if out.esc = dedup(out.esc); len(out.esc) > maxValues {
		out = val{cwd: true} // too many to check: fail closed
	}
	return out
}

// product applies f to every combination of the values of vs.
func product(f func([]string) string, vs ...val) val {
	out := [][]string{nil}
	for _, v := range vs {
		if !v.known || len(out)*len(v.ss) > maxValues {
			return unknown(vs...)
		}
		var next [][]string
		for _, o := range out {
			for _, s := range v.ss {
				next = append(next, append(slices.Clip(o), s))
			}
		}
		out = next
	}
	ss := make([]string, len(out))
	for i, o := range out {
		ss[i] = f(o)
	}
	return known(dedup(ss)...)
}

// union is a value that can be any of vs.
func union(vs ...val) val {
	var ss []string
	for _, v := range vs {
		if !v.known {
			return unknown(vs...)
		}
		ss = append(ss, v.ss...)
	}
	if ss = dedup(ss); len(ss) > maxValues {
		return unknown(vs...)
	}
	return known(ss...)
}

func dedup(ss []string) []string { return slices.Compact(slices.Sorted(slices.Values(ss))) }

// def is one value given to a variable: an expression (its idx-th result
// when it has several), an element or key of a range, or an argument at a
// call site.
type def struct {
	e     ast.Expr
	idx   int
	rng   bool // e is the ranged expression; key: the key, else the element
	key   bool
	opAsg bool // x += e
}

// resolver evaluates path expressions in one package.
type resolver struct {
	fset  *token.FileSet
	info  *types.Info
	defs  map[types.Object][]def
	funcs map[types.Object][]*ast.FuncDecl // several in files for different platforms
	top   map[token.Pos]types.Object       // each package-level name's first object, by declaring identifier
	calls map[types.Object][]*ast.CallExpr
	refs  map[types.Object]int  // uses, calls included
	tests map[types.Object]bool // variables of type *testing.T, B, F or testing.TB
	guard map[types.Object]bool // names composite and field are following
	stack map[any]int           // keys once is computing, by depth
	memo  map[any]val
	low   int // the shallowest depth a cycle closed at in the current computation
	depth int
}

// once returns compute's value for key, computed once. A key reached again
// while it is computed (a cycle) adds nothing there: the computation that
// started it unions every definition on the cycle. A value computed inside a
// cycle that closes further out may lack the cycle's values, so it is not
// memoized.
func (r *resolver) once(key any, compute func() val) val {
	if v, ok := r.memo[key]; ok {
		return v
	}
	if lvl, ok := r.stack[key]; ok {
		r.low = min(r.low, lvl)
		return val{}
	}
	lvl, outer := len(r.stack), r.low
	r.stack[key], r.low = lvl, math.MaxInt
	v := compute()
	delete(r.stack, key)
	if r.low >= lvl {
		r.memo[key] = v
	}
	r.low = min(outer, r.low)
	return v
}

// maxDepth bounds how deep eval follows names and calls.
const maxDepth = 256

// fakeImporter imports every package as an empty one: the resolver needs
// only the names declared in the package itself.
type fakeImporter struct{}

func (fakeImporter) Import(p string) (*types.Package, error) {
	pkg := types.NewPackage(p, path.Base(p))
	pkg.MarkComplete()
	return pkg, nil
}

// reads returns every read call in files, the parsed files of one package
// (a package and its external test package are checked apart).
func reads(fset *token.FileSet, files []*ast.File) []read {
	groups := map[string][]*ast.File{}
	for _, f := range files {
		groups[f.Name.Name] = append(groups[f.Name.Name], f)
	}
	var out []read
	for _, name := range slices.Sorted(maps.Keys(groups)) {
		out = append(out, newResolver(fset, groups[name]).reads(groups[name])...)
	}
	return out
}

// newResolver type-checks files with every import empty, ignoring errors,
// and records what is assigned to each variable.
func newResolver(fset *token.FileSet, files []*ast.File) *resolver {
	r := &resolver{fset: fset, info: &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{},
		Types: map[ast.Expr]types.TypeAndValue{}},
		defs: map[types.Object][]def{}, funcs: map[types.Object][]*ast.FuncDecl{}, calls: map[types.Object][]*ast.CallExpr{}, top: map[token.Pos]types.Object{},
		refs: map[types.Object]int{}, tests: map[types.Object]bool{}, guard: map[types.Object]bool{}, stack: map[any]int{}, memo: map[any]val{}, low: math.MaxInt}
	conf := types.Config{Importer: fakeImporter{}, Error: func(error) {}, FakeImportC: true}
	_, _ = conf.Check(files[0].Name.Name, fset, files, r.info) // errors are expected: imports are empty
	r.topLevel(files)
	for _, f := range files {
		ast.Inspect(f, r.record)
	}
	for fn, decls := range r.funcs {
		for _, d := range decls {
			r.params(fn, d)
		}
	}
	return r
}

// topLevel maps each package-level declaration to the first object of its
// name: a name declared in files for two platforms is one name, with the
// values of both.
func (r *resolver) topLevel(files []*ast.File) {
	first := map[string]types.Object{}
	var ids []*ast.Ident
	add := func(id *ast.Ident) {
		ids = append(ids, id)
		if o := r.info.Defs[id]; o != nil && first[id.Name] == nil {
			first[id.Name] = o
		}
	}
	for _, f := range files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					add(d.Name)
				}
			case *ast.GenDecl:
				for _, sp := range d.Specs {
					if vs, ok := sp.(*ast.ValueSpec); ok {
						for _, id := range vs.Names {
							add(id)
						}
					}
				}
			}
		}
	}
	for _, id := range ids { // a redeclaration has no object of its own
		if o := first[id.Name]; o != nil {
			r.top[id.Pos()] = o
		}
	}
}

// obj returns the variable, constant or function id names; a name declared
// twice (in files for different platforms) is one object per name.
func (r *resolver) obj(id *ast.Ident) types.Object {
	if o := r.top[id.Pos()]; o != nil {
		return o
	}
	o := r.info.Defs[id]
	if o == nil {
		o = r.info.Uses[id]
	}
	switch o := o.(type) {
	case *types.Var:
		if o.IsField() {
			return nil
		}
	case *types.Const, *types.Func:
	default:
		return nil
	}
	if first := r.top[o.Pos()]; first != nil {
		return first
	}
	return o
}

// record collects definitions, functions and call sites from one node.
func (r *resolver) record(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.UnaryExpr:
		if n.Op == token.AND {
			r.define(root(n.X), def{}) // written through a pointer: any value
		}
	case *ast.AssignStmt:
		r.assign(n)
	case *ast.ValueSpec:
		r.spec(n)
	case *ast.RangeStmt:
		r.define(n.Key, def{e: n.X, rng: true, key: true})
		r.define(n.Value, def{e: n.X, rng: true})
	case *ast.FuncDecl:
		if o := r.obj(n.Name); o != nil && n.Recv == nil {
			r.funcs[o] = append(r.funcs[o], n)
		}
	case *ast.CallExpr:
		r.callSite(n)
	case *ast.Ident:
		if o := r.obj(n); o != nil && r.info.Uses[n] != nil {
			r.refs[o]++
		}
	case *ast.Field:
		r.testingField(n)
	}
	return true
}

// callSite records a call of a function of the package.
func (r *resolver) callSite(c *ast.CallExpr) {
	if id, ok := ast.Unparen(c.Fun).(*ast.Ident); ok {
		if o := r.obj(id); o != nil {
			r.calls[o] = append(r.calls[o], c)
		}
	}
}

// testingField records the names a field of type *testing.T, B, F or
// testing.TB declares.
func (r *resolver) testingField(f *ast.Field) {
	t := f.Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if ip, name := r.callee(t); ip == "testing" && strings.Contains(" T B F TB ", " "+name+" ") {
		for _, id := range f.Names {
			r.tests[r.obj(id)] = true
		}
	}
}

// spec records the values a var or const declaration gives.
func (r *resolver) spec(n *ast.ValueSpec) {
	for i, id := range n.Names {
		switch {
		case len(n.Values) == len(n.Names):
			r.define(id, def{e: n.Values[i]})
		case len(n.Values) == 1:
			r.define(id, def{e: n.Values[0], idx: i})
		}
	}
}

// assign records the values an assignment gives.
func (r *resolver) assign(n *ast.AssignStmt) {
	for i, l := range n.Lhs {
		d := def{e: n.Rhs[0], idx: i, opAsg: n.Tok != token.ASSIGN && n.Tok != token.DEFINE}
		if len(n.Rhs) == len(n.Lhs) {
			d = def{e: n.Rhs[i], opAsg: d.opAsg}
		}
		if _, ok := l.(*ast.Ident); !ok {
			// x.f, x[i] or *x changed: x is no longer its literal, and may
			// hold what was assigned.
			l, d.opAsg = root(l), true
		}
		r.define(l, d)
	}
}

// root returns the variable an expression such as x.f[i] or *x starts
// from, or nil.
func root(e ast.Expr) ast.Expr {
	for {
		switch x := ast.Unparen(e).(type) {
		case *ast.SelectorExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		default:
			return x
		}
	}
}

// define records d as a value of the variable l names.
func (r *resolver) define(l ast.Expr, d def) {
	if id, ok := l.(*ast.Ident); ok && id.Name != "_" {
		if o := r.obj(id); o != nil {
			r.defs[o] = append(r.defs[o], d)
		}
	}
}

// params records each call site's arguments as values of the parameters of
// fn. A function also used as a value has callers the resolver cannot see,
// so its parameters can take any value.
func (r *resolver) params(fn types.Object, decl *ast.FuncDecl) {
	var ps []*ast.Ident
	for _, f := range decl.Type.Params.List {
		ps = append(ps, f.Names...)
	}
	escapes := r.refs[fn] > len(r.calls[fn])
	l := decl.Type.Params.List
	variadic := len(l) > 0 && isEllipsis(l[len(l)-1].Type)
	for i, p := range ps {
		o := r.obj(p) // never nil: the checker declares every parameter
		if escapes {
			r.defs[o] = append(r.defs[o], def{})
		}
		for _, c := range r.calls[fn] {
			switch {
			case variadic && i == len(ps)-1:
				// A slice of the arguments, never a path itself: only
				// whether one of them climbs matters.
				for _, a := range c.Args[min(i, len(c.Args)):] {
					r.defs[o] = append(r.defs[o], def{e: a, opAsg: true})
				}
			case i < len(c.Args):
				r.defs[o] = append(r.defs[o], def{e: c.Args[i]})
			}
		}
	}
}

func isEllipsis(e ast.Expr) bool {
	_, ok := e.(*ast.Ellipsis)
	return ok
}

// reads returns the read calls in files.
func (r *resolver) reads(files []*ast.File) []read {
	var out []read
	for _, f := range files {
		dot := dotImports(f)
		ast.Inspect(f, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := ast.Unparen(c.Fun).(*ast.SelectorExpr); ok && sel.Sel.Name == "Chdir" && len(c.Args) == 0 {
				// (*os.File).Chdir: a directory it cannot resolve
				out = append(out, read{pos: r.fset.Position(c.Pos()), call: "Chdir", val: val{cwd: true}})
				return true
			}
			if len(c.Args) == 0 {
				return true
			}
			call, ok := r.readCall(c.Fun, dot)
			if !ok {
				return true
			}
			args := c.Args[:1]
			if strings.HasSuffix(call, "ParseFiles") {
				args = c.Args
			}
			for _, a := range args {
				rd := read{pos: r.fset.Position(c.Pos()), call: call, main: f.Name.Name == "main", glob: strings.HasSuffix(call, "Glob"), val: r.eval(a, 0)}
				if strings.HasSuffix(call, "Chdir") && r.getwd(a) {
					rd.val = val{} // back to the working directory: not a read
				}
				out = append(out, rd)
			}
			return true
		})
		out = append(out, r.readValues(f, dot)...)
	}
	return out
}

// readValues returns a read of any value for each read function f uses as
// a value (var readFile = os.ReadFile): it reads whatever its later callers
// pass, so it fails closed. A callee, and a value assigned to _ (keeping an
// import), is not one.
func (r *resolver) readValues(f *ast.File, dot []string) []read {
	var out []read
	skip := map[ast.Expr]bool{}
	mark := func(e ast.Expr) {
		for {
			skip[e] = true
			p, ok := e.(*ast.ParenExpr)
			if !ok {
				return
			}
			e = p.X
		}
	}
	blank := func(lhs []*ast.Ident, rhs []ast.Expr) {
		for i, id := range lhs {
			if id != nil && id.Name == "_" && len(lhs) == len(rhs) {
				mark(rhs[i])
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			mark(n.Fun)
		case *ast.ValueSpec:
			blank(n.Names, n.Values)
		case *ast.AssignStmt:
			ids := make([]*ast.Ident, len(n.Lhs))
			for i, e := range n.Lhs {
				ids[i], _ = e.(*ast.Ident)
			}
			blank(ids, n.Rhs)
		}
		return true
	})
	ast.Inspect(f, func(n ast.Node) bool {
		e, ok := n.(ast.Expr)
		if !ok || skip[e] {
			return true
		}
		if call, ok := r.readCall(e, dot); ok {
			out = append(out, read{pos: r.fset.Position(e.Pos()), call: call, main: f.Name.Name == "main", val: val{cwd: true}})
		}
		return true
	})
	return out
}

// getwd reports whether e is a variable whose only value is the working
// directory os.Getwd returned, which a test changes back to. A test that
// moves anywhere else reads under that directory.
func (r *resolver) getwd(e ast.Expr) bool {
	id, _ := ast.Unparen(e).(*ast.Ident)
	return id != nil && r.from(r.obj(id), "os", "Getwd")
}

// from reports whether every value of the variable o is the first result
// of a call to the function name of package ip.
func (r *resolver) from(o types.Object, ip, name string) bool {
	ds := r.defs[o]
	return len(ds) > 0 && !slices.ContainsFunc(ds, func(d def) bool {
		c, ok := ast.Unparen(d.e).(*ast.CallExpr)
		if !ok || d.idx != 0 || d.rng || d.opAsg {
			return true
		}
		cip, cname := r.callee(c.Fun)
		return cip != ip || cname != name
	})
}

// chdirPkgs are the packages whose Chdir moves the process's directory.
var chdirPkgs = map[string]bool{"os": true, "syscall": true, "golang.org/x/sys/unix": true}

// readCall returns the name of the read call fun is, such as os.ReadFile:
// a function of readCalls, through a dot import too, or os.Chdir,
// syscall.Chdir, unix.Chdir or a Chdir method (testing.T.Chdir), which make
// later relative reads land under their directory.
func (r *resolver) readCall(fun ast.Expr, dot []string) (string, bool) {
	ip, name := r.callee(fun)
	switch fun := ast.Unparen(fun).(type) {
	case *ast.Ident: // a dot import: the empty package declares no member
		for _, d := range dot {
			if (readCalls[d][fun.Name] || chdirPkgs[d] && fun.Name == "Chdir") && r.obj(fun) == nil {
				return path.Base(d) + "." + fun.Name, true
			}
		}
	case *ast.SelectorExpr:
		if readCalls[ip][name] || chdirPkgs[ip] && name == "Chdir" {
			return path.Base(ip) + "." + name, true
		}
		if ip == "" && fun.Sel.Name == "Chdir" {
			return "Chdir", true
		}
	}
	return "", false
}

// dotImports returns the import paths f imports with a dot.
func dotImports(f *ast.File) []string {
	var out []string
	for _, im := range f.Imports {
		if im.Name != nil && im.Name.Name == "." {
			p, _ := strconv.Unquote(im.Path.Value) // the parser checked the literal
			out = append(out, p)
		}
	}
	return out
}

// callee returns the import path and name of a reference to a package
// member, such as os.ReadFile.
func (r *resolver) callee(e ast.Expr) (string, string) {
	sel, ok := ast.Unparen(e).(*ast.SelectorExpr)
	if !ok {
		return "", ""
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", ""
	}
	pn, ok := r.info.Uses[x].(*types.PkgName)
	if !ok {
		return "", ""
	}
	return pn.Imported().Path(), sel.Sel.Name
}

// eval returns the value of e, or of its idx-th result when it is a call
// with several.
func (r *resolver) eval(e ast.Expr, idx int) val {
	if r.depth++; r.depth > maxDepth {
		return val{cwd: true} // too deep to follow: fail closed
	}
	defer func() { r.depth-- }()
	switch e := ast.Unparen(e).(type) {
	case *ast.BasicLit:
		if s, err := strconv.Unquote(e.Value); err == nil && e.Kind == token.STRING {
			return known(s)
		}
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			return product(func(s []string) string { return s[0] + s[1] }, r.eval(e.X, 0), r.eval(e.Y, 0))
		}
	case *ast.Ident:
		if o := r.obj(e); o != nil {
			return r.object(o)
		}
	case *ast.SelectorExpr:
		if v, ok := r.member(e); ok {
			return v
		}
		if v, ok := r.field(e); ok {
			return v
		}
	case *ast.CallExpr:
		if v, ok := r.call(e, idx); ok {
			return v
		}
		return r.opaque(e)
	}
	return r.taint(e)
}

// member returns the value of a member of another package, such as
// os.DevNull: any value, unless the resolver knows it.
func (r *resolver) member(sel *ast.SelectorExpr) (val, bool) {
	switch ip, name := r.callee(sel); {
	case ip == "os" && name == "DevNull":
		return val{}, true // not a file of the repository
	case ip == "os" && name == "PathSeparator", ip == "path/filepath" && name == "Separator":
		return known("/"), true
	case ip != "":
		return val{cwd: true}, true
	}
	return val{}, false
}

// opaque returns the value of a call the resolver does not evaluate. A
// temporary path or a test's name is not a file of the repository, and a
// function of stringPkgs keeps what its arguments may escape through. Any
// other call (another package, a method, a function value) may return any
// path: fail closed.
func (r *resolver) opaque(c *ast.CallExpr) val {
	ip, name := r.callee(c.Fun)
	switch {
	case ip == "os" && (name == "TempDir" || name == "MkdirTemp"), r.temp(c.Fun):
		return val{}
	case stringPkgs[ip]:
		vs := make([]val, len(c.Args))
		for i, a := range c.Args {
			vs[i] = r.eval(a, 0)
		}
		return unknown(vs...)
	}
	return val{cwd: true}
}

// temp reports whether fun is t.TempDir or t.Name of a testing variable,
// or the Name method of a file os.CreateTemp returned.
func (r *resolver) temp(fun ast.Expr) bool {
	sel, _ := ast.Unparen(fun).(*ast.SelectorExpr)
	if sel == nil {
		return false
	}
	id, _ := sel.X.(*ast.Ident)
	if id == nil {
		return false
	}
	o := r.obj(id)
	if r.tests[o] {
		return sel.Sel.Name == "TempDir" || sel.Sel.Name == "Name"
	}
	return sel.Sel.Name == "Name" && r.from(o, "os", "CreateTemp")
}

// taint reports whether an expression the resolver cannot evaluate may
// still leave the package directory: the unknown value built from the
// string literals, names, selectors and calls in it.
func (r *resolver) taint(e ast.Expr) val {
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		if o := r.obj(x); o != nil {
			return unknown(r.object(o))
		}
		return val{} // a builtin, a type or a field name
	case *ast.CallExpr:
		return r.opaque(x)
	}
	var vs []val
	ast.Inspect(e, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BasicLit:
			if s, err := strconv.Unquote(n.Value); err == nil && n.Kind == token.STRING {
				vs = append(vs, known(s))
			}
		case *ast.Ident, *ast.CallExpr, *ast.SelectorExpr:
			if n != e {
				vs = append(vs, r.eval(n.(ast.Expr), 0))
				return false
			}
		}
		return true
	})
	return unknown(vs...)
}

// object returns the value of a variable, constant or function result.
func (r *resolver) object(o types.Object) val {
	return r.once(o, func() val {
		defs := r.defs[o]
		if len(defs) == 0 {
			if v, ok := o.(*types.Var); ok && (v.Kind() == types.ParamVar || v.Kind() == types.RecvVar) {
				// A parameter of a method or literal, or of a function
				// no call in the package passes: any value.
				return val{cwd: true}
			}
			return val{} // a name without a value: the zero value, or no path
		}
		vs := make([]val, len(defs))
		for i, d := range defs {
			vs[i] = r.def(d)
		}
		return union(vs...)
	})
}

// def returns the value d gives.
func (r *resolver) def(d def) val {
	switch {
	case d.e == nil:
		return val{cwd: true} // written through a pointer, or a parameter of a function used as a value: any value
	case d.opAsg:
		return unknown(r.eval(d.e, 0))
	case d.rng && d.key:
		_, keys, ok := r.elements(d.e)
		switch {
		case ok && keys != nil:
			return r.evalAll(keys)
		case ok:
			return val{} // an index
		}
		return r.taint(d.e)
	case d.rng:
		if elts, _, ok := r.elements(d.e); ok {
			return r.evalAll(elts)
		}
		return r.taint(d.e)
	}
	return r.eval(d.e, d.idx)
}

func (r *resolver) evalAll(es []ast.Expr) val {
	vs := make([]val, len(es))
	for i, e := range es {
		vs[i] = r.eval(e, 0)
	}
	return union(vs...)
}

// composite returns the composite literal e is or names.
func (r *resolver) composite(e ast.Expr) (*ast.CompositeLit, bool) {
	switch e := ast.Unparen(e).(type) {
	case *ast.CompositeLit:
		return e, true
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return r.composite(e.X)
		}
	case *ast.Ident:
		if o := r.obj(e); o != nil && len(r.defs[o]) == 1 && !r.defs[o][0].rng && !r.defs[o][0].opAsg && r.defs[o][0].e != nil {
			if r.guard[o] {
				return nil, false
			}
			r.guard[o] = true
			defer delete(r.guard, o)
			return r.composite(r.defs[o][0].e)
		}
	}
	return nil, false
}

// elements returns the elements of the slice, array or map literal e is or
// names, and the keys of a map.
func (r *resolver) elements(e ast.Expr) (elts, keys []ast.Expr, ok bool) {
	c, ok := r.composite(e)
	if !ok {
		return nil, nil, false
	}
	_, isMap := underlying(r.info.Types[c].Type).(*types.Map)
	for _, el := range c.Elts {
		if kv, isKV := el.(*ast.KeyValueExpr); isKV {
			if isMap {
				keys = append(keys, kv.Key)
			}
			el = kv.Value
		}
		elts = append(elts, el)
	}
	if isMap && keys == nil {
		keys = []ast.Expr{} // an empty map: no keys, not an index
	}
	return elts, keys, true
}

// field returns the value of x.f when x is a struct literal, or ranges
// over a literal of struct literals, that the resolver can read.
func (r *resolver) field(sel *ast.SelectorExpr) (val, bool) {
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return val{}, false
	}
	o := r.obj(id)
	if o == nil || len(r.defs[o]) == 0 || r.guard[o] {
		return val{}, false
	}
	r.guard[o] = true
	defer delete(r.guard, o)
	var structs []ast.Expr
	for _, d := range r.defs[o] {
		switch elts, _, ok := r.elements(d.e); {
		case d.rng && !d.key && ok:
			structs = append(structs, elts...)
		case !d.rng && !d.opAsg && d.e != nil:
			structs = append(structs, d.e)
		default:
			return val{}, false
		}
	}
	vs := make([]val, 0, len(structs))
	for _, s := range structs {
		v, ok := r.structField(s, sel.Sel.Name)
		if !ok {
			return val{}, false
		}
		vs = append(vs, v)
	}
	return union(vs...), true
}

// underlying returns t's underlying type, through a pointer, or nil when
// the checker found none.
func underlying(t types.Type) types.Type {
	if t == nil {
		return nil
	}
	if p, ok := t.Underlying().(*types.Pointer); ok {
		return p.Elem().Underlying()
	}
	return t.Underlying()
}

// structField returns field name of the struct literal e.
func (r *resolver) structField(e ast.Expr, name string) (val, bool) {
	c, ok := r.composite(e)
	if !ok {
		return val{}, false
	}
	st, ok := underlying(r.info.Types[c].Type).(*types.Struct)
	if !ok {
		return val{}, false
	}
	for i, el := range c.Elts {
		if kv, isKV := el.(*ast.KeyValueExpr); isKV {
			if k, _ := kv.Key.(*ast.Ident); k != nil && k.Name == name {
				return r.eval(kv.Value, 0), true
			}
		} else if i < st.NumFields() && st.Field(i).Name() == name {
			return r.eval(el, 0), true
		}
	}
	return known(""), true // not set: the zero value
}

// call returns the value of a call the resolver knows: a function of path
// or path/filepath, a conversion, a function of the package, or one that
// locates the working directory or the source file.
func (r *resolver) call(c *ast.CallExpr, idx int) (val, bool) {
	if tv, ok := r.info.Types[c.Fun]; ok && tv.IsType() && len(c.Args) == 1 {
		return r.eval(c.Args[0], 0), true // string(x)
	}
	ip, name := r.callee(c.Fun)
	switch {
	case ip == "runtime" && name == "Caller":
		return r.caller(c, idx), true
	case anyPath[ip+"."+name]:
		return val{cwd: true}, true
	case (ip == "path" || ip == "path/filepath") && pathFuncs[name] != 0:
		return r.pathFunc(c, name), true
	}
	if id, ok := ast.Unparen(c.Fun).(*ast.Ident); ok {
		if o := r.obj(id); o != nil && len(r.funcs[o]) > 0 {
			return r.results(o, idx), true
		}
	}
	return val{}, false
}

// caller returns the idx-th result of runtime.Caller: with depth 0, the file
// name is this source file, relative to its directory; otherwise a caller's
// file, which can be anywhere.
func (r *resolver) caller(c *ast.CallExpr, idx int) val {
	if lit, ok := c.Args[0].(*ast.BasicLit); ok && lit.Value == "0" && idx == 1 {
		return known(path.Base(r.fset.Position(c.Pos()).Filename))
	}
	return val{cwd: true}
}

// pathFunc evaluates a call to Join, Clean, FromSlash, ToSlash, Abs or Dir.
// Paths are slash-separated: a backslash is a separator too.
func (r *resolver) pathFunc(c *ast.CallExpr, name string) val {
	if n := pathFuncs[name]; (n > 0 && len(c.Args) != n) || c.Ellipsis.IsValid() {
		return r.taint(c)
	}
	args := make([]val, len(c.Args))
	for i, a := range c.Args {
		args[i] = r.eval(a, 0)
	}
	return product(func(s []string) string {
		for i := range s {
			s[i] = strings.ReplaceAll(s[i], `\`, "/")
		}
		switch name {
		case "Join":
			return path.Join(s...)
		case "Dir":
			return path.Dir(s[0])
		}
		return path.Clean(s[0])
	}, args...)
}

// results returns the idx-th result of the package function fn: the union
// of its return statements.
func (r *resolver) results(fn types.Object, idx int) val {
	return r.once(struct {
		types.Object
		int
	}{fn, idx}, func() val {
		var vs []val
		for _, d := range r.funcs[fn] {
			vs = append(vs, r.returns(d, idx)...)
		}
		if len(vs) == 0 {
			return val{}
		}
		return union(vs...)
	})
}

// returns is the values of the idx-th result of decl, one per return
// statement.
func (r *resolver) returns(decl *ast.FuncDecl, idx int) []val {
	if decl.Body == nil {
		return []val{{cwd: true}} // implemented elsewhere (assembly, linkname): any value
	}
	var named []*ast.Ident
	if decl.Type.Results != nil {
		for _, f := range decl.Type.Results.List {
			named = append(named, f.Names...)
		}
	}
	var vs []val
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			switch {
			case len(n.Results) == 0 && idx < len(named):
				vs = append(vs, r.eval(named[idx], 0))
			case len(n.Results) == 1 && idx > 0:
				vs = append(vs, r.eval(n.Results[0], idx))
			case idx < len(n.Results):
				vs = append(vs, r.eval(n.Results[idx], 0))
			}
		}
		return true
	})
	return vs
}

// readScope returns the scope globs of what the reads rs of the package p
// (unit id) name: each file, or each directory as <dir>/**, inside the
// contract root. Paths are relative to the package directory, where go test
// runs its tests. A path that does not exist adds nothing: when it appears,
// aval gen go --check reports the drift. Unless allow is set, a read that
// may leave the package directory without a value the resolver knows, a
// path outside the contract root, and a relative path in a non-test file of
// a package other than main (relative to the directory of whichever test
// calls it) are
// errors (invariant 3).
func readScope(root, id string, p pkg, rs []read, allow bool) ([]string, error) {
	dirs, err := p.chdirs(root, id, rs)
	if err != nil && !allow {
		return nil, err
	}
	var out []string
	var errs []error
	for _, rd := range rs {
		problem := ""
		if !rd.val.known && p.escapes(root, dirs, rd.val) {
			problem = "reads a path that it cannot resolve and that may leave the package directory"
		}
		for _, s := range rd.val.ss {
			for _, c := range candidates(dirs, rd, s) {
				g, why := p.readGlob(root, rd, c)
				if g != "" {
					out = append(out, g)
				}
				if problem == "" {
					problem = why
				}
			}
		}
		if problem != "" && !allow {
			errs = append(errs, fmt.Errorf("%s:%d: %s %s; declare what it reads by hand and pass --go-allow-dynamic %s", rd.pos.Filename, rd.pos.Line, rd.call, problem, id))
		}
	}
	return out, errors.Join(errs...)
}

// maxChdirs bounds the Chdir chains chdirs follows.
const maxChdirs = 64

// chdirs returns the directories, relative to the package directory, that
// a relative read of the package may run in: os.Chdir lasts for the whole
// process, so the package directory and every existing directory of the
// contract root that its known Chdir targets reach, in any order, each
// once. A target
// that does not exist fails the Chdir; one outside the root is an error of
// its own read.
func (p pkg) chdirs(root, id string, rs []read) ([]string, error) {
	var ts []string
	for _, rd := range rs {
		if strings.HasSuffix(rd.call, "Chdir") && rd.val.known {
			ts = append(ts, rd.val.ss...)
		}
	}
	// ponytail: each chain uses a target once; a Chdir the tests repeat
	// (os.Chdir("..") in a loop) is not followed (see the guide).
	type state struct {
		dir  string
		used []bool
	}
	q := []state{{".", make([]bool, len(ts))}}
	dirs := []string{"."}
	for i := 0; i < len(q); i++ {
		for j, t := range ts {
			t = strings.ReplaceAll(t, `\`, "/")
			d := path.Join(q[i].dir, t)
			rel := path.Join(p.Dir, d)
			if q[i].used[j] || path.IsAbs(t) || filepath.VolumeName(t) != "" || climbs(rel) {
				continue
			}
			if fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil || !fi.IsDir() {
				continue
			}
			if len(q) == maxChdirs {
				return dirs, fmt.Errorf("%s: os.Chdir and Chdir reach more than %d directories; declare what the package reads by hand and pass --go-allow-dynamic %s", p.Dir, maxChdirs, id)
			}
			used := slices.Clone(q[i].used)
			used[j] = true
			q = append(q, state{d, used})
			if !slices.Contains(dirs, d) {
				dirs = append(dirs, d)
			}
		}
	}
	return dirs, nil
}

// candidates returns the paths, relative to the package directory, that
// the path s rd reads may name: s itself and, for a relative read other
// than a Chdir, s under each directory in dirs the package changes to.
func candidates(dirs []string, rd read, s string) []string {
	if s == "" || strings.HasSuffix(rd.call, "Chdir") || path.IsAbs(s) || filepath.VolumeName(s) != "" {
		return []string{s}
	}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d == "." {
			out = append(out, s)
		} else {
			out = append(out, path.Join(d, s))
		}
	}
	return slices.Compact(out)
}

// escapes reports whether the unknown value v may read a file of the
// contract root outside the package directory: it is built from the working
// directory, or from a relative path with a ".." segment that names an
// existing file or directory inside the root, from the package directory or
// any of the directories dirs it changes to. A literal that names nothing
// (test data such as "../evil") is not a read; once it names something,
// aval gen go --check fails on it.
func (p pkg) escapes(root string, dirs []string, v val) bool {
	return v.cwd || slices.ContainsFunc(v.esc, func(s string) bool {
		s = strings.ReplaceAll(s, `\`, "/")
		if slices.Contains(strings.Split(s, "/"), "..") {
			s = strings.TrimLeft(s, "/") // "/.." appended to a relative path
		}
		if path.IsAbs(s) || filepath.VolumeName(s) != "" {
			return false // outside the root: test data, or a file no scope can hold
		}
		return slices.ContainsFunc(dirs, func(d string) bool {
			return slices.ContainsFunc(cuts(s), func(c string) bool {
				rel := path.Join(p.Dir, d, c)
				if climbs(rel) {
					return false
				}
				_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
				return err == nil
			})
		})
	})
}

// cuts returns s, s before its first format verb, and each ancestor of s
// that ends in a named segment: a template ("../../fixtures/%s.json",
// "../../fixtures/NAME.json") names the directory fmt or strings fill in.
func cuts(s string) []string {
	out := []string{s}
	if i := strings.IndexByte(s, '%'); i >= 0 {
		out = append(out, s[:i])
	}
	for d := path.Dir(s); d != "." && d != "/" && path.Base(d) != ".."; d = path.Dir(d) {
		out = append(out, d)
	}
	return out
}

// readGlob returns the scope glob of the path s that rd reads, "" when it
// adds nothing, or why it cannot be resolved inside the contract root.
func (p pkg) readGlob(root string, rd read, s string) (glob, why string) {
	if rd.glob {
		s = globDir(s)
	}
	s = strings.ReplaceAll(s, `\`, "/")
	rel := path.Join(p.Dir, s)
	switch {
	case s == "":
		return "", "" // nothing to read
	case path.IsAbs(s) || filepath.VolumeName(s) != "" || climbs(rel):
		return "", fmt.Sprintf("reads %s, outside the contract root", s)
	case !strings.HasSuffix(rd.pos.Filename, "_test.go") && !rd.main:
		return "", fmt.Sprintf("reads %s relative to the working directory, the directory of whichever package's test calls it", s)
	}
	fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	switch {
	case err != nil:
		return "", "" // missing: --check reports it once it exists
	case !fi.IsDir():
		// ponytail: rel is clean and has no backslash, so a contract glob
		// can always express it; Literal and Under cannot fail here.
		g, _ := gen.Literal(rel)
		return g, ""
	case hasFile(filepath.Join(root, filepath.FromSlash(rel))):
		g, _ := gen.Under(rel, "**")
		return g, ""
	}
	return "", "" // an empty directory: a glob must match a file
}

// globDir returns the directory a filepath.Glob pattern reads: the
// segments before the first one with a metacharacter, or the pattern itself
// when it has none.
func globDir(pattern string) string {
	segs := strings.Split(strings.ReplaceAll(pattern, `\`, "/"), "/")
	for i, seg := range segs {
		if strings.ContainsAny(seg, `*?[`) {
			if i == 0 {
				return "."
			}
			return strings.Join(segs[:i], "/")
		}
	}
	return pattern
}
