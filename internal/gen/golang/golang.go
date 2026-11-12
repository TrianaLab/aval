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
	cmd := exec.Command("go", "list", "-deps", "-test", "-json", "./...")
	cmd.Dir = abs
	// PWD: go list reports directories under abs, which they are cut from.
	cmd.Env = append(os.Environ(), "PWD="+abs, "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=1", "GOFLAGS=")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w: %s", err, strings.TrimSpace(stderr.String()))
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
