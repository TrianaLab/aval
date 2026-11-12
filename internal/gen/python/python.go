// Package python is the aval gen adapter for Python: one unit per local
// project (a pyproject.toml with a project name: uv workspace members,
// poetry and pip projects), with edges to the other local projects it
// declares as dependencies or imports. Python itself parses the manifests
// (tomllib) and the sources (ast); see facts.py.
package python

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/pflag"

	"github.com/TrianaLab/aval/internal/gen"
)

//go:embed facts.py
var factsPy string

// Adapter lists the Python projects under a contract root.
type Adapter struct {
	// StrictImports makes an import_module or __import__ call whose module
	// is not a string literal an error instead of an edge nobody sees.
	StrictImports bool
}

// Flags registers the adapter's flags.
func (a *Adapter) Flags(fs *pflag.FlagSet) {
	fs.BoolVar(&a.StrictImports, "strict-imports", false, "python: fail on an import_module or __import__ call whose module is not a string literal")
}

// manifest is the part of a pyproject.toml that names a project and its
// dependencies.
type manifest struct {
	Project struct {
		Name                 string
		Dependencies         []string
		OptionalDependencies map[string][]string `json:"optional-dependencies"`
	}
	DependencyGroups map[string][]any `json:"dependency-groups"`
	Tool             struct {
		Poetry struct {
			Name            string
			Dependencies    map[string]any
			DevDependencies map[string]any `json:"dev-dependencies"`
			Group           map[string]struct{ Dependencies map[string]any }
		}
		UV struct {
			DevDependencies []string `json:"dev-dependencies"`
		} `json:"uv"`
	}
}

type source struct {
	Imports []string
	Dynamic []int
}

type facts struct {
	Manifests map[string]json.RawMessage
	Files     map[string]source
}

// project is one unit while it is being built.
type project struct {
	name     string // normalized project name, the unit ID
	dir      string // relative to the root, slash-separated, "." for the root
	declared []string
	files    []string // the files it owns, relative to the root
	deps     map[string]bool
	poetry   bool // named only in [tool.poetry], so not a uv workspace member
}

// Units lists one unit per local project under root.
func (a *Adapter) Units(root string) ([]gen.Unit, error) {
	projects, err := load(root)
	if err != nil {
		return nil, err
	}
	if err := a.link(root, projects); err != nil {
		return nil, err
	}
	dirs := map[string]bool{}
	for _, p := range projects {
		dirs[p.dir] = true
	}
	uv := exists(filepath.Join(root, "uv.lock"))
	units := make([]gen.Unit, 0, len(projects))
	for _, p := range projects {
		u := gen.Unit{ID: p.name, Source: p.globs(dirs), Command: p.command(uv, dirs)}
		for d := range p.deps {
			u.Deps = append(u.Deps, d)
		}
		slices.Sort(u.Deps)
		units = append(units, u)
	}
	return units, nil
}

// load reads the projects under root and gives each the files it owns.
func load(root string) ([]*project, error) {
	files, err := walk(root)
	if err != nil {
		return nil, err
	}
	var manifests []string
	for _, file := range files {
		if path.Base(file) == "pyproject.toml" {
			manifests = append(manifests, file)
		}
	}
	f, err := run(root, manifests, nil)
	if err != nil {
		return nil, err
	}
	var projects []*project
	for _, m := range manifests {
		p, err := newProject(m, f.Manifests[m])
		if err != nil {
			return nil, err
		}
		if p != nil {
			projects = append(projects, p)
		}
	}
	if len(projects) == 0 {
		return nil, fmt.Errorf("no pyproject.toml with a project name under %s", root)
	}
	for _, file := range files {
		if p := owner(projects, file); p != nil {
			p.files = append(p.files, file)
		}
	}
	return projects, nil
}

// link adds the edges between projects: declared dependencies and the
// imports in their .py files.
func (a *Adapter) link(root string, projects []*project) error {
	byImport := map[string][]*project{}
	byName := map[string][]*project{}
	owned := map[string]*project{}
	var sources []string
	for _, p := range projects {
		byName[p.name] = append(byName[p.name], p)
		for _, m := range p.provides() {
			byImport[m] = append(byImport[m], p)
		}
		for _, file := range p.files {
			if strings.HasSuffix(file, ".py") {
				owned[file] = p
				sources = append(sources, file)
			}
		}
	}
	f, err := run(root, nil, sources)
	if err != nil {
		return err
	}
	for _, p := range projects {
		for _, d := range p.declared {
			p.edge(byName[d])
		}
	}
	var errs []error
	for _, file := range sources {
		p, s := owned[file], f.Files[file]
		for _, imp := range s.Imports {
			if top, _, _ := strings.Cut(imp, "."); top != "" { // "" is a relative import
				p.edge(byImport[top])
			}
		}
		if a.StrictImports {
			for _, line := range s.Dynamic {
				errs = append(errs, fmt.Errorf("%s:%d: a dynamic import aval gen cannot resolve (--strict-imports)", file, line))
			}
		}
	}
	return errors.Join(errs...)
}

// command runs pytest on p's directory but not on nested projects, whose
// files are not in p's scope: through uv run in a uv workspace, else with
// the python of the active environment.
func (p *project) command(uv bool, dirs map[string]bool) []string {
	cmd := []string{"python", "-m", "pytest", p.dir}
	if uv && !p.poetry {
		cmd = []string{"uv", "run", "--package", p.name, "pytest", p.dir}
	}
	for _, d := range slices.Sorted(maps.Keys(dirs)) {
		if d != p.dir && (p.dir == "." || strings.HasPrefix(d, p.dir+"/")) {
			cmd = append(cmd, "--ignore="+d)
		}
	}
	return cmd
}

