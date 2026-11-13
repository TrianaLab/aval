// Package golang is the aval gen adapter for Go modules and workspaces: one
// unit per package of the main modules under the contract root, with the
// package's own files and its imports of other such packages.
package golang

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"golang.org/x/mod/modfile"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/gen"
)

// Adapter lists the packages under the contract root with go list.
type Adapter struct {
	// Cover makes each unit's test fail below 100% statement coverage of
	// its own package. The command then needs a POSIX sh, mktemp and awk.
	Cover bool
	// CoverProfile, when set, is a directory below the contract root: each
	// unit's test writes its package's coverage profile there, named after
	// the test, and declares it in outputs, so a reused result restores it.
	CoverProfile string
	// AllowDynamic are contract globs over unit IDs: in the packages they
	// match, a read the adapter cannot resolve inside the contract root is
	// not an error. Their authors declare those files by hand.
	AllowDynamic []string
	// NoTest are the aval gen --no-test patterns (see SkipTests): a unit
	// they match gets no test, so a read of its test files is not an error.
	NoTest []string
}

// SkipTests implements gen.TestSkipper.
func (a *Adapter) SkipTests(patterns []string) { a.NoTest = patterns }

// Flags registers the adapter's options as aval gen flags.
func (a *Adapter) Flags(fs *pflag.FlagSet) {
	fs.BoolVar(&a.Cover, "go-cover", false, "go: each test fails below 100% statement coverage of its package (needs sh, mktemp and awk)")
	fs.StringVar(&a.CoverProfile, "go-cover-profile", "", "go: each test writes its package's coverage profile to DIR/<test>.out, a directory below the contract root and outside every package's scope, and declares it in outputs, so a reused result restores it (needs sh)")
	fs.StringArrayVar(&a.AllowDynamic, "go-allow-dynamic", nil, "go: in the packages whose ID matches this contract glob (repeatable), a file read whose path cannot be resolved inside the contract root is not an error; declare those files by hand")
}

// pkg is the part of a go list -json record the adapter reads.
type pkg struct {
	ImportPath, Name, Dir, ForTest string
	Module                         *struct {
		Path    string
		Main    bool
		Replace *struct{ Version string }
	}
	GoFiles, CgoFiles, TestGoFiles, XTestGoFiles, IgnoredGoFiles           []string
	EmbedFiles, TestEmbedFiles, XTestEmbedFiles                            []string
	CFiles, CXXFiles, MFiles, HFiles, FFiles, SFiles, SwigFiles, SysoFiles []string
	SwigCXXFiles, IgnoredOtherFiles                                        []string
}

// full fails unless every statement of the profile that follows it ran: the
// same rule as Aval's own coverage gate.
const full = `awk 'NR>1 {t+=$2; if ($3>0) c+=$2} END {if (t!=c) {print "coverage: below 100.0% of statements"; exit 1}}'`

// coverScript runs the tests of the package in $0 and fails unless every
// statement of the package ran (--go-cover).
const coverScript = `p=$(mktemp) || exit 1; go test -coverprofile="$p" "$0" && ` + full + ` "$p"; s=$?; rm -f "$p"; exit $s`

// profileScript runs the tests of the package in $0 and writes its coverage
// profile to $1, creating its directory (--go-cover-profile). No -coverpkg:
// the profile covers only the package's own files, which are in the test's
// scope, so a restored profile matches the code it was reused for.
const profileScript = `mkdir -p "${1%/*}" && go test -coverprofile="$1" "$0"`

