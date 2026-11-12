// Package js is the aval gen adapter for JavaScript and TypeScript
// workspaces (npm, pnpm, yarn). It reads the manifests and runs no tool:
// one unit per workspace package, with an edge for every dependency on
// another workspace package of the repository: in package.json, in a
// tsconfig references or extends, or through a relative import.
package js

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/gen"
)

// Adapter lists the workspace packages under the contract root.
type Adapter struct{}

var _ gen.Adapter = Adapter{}

// lockfiles name the package manager each lockfile belongs to.
var lockfiles = []struct{ file, manager string }{
	{"package-lock.json", "npm"},
	{"npm-shrinkwrap.json", "npm"},
	{"pnpm-lock.yaml", "pnpm"},
	{"yarn.lock", "yarn"},
}

type manifest struct {
	Name                 string            `json:"name"`
	Workspaces           json.RawMessage   `json:"workspaces"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
}

// Units implements gen.Adapter. The lockfile at root picks the package
// manager; pnpm reads pnpm-workspace.yaml, npm and yarn the workspaces of
// package.json. A missing or ambiguous lockfile, an unreadable manifest, a
// workspace package without a name or a local reference that is not a
// workspace package (in package.json, a tsconfig or a relative import) is
// an error.
func (Adapter) Units(root string) ([]gen.Unit, error) {
	pm, err := manager(root)
	if err != nil {
		return nil, err
	}
	patterns, err := workspaces(root, pm)
	if err != nil {
		return nil, err
	}
	dirs, err := packageDirs(root, patterns)
	if err != nil {
		return nil, err
	}
	pkgs := make([]manifest, len(dirs))
	byDir := map[string]string{}
	for i, dir := range dirs {
		if err := readJSON(filepath.Join(root, filepath.FromSlash(dir), "package.json"), &pkgs[i]); err != nil {
			return nil, err
		}
		if pkgs[i].Name == "" {
			return nil, fmt.Errorf("%s/package.json: no name", dir)
		}
		byDir[dir] = pkgs[i].Name
	}
	isUnit := map[string]bool{}
	byName := map[string]string{}
	for d, n := range byDir {
		isUnit[n] = true
		byName[n] = d
	}
	w := workspace{root: root, byDir: byDir, byName: byName}
	units := make([]gen.Unit, len(dirs))
	for i, dir := range dirs {
		p := pkgs[i]
		deps, err := localDeps(dir, p, isUnit, byDir)
		if err != nil {
			return nil, err
		}
		more, files, err := w.scan(dir)
		if err != nil {
			return nil, err
		}
		deps = slices.Compact(slices.Sorted(slices.Values(append(deps, more...))))
		source, err := gen.Under(dir, "**")
		if err != nil {
			return nil, fmt.Errorf("package %s: %w", p.Name, err)
		}
		units[i] = gen.Unit{ID: p.Name, Source: append([]string{source}, files...), Deps: deps, Command: command(pm, p.Name)}
	}
	return units, nil
}

// manager returns the package manager of the one lockfile kind at root.
func manager(root string) (string, error) {
	var found []string
	for _, l := range lockfiles {
		if _, err := os.Stat(filepath.Join(root, l.file)); err == nil && !slices.Contains(found, l.manager) {
			found = append(found, l.manager)
		}
	}
	switch len(found) {
	case 0:
		return "", errors.New("no package-lock.json, npm-shrinkwrap.json, pnpm-lock.yaml or yarn.lock at the contract root: cannot tell the package manager")
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("lockfiles of %s at the contract root: keep one package manager", strings.Join(found, " and "))
}

// workspaces returns the workspace globs pm reads.
func workspaces(root, pm string) ([]string, error) {
	if pm == "pnpm" {
		file := filepath.Join(root, "pnpm-workspace.yaml")
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var ws struct{ Packages []string }
		if err := yaml.Unmarshal(data, &ws); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		return ws.Packages, nil
	}
	file := filepath.Join(root, "package.json")
	var m manifest
	if err := readJSON(file, &m); err != nil {
		return nil, err
	}
	var list []string
	if m.Workspaces == nil {
		return nil, nil
	}
	if err := json.Unmarshal(m.Workspaces, &list); err != nil {
		var obj struct{ Packages []string } // yarn classic: {"packages": [...]}
		if err := json.Unmarshal(m.Workspaces, &obj); err != nil {
			return nil, fmt.Errorf("%s: workspaces: want a list of globs or {packages: [...]}", file)
		}
		list = obj.Packages
	}
	return list, nil
}

// packageDirs returns the directories under root, slash-separated and
// relative to it, that hold a package.json and match a workspace glob and no
// "!" glob. node_modules and hidden directories are not walked.
func packageDirs(root string, patterns []string) ([]string, error) {
	var include, exclude []string
	for _, p := range patterns {
		neg := strings.HasPrefix(p, "!")
		p = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(p, "!"), "./"), "/")
		if err := contract.ValidateGlob(p); err != nil {
			return nil, fmt.Errorf("workspace glob: %w", err)
		}
		if neg {
			exclude = append(exclude, p)
		} else {
			include = append(include, p)
		}
	}
	if len(include) == 0 {
		return nil, errors.New("no workspaces declared: aval gen js makes one unit per workspace package")
	}
	matches := func(globs []string, dir string) bool {
		return slices.ContainsFunc(globs, func(g string) bool { return contract.Match(g, dir) })
	}
	var dirs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		switch {
		case !d.IsDir() || rel == ".":
			return nil
		case d.Name() == "node_modules" || strings.HasPrefix(d.Name(), "."):
			return filepath.SkipDir
		}
		if matches(include, rel) && !matches(exclude, rel) {
			switch _, err := os.Stat(filepath.Join(p, "package.json")); {
			case err == nil:
				dirs = append(dirs, rel)
			case !errors.Is(err, fs.ErrNotExist):
				return err
			}
		}
		return nil
	})
	return dirs, err
}

// localDeps returns the workspace packages p depends on: any dependency
// named like one (a range a workspace package satisfies links it), the
// target of a workspace: alias, and the package a file:, link: or portal:
// path points to, which must be a workspace package.
func localDeps(dir string, p manifest, isUnit map[string]bool, byDir map[string]string) ([]string, error) {
	var deps []string
	for _, m := range []map[string]string{p.Dependencies, p.DevDependencies, p.PeerDependencies, p.OptionalDependencies} {
		for name, spec := range m {
			if isUnit[name] {
				deps = append(deps, name)
			}
			proto, rest, _ := strings.Cut(spec, ":")
			switch proto {
			case "workspace":
				if i := strings.LastIndex(rest, "@"); i > 0 {
					name = rest[:i] // pnpm alias: workspace:@scope/pkg@*
				}
				if !isUnit[name] {
					return nil, fmt.Errorf("%s/package.json: %s: %q is not a workspace package", dir, name, spec)
				}
				deps = append(deps, name)
			case "file", "link", "portal":
				target, ok := byDir[path.Join(dir, filepath.ToSlash(rest))]
				if !ok {
					return nil, fmt.Errorf("%s/package.json: %s: %q is not a workspace package: add it to the workspaces or declare it in defaults by hand", dir, name, spec)
				}
				deps = append(deps, target)
			}
		}
	}
	return deps, nil
}

// command returns the argv that runs the test script of the workspace
// package name with pm.
func command(pm, name string) []string {
	switch pm {
	case "pnpm":
		return []string{"pnpm", "--filter", name, "test"}
	case "yarn":
		return []string{"yarn", "workspace", name, "test"}
	}
	return []string{"npm", "test", "--workspace", name}
}

func readJSON(file string, v any) error {
	data, err := os.ReadFile(file)
	if err == nil {
		err = json.Unmarshal(data, v)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	return nil
}