// edge adds a dependency on each of to but p itself.
func (p *project) edge(to []*project) {
	for _, q := range to {
		if q != p {
			p.deps[q.name] = true
		}
	}
}

// rel is file relative to p.dir.
func (p *project) rel(file string) string {
	if p.dir == "." {
		return file
	}
	return strings.TrimPrefix(file, p.dir+"/")
}

// globs covers p's files. A project without nested projects owns dir/**, so
// a new file in it is in scope before gen runs again. Otherwise: dir/** for
// each top-level directory of p, and one glob per file, descending only
// toward the directories of nested projects (dirs), which own their own files.
func (p *project) globs(dirs map[string]bool) []string {
	if p.dir != "." && !nests(p.dir, dirs) {
		return []string{p.dir + "/**"}
	}
	var globs []string
	for _, file := range p.files {
		dir := p.dir
		glob := file
		for seg := range strings.SplitSeq(path.Dir(p.rel(file)), "/") {
			if seg == "." {
				break
			}
			dir = path.Join(dir, seg)
			if !nests(dir, dirs) {
				glob = dir + "/**"
				break
			}
		}
		globs = append(globs, glob)
	}
	return slices.Compact(globs)
}

// nests reports whether a project lives below dir.
func nests(dir string, dirs map[string]bool) bool {
	for d := range dirs {
		if strings.HasPrefix(d, dir+"/") {
			return true
		}
	}
	return false
}

// provides lists p's top-level import names in the flat and src layouts:
// packages (directories with __init__.py) and modules (.py files) at the
// top of p and of p/src.
func (p *project) provides() []string {
	var names []string
	for _, file := range p.files {
		rel := strings.TrimPrefix(p.rel(file), "src/")
		if name, ok := strings.CutSuffix(rel, "/__init__.py"); ok && !strings.Contains(name, "/") {
			names = append(names, name)
		} else if name, ok := strings.CutSuffix(rel, ".py"); ok && !strings.Contains(name, "/") {
			names = append(names, name)
		}
	}
	return names
}

var reqName = regexp.MustCompile(`^\s*([A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?)`)
var separators = regexp.MustCompile(`[-_.]+`)

// normalize is the PEP 503 form of a project name.
func normalize(name string) string { return separators.ReplaceAllString(strings.ToLower(name), "-") }

// newProject reads the manifest at file, or returns nil when it names no
// project (a uv workspace root that is not a project itself).
func newProject(file string, raw json.RawMessage) (*project, error) {
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	name, poetry := m.Project.Name, false
	if name == "" {
		name, poetry = m.Tool.Poetry.Name, true
	}
	if name == "" {
		return nil, nil
	}
	if reqName.FindString(name) != name {
		return nil, fmt.Errorf("%s: invalid project name %q", file, name)
	}
	p := &project{name: normalize(name), dir: path.Dir(file), deps: map[string]bool{}, poetry: poetry}
	reqs := slices.Clone(m.Project.Dependencies)
	for _, g := range m.Project.OptionalDependencies {
		reqs = append(reqs, g...)
	}
	for _, g := range m.DependencyGroups {
		for _, r := range g {
			if s, ok := r.(string); ok { // not an {include-group = ...} table
				reqs = append(reqs, s)
			}
		}
	}
	reqs = append(reqs, m.Tool.UV.DevDependencies...)
	for _, r := range reqs {
		if n := reqName.FindStringSubmatch(r); n != nil {
			p.declared = append(p.declared, normalize(n[1]))
		}
	}
	tables := []map[string]any{m.Tool.Poetry.Dependencies, m.Tool.Poetry.DevDependencies}
	for _, g := range m.Tool.Poetry.Group {
		tables = append(tables, g.Dependencies)
	}
	for _, t := range tables {
		for n := range t {
			p.declared = append(p.declared, normalize(n))
		}
	}
	return p, nil
}

// owner is the project whose directory holds file most closely, or nil.
func owner(projects []*project, file string) *project {
	var best *project
	for _, p := range projects {
		if (p.dir == "." || strings.HasPrefix(file, p.dir+"/")) && (best == nil || len(p.dir) > len(best.dir)) {
			best = p
		}
	}
	return best
}

// skip reports whether the directory dir, called name, holds no project
// input: hidden directories (.git, .venv, caches), __pycache__,
// node_modules and virtual environments.
func skip(dir, name string) bool {
	return strings.HasPrefix(name, ".") || name == "__pycache__" || name == "node_modules" || exists(filepath.Join(dir, "pyvenv.cfg"))
}

// walk lists the files under root, relative and slash-separated, in
// lexical order, leaving out skipped directories.
func walk(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		switch {
		case !d.IsDir():
			files = append(files, filepath.ToSlash(rel))
		case rel != "." && skip(p, d.Name()):
			return filepath.SkipDir
		}
		return nil
	})
	return files, err
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// interpreter is the Python that runs facts.py: python3, else python.
func interpreter() (string, error) {
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New("python3 not found on PATH (aval gen python needs Python 3.11 or later)")
}

// run reads the manifests and source files, relative to root, with Python.
func run(root string, manifests, files []string) (facts, error) {
	var f facts
	py, err := interpreter()
	if err != nil {
		return f, err
	}
	in, _ := json.Marshal(map[string][]string{"manifests": nonNil(manifests), "files": nonNil(files)})
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(py, "-I", "-c", factsPy)
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = root, bytes.NewReader(in), &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return f, fmt.Errorf("python: %s", msg)
		}
		return f, fmt.Errorf("python: %w", err)
	}
	if err := json.Unmarshal(stdout.Bytes(), &f); err != nil {
		return f, fmt.Errorf("python: %w", err)
	}
	return f, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