// Units runs go list -deps -test -json ./... in root. The package set and
// the embedded files are read as for linux/amd64 with cgo, so the result is
// the same on every host; imports are read from every .go file of a
// package, whatever its build constraints.
func (a *Adapter) Units(root string) ([]gen.Unit, error) {
	if a.CoverProfile != "" {
		dir := path.Clean(a.CoverProfile)
		segs := strings.Split(dir, "/")
		if _, err := gen.Literal(dir); err != nil || dir == "." || slices.Contains(segs, ".git") || slices.Contains(segs, ".aval") {
			return nil, fmt.Errorf("--go-cover-profile %q: want a directory below the contract root, slash-separated, outside .git and .aval", a.CoverProfile)
		}
	}
	pkgs, err := list(root)
	if err != nil {
		return nil, err
	}
	ids := map[string]string{} // import path -> unit ID
	var mods []string          // main module paths
	for _, p := range pkgs {
		id := p.Dir
		if id == "." {
			id = path.Base(p.ImportPath)
		}
		ids[p.ImportPath] = id
		mods = append(mods, p.Module.Path)
	}
	allowed, err := a.allowed(ids)
	if err != nil {
		return nil, err
	}
	units := make([]gen.Unit, 0, len(pkgs))
	var errs []error // every package's, so one run lists them all
	ext := externs(root, pkgs)
	for _, p := range pkgs {
		id := ids[p.ImportPath]
		noTest := slices.ContainsFunc(a.NoTest, func(g string) bool { return contract.Match(g, id) })
		u, err := a.unit(root, p, ids, mods, allowed[id], noTest, ext)
		if err != nil {
			errs = append(errs, fmt.Errorf("package %s: %w", p.ImportPath, err))
		}
		units = append(units, u)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return units, nil
}

// externs returns a function that returns the resolver of the non-test
// files of a package of pkgs by import path, built once, or nil for another
// package: a test reads what a helper of another package returns too.
func externs(root string, pkgs []pkg) func(string) *resolver {
	byPath := map[string]pkg{}
	for _, p := range pkgs {
		byPath[p.ImportPath] = p
	}
	cache := map[string]*resolver{}
	var ext func(string) *resolver
	ext = func(ip string) *resolver {
		if r, ok := cache[ip]; ok {
			return r
		}
		cache[ip] = nil
		p, ok := byPath[ip]
		if !ok {
			return nil
		}
		fset := token.NewFileSet()
		// A file that does not parse is an error of its own package's unit.
		files, _ := parseFiles(fset, root, p.Dir, slices.Concat(p.GoFiles, p.CgoFiles, p.IgnoredGoFiles))
		files = slices.DeleteFunc(files, func(f *ast.File) bool {
			return f.Name.Name != p.Name || strings.HasSuffix(fset.Position(f.Pos()).Filename, "_test.go")
		})
		if len(files) == 0 {
			return nil
		}
		r := newResolver(fset, files, ext)
		r.foreign = true
		cache[ip] = r
		return r
	}
	return ext
}

// allowed returns the unit IDs that a --go-allow-dynamic pattern matches.
// A pattern that matches none is an error, so a typo or a moved package
// cannot leave the flag doing nothing.
func (a *Adapter) allowed(ids map[string]string) (map[string]bool, error) {
	out := map[string]bool{}
	var errs []error
	for _, p := range a.AllowDynamic {
		if err := contract.ValidateGlob(p); err != nil {
			errs = append(errs, fmt.Errorf("--go-allow-dynamic: %w", err))
			continue
		}
		matched := false
		for _, id := range ids {
			if contract.Match(p, id) {
				out[id], matched = true, true
			}
		}
		if !matched {
			errs = append(errs, fmt.Errorf("--go-allow-dynamic %s: matches no package", p))
		}
	}
	return out, errors.Join(errs...)
}

// list returns the packages of the main modules that go list reports in
// root, each with Dir relative to root and slash-separated.
func list(root string) ([]pkg, error) {
	abs, err := gen.Canonical(root)
	if err != nil {
		return nil, err
	}
	out, err := goCmd(abs, "list", "-deps", "-test", "-json", "./...")
	if err != nil {
		return nil, err
	}
	var pkgs []pkg
	for dec := json.NewDecoder(bytes.NewReader(out)); ; {
		var p pkg
		if err := dec.Decode(&p); err == io.EOF {
			return pkgs, nil
		} else if err != nil {
			return nil, fmt.Errorf("go list: %w", err)
		}
		switch {
		case p.ForTest != "" || p.Name == "main" && strings.HasSuffix(p.ImportPath, ".test"), p.Module == nil:
			// a test variant, a test main or the standard library
		case p.Module.Replace != nil && p.Module.Replace.Version == "":
			return nil, fmt.Errorf("package %s: module %s is replaced by a local directory, whose files go.sum does not cover; use a go.work instead", p.ImportPath, p.Module.Path)
		case p.Module.Main:
			rel, inside := strings.CutPrefix(p.Dir, abs+string(filepath.Separator))
			if p.Dir == abs {
				rel, inside = ".", true
			}
			if !inside {
				return nil, fmt.Errorf("package %s: %s is outside the contract root %s", p.ImportPath, p.Dir, root)
			}
			p.Dir = filepath.ToSlash(rel)
			pkgs = append(pkgs, p)
		}
	}
}

// goCmd runs go args in abs, a canonical root, as for linux/amd64 with cgo
// and GOFLAGS cleared, so the result is the same on every host.
func goCmd(abs string, args ...string) ([]byte, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = abs
	// PWD: go reports paths under abs, which they are cut from.
	cmd.Env = append(os.Environ(), "PWD="+abs, "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=1", "GOFLAGS=")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// buildEnv are the variables that change what go builds or how it runs the
// tests.
var buildEnv = []string{"CGO_ENABLED", "GOARCH", "GODEBUG", "GOEXPERIMENT", "GOFLAGS", "GOOS", "GOTOOLCHAIN", "GOWORK"}

// Shared returns go.module: the Go version from the toolchain directive,
// else the go directive, of go.work in a workspace or of go.mod otherwise,
// checked by go env GOVERSION; go.work, go.work.sum and every main module's
// go.mod and go.sum that exist; and the Go build environment. A version it
// cannot read, or a file outside root, is an error.
func (a *Adapter) Shared(root string) (*gen.Shared, error) {
	abs, err := gen.Canonical(root)
	if err != nil {
		return nil, err
	}
	version, files, err := goFiles(abs, root)
	if err != nil {
		return nil, err
	}
	s := &gen.Shared{
		Toolchain: map[string]string{"go": version},
		Probe:     map[string][]string{"go": {"go", "env", "GOVERSION"}},
		Env:       buildEnv,
	}
	for _, f := range files {
		if _, err := os.Stat(f); strings.HasSuffix(f, ".sum") && errors.Is(err, fs.ErrNotExist) {
			continue // a module without dependencies has no go.sum
		}
		rel, err := filepath.Rel(abs, f)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s is outside the contract root %s", f, root)
		}
		g, err := gen.Literal(filepath.ToSlash(rel))
		if err != nil {
			return nil, err
		}
		s.Dependencies = append(s.Dependencies, g)
	}
	return s, nil
}

// goFiles returns the Go version and the absolute paths of go.work and
// go.work.sum in a workspace, plus the go.mod and go.sum of each main module.
func goFiles(abs, root string) (string, []string, error) {
	out, err := goCmd(abs, "env", "-json", "GOWORK", "GOMOD")
	var env struct{ GOWORK, GOMOD string }
	if err == nil {
		if err = json.Unmarshal(out, &env); err != nil {
			err = fmt.Errorf("go env: %w", err)
		}
	}
	if err != nil {
		return "", nil, err
	}
	file := env.GOWORK
	work := file != "" && file != "off"
	if !work {
		file = env.GOMOD
	}
	if file == "" || file == os.DevNull {
		return "", nil, fmt.Errorf("go env: no go.mod or go.work for %s", root)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", nil, err
	}
	var version string
	var files []string
	if work {
		version, files, err = workFiles(file, data)
	} else {
		var m *modfile.File
		if m, err = modfile.Parse(file, data, nil); err == nil {
			version, files = goVersion(m.Go, m.Toolchain), []string{file, strings.TrimSuffix(file, ".mod") + ".sum"}
		}
	}
	if err == nil && version == "" {
		err = fmt.Errorf("%s: cannot read the Go version: no go or toolchain directive", file)
	}
	return version, files, err
}

// workFiles parses the go.work file: its version, itself, its go.work.sum
// and the go.mod and go.sum of each module it uses.
func workFiles(file string, data []byte) (string, []string, error) {
	w, err := modfile.ParseWork(file, data, nil)
	if err != nil {
		return "", nil, err
	}
	files := []string{file, file + ".sum"}
	for _, u := range w.Use {
		dir := u.Path
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(filepath.Dir(file), dir)
		}
		files = append(files, filepath.Join(dir, "go.mod"), filepath.Join(dir, "go.sum"))
	}
	return goVersion(w.Go, w.Toolchain), files, nil
}

