// Package golang is the aval gen adapter for Go modules and workspaces: one
// unit per package of the main modules under the contract root, with the
// package's own files and its imports of other such packages.
package golang

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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

	"github.com/TrianaLab/aval/internal/gen"
)

// Adapter lists the packages under the contract root with go list.
type Adapter struct {
	// Cover makes each unit's test fail below 100% statement coverage of
	// its own package. The command then needs a POSIX sh, mktemp and awk.
	Cover bool
}

// Flags registers the adapter's options as aval gen flags.
func (a *Adapter) Flags(fs *pflag.FlagSet) {
	fs.BoolVar(&a.Cover, "go-cover", false, "go: each test fails below 100% statement coverage of its package (needs sh, mktemp and awk)")
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

// coverScript runs the tests of the package in $0 and fails unless every
// statement of the profile ran: the same rule as Aval's own coverage gate.
const coverScript = `p=$(mktemp) || exit 1; go test -coverprofile="$p" "$0" && awk 'NR>1 {t+=$2; if ($3>0) c+=$2} END {if (t!=c) {print "coverage: below 100.0% of statements"; exit 1}}' "$p"; s=$?; rm -f "$p"; exit $s`

// Units runs go list -deps -test -json ./... in root. The package set and
// the embedded files are read as for linux/amd64 with cgo, so the result is
// the same on every host; imports are read from every .go file of a
// package, whatever its build constraints.
func (a *Adapter) Units(root string) ([]gen.Unit, error) {
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
	units := make([]gen.Unit, 0, len(pkgs))
	for _, p := range pkgs {
		u, err := a.unit(root, p, ids, mods)
		if err != nil {
			return nil, fmt.Errorf("package %s: %w", p.ImportPath, err)
		}
		units = append(units, u)
	}
	return units, nil
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
	_ = filepath.WalkDir(filepath.Join(dir, "testdata"), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			glob("testdata/**")
			return fs.SkipAll
		}
		return nil // never an error: a missing or unreadable testdata adds nothing
	})
	return out, errors.Join(errs...)
}

// unit builds the unit of p, whose Dir is relative to root.
func (a *Adapter) unit(root string, p pkg, ids map[string]string, mods []string) (gen.Unit, error) {
	dir := filepath.Join(root, filepath.FromSlash(p.Dir))
	src, err := p.sources(dir)
	if err != nil {
		return gen.Unit{}, fmt.Errorf("package %s: %w", p.ImportPath, err)
	}
	u := gen.Unit{ID: ids[p.ImportPath], Source: src}
	imports, err := parseImports(dir, slices.Concat(p.GoFiles, p.CgoFiles, p.TestGoFiles, p.XTestGoFiles, p.IgnoredGoFiles))
	if err != nil {
		return u, err
	}
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
	if a.Cover {
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

// parseImports returns the sorted import paths of files in dir: every .go
// file of the package, test files and files a build constraint excludes
// included.
func parseImports(dir string, files []string) ([]string, error) {
	var out []string
	for _, f := range files {
		ast, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, f), nil, parser.ImportsOnly)
		if err != nil {
			return nil, err
		}
		for _, s := range ast.Imports {
			p, _ := strconv.Unquote(s.Path.Value) // the parser checked the literal
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}