// goVersion returns the toolchain directive without its "go" prefix, else
// the go directive, else "". "toolchain default" names no version.
func goVersion(g *modfile.Go, t *modfile.Toolchain) string {
	if t != nil {
		if v, ok := strings.CutPrefix(t.Name, "go"); ok {
			return v
		}
	}
	if g != nil {
		return g.Version
	}
	return ""
}

// sources returns the escaped scope globs of p, whose files are in dir.
func (p pkg) sources(dir string) ([]string, error) {
	var out []string
	// Names are escaped (gen.Under, gen.Literal): a [ or * in a file or
	// directory name must not turn the scope into a different pattern.
	var errs []error
	glob := func(pattern string) {
		g, err := gen.Under(p.Dir, pattern)
		errs = append(errs, err)
		out = append(out, g)
	}
	glob("*.go")
	for _, files := range [][]string{p.EmbedFiles, p.TestEmbedFiles, p.XTestEmbedFiles, p.CFiles, p.CXXFiles,
		p.MFiles, p.HFiles, p.FFiles, p.SFiles, p.SwigFiles, p.SwigCXXFiles, p.SysoFiles, p.IgnoredOtherFiles} {
		for _, f := range files {
			g, err := gen.Literal(path.Join(p.Dir, f))
			errs = append(errs, err)
			out = append(out, g)
		}
	}
	// go list reports embedded files only for linux/amd64. A file for another
	// OS that embeds puts the whole directory in scope, which fails closed.
	// ponytail: over-broad scope; parse its //go:embed patterns if it costs too many misses.
	// A //go:embed pattern can name a directory, so a file added under it
	// later changes what is embedded: the whole package directory is in
	// scope whenever the package embeds anything (embeds cannot leave it).
	if p.embeds(dir) {
		glob("**")
	}
	// Tests read testdata/ by convention. A glob must match a file, so it is
	// listed only when testdata holds one.
	if hasFile(filepath.Join(dir, "testdata")) {
		glob("testdata/**")
	}
	return out, errors.Join(errs...)
}

// hasFile reports whether the directory dir holds a file, at any depth. A
// missing or unreadable directory holds none.
func hasFile(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			found = true
			return fs.SkipAll
		}
		return nil // never an error: what cannot be read adds nothing
	})
	return found
}

// unit builds the unit of p, whose Dir is relative to root.
func (a *Adapter) unit(root string, p pkg, ids map[string]string, mods []string, allowDynamic, noTest bool, ext func(string) *resolver) (gen.Unit, error) {
	dir := filepath.Join(root, filepath.FromSlash(p.Dir))
	src, err := p.sources(dir)
	if err != nil {
		return gen.Unit{}, fmt.Errorf("package %s: %w", p.ImportPath, err)
	}
	u := gen.Unit{ID: ids[p.ImportPath], Source: src}
	fset := token.NewFileSet()
	files, err := parseFiles(fset, root, p.Dir, slices.Concat(p.GoFiles, p.CgoFiles, p.TestGoFiles, p.XTestGoFiles, p.IgnoredGoFiles))
	if err != nil {
		return u, err
	}
	read, err := readScope(root, u.ID, p, reads(fset, files, ext), allowDynamic, noTest)
	if err != nil {
		return u, err
	}
	u.Source = append(u.Source, read...)
	imports := importPaths(files)
	for _, imp := range imports {
		id, local := ids[imp]
		switch {
		case local && imp != p.ImportPath:
			u.Deps = append(u.Deps, id)
		case !local && slices.ContainsFunc(mods, func(m string) bool { return imp == m || strings.HasPrefix(imp, m+"/") }):
			return u, fmt.Errorf("imports %s of a main module, which go list did not list under the contract root", imp)
		}
	}
	u.Command = []string{"go", "test", "./" + p.Dir}
	if p.Dir == "." {
		u.Command[2] = "."
	}
	switch {
	case a.CoverProfile != "":
		name, _ := gen.Name("go", u.ID) // an ID without a name fails in gen too
		script, profile := profileScript, path.Join(a.CoverProfile, name+".out")
		if a.Cover {
			script += " && " + full + ` "$1"`
		}
		u.Command = []string{"sh", "-c", script, u.Command[2], profile}
		u.Outputs = []string{profile}
	case a.Cover:
		u.Command = []string{"sh", "-c", coverScript, u.Command[2]}
	}
	return u, nil
}

// embeds reports whether the package embeds files, in a file for this
// platform or for another one.
func (p pkg) embeds(dir string) bool {
	return len(p.EmbedFiles)+len(p.TestEmbedFiles)+len(p.XTestEmbedFiles) > 0 || embeds(dir, p.IgnoredGoFiles)
}

// embeds reports whether a file in dir mentions //go:embed.
func embeds(dir string, files []string) bool {
	return slices.ContainsFunc(files, func(f string) bool {
		src, err := os.ReadFile(filepath.Join(dir, f))
		return err == nil && bytes.Contains(src, []byte("//go:embed"))
	})
}

// parseFiles parses files, in the package directory dir relative to root:
// every .go file of the package, test files and files a build constraint
// excludes included. Positions name the files relative to root.
func parseFiles(fset *token.FileSet, root, dir string, files []string) ([]*ast.File, error) {
	var out []*ast.File
	for _, f := range files {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), f))
		if err != nil {
			return nil, err
		}
		af, err := parser.ParseFile(fset, path.Join(dir, f), src, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		out = append(out, af)
	}
	return out, nil
}

// importPaths returns the sorted import paths of files.
func importPaths(files []*ast.File) []string {
	var out []string
	for _, f := range files {
		for _, s := range f.Imports {
			p, _ := strconv.Unquote(s.Path.Value) // the parser checked the literal
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
