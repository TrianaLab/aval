// Package rust is the aval gen adapter for Cargo workspaces: one unit per
// workspace member, read from cargo metadata, with path dependencies between
// members as edges.
package rust

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/TrianaLab/aval/internal/gen"
)

// cargo is the tool the adapter runs; tests point it at a fake.
var cargo = "cargo"

// Adapter lists the members of the Cargo workspace found from the contract
// root. Registry and git dependencies are not units: Cargo.lock, in the
// hand-written defaults, covers them.
type Adapter struct{}

type metadata struct {
	WorkspaceRoot string `json:"workspace_root"`
	Packages      []struct {
		Name         string `json:"name"`
		ManifestPath string `json:"manifest_path"`
		Dependencies []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"dependencies"`
	} `json:"packages"`
}

// Units runs cargo metadata --no-deps in root (no network, no build
// scripts) and returns one unit per member: its directory, its path
// dependencies, and cargo test -p <member>. A path dependency that is not a
// member, or a member outside root, is an error: its files would be in no
// scope (invariant 3). So is a [patch] or .cargo/config.toml paths override
// that points outside every member; one inside a member makes every member
// depend on it.
func (Adapter) Units(root string) ([]gen.Unit, error) {
	root, err := gen.Canonical(root) // cargo prints canonical, absolute paths
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(cargo, "metadata", "--format-version", "1", "--no-deps")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("cargo metadata: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var m metadata
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, fmt.Errorf("cargo metadata: %w", err)
	}
	byDir := map[string]string{} // member directory -> name
	for _, p := range m.Packages {
		byDir[filepath.Dir(p.ManifestPath)] = p.Name
	}
	if m.WorkspaceRoot == "" {
		return nil, errors.New("cargo metadata: no workspace_root")
	}
	shared, err := overridden(m.WorkspaceRoot, byDir)
	if err != nil {
		return nil, err
	}
	units := make([]gen.Unit, 0, len(m.Packages))
	for _, p := range m.Packages {
		dir, err := filepath.Rel(root, filepath.Dir(p.ManifestPath))
		if err != nil || !filepath.IsLocal(dir) {
			return nil, fmt.Errorf("member %s: %s is outside the contract root %s", p.Name, p.ManifestPath, root)
		}
		source, err := gen.Under(filepath.ToSlash(dir), "**")
		if err != nil {
			return nil, fmt.Errorf("member %s: %w", p.Name, err)
		}
		u := gen.Unit{ID: p.Name, Source: []string{source}, Command: []string{"cargo", "test", "-p", p.Name}}
		for _, d := range p.Dependencies {
			if d.Path == "" {
				continue
			}
			name, ok := byDir[filepath.Clean(d.Path)]
			if !ok {
				return nil, fmt.Errorf("member %s: path dependency %s at %s is not a workspace member: add it to [workspace] members", p.Name, d.Name, d.Path)
			}
			// ponytail: dev-dependencies are edges like the others, so the
			// dependents of a unit also use its dev-dependencies' files: more
			// re-runs, never a false hit. Precise only if the core learns
			// edges that do not propagate.
			u.Deps = append(u.Deps, name)
		}
		// ponytail: an override applies to every member that depends on the
		// crate it replaces; every member gets the edge. Exact if the patched
		// crate names are matched against each member's dependencies.
		u.Deps = append(u.Deps, shared...)
		units = append(units, u)
	}
	return units, nil
}

// overridden returns the members that hold the path overrides of the
// workspace, which cargo metadata --no-deps does not show: path entries of
// [patch] tables in Cargo.toml and .cargo/config.toml, and paths in
// .cargo/config.toml, all relative to ws. An override outside every member
// is an error naming its file.
// ponytail: only the workspace root's files; .cargo/config.toml in parent
// directories and in CARGO_HOME are not read.
func overridden(ws string, byDir map[string]string) ([]string, error) {
	var shared []string
	for _, f := range []struct {
		name   string
		config bool
	}{{"Cargo.toml", false}, {".cargo/config.toml", true}, {".cargo/config", true}} {
		file := filepath.Join(ws, filepath.FromSlash(f.name))
		paths, err := scan(file, f.config)
		if err != nil {
			return nil, err
		}
		for _, p := range paths {
			dir := filepath.Clean(p)
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(ws, dir)
			}
			if real, err := filepath.EvalSymlinks(dir); err == nil {
				dir = real
			}
			best := ""
			for mdir := range byDir {
				if rel, err := filepath.Rel(mdir, dir); err == nil && filepath.IsLocal(rel) && len(mdir) > len(best) {
					best = mdir
				}
			}
			if best == "" {
				return nil, fmt.Errorf("%s: path override %s is not inside a workspace member: its files would be in no scope; make it a member or put it in defaults and remove the override", file, p)
			}
			shared = append(shared, byDir[best])
		}
	}
	return shared, nil
}

var (
	pathKey  = regexp.MustCompile(`(?:^|[\s{,.])["']?path["']?\s*=`)
	pathVal  = regexp.MustCompile(`(?:^|[\s{,.])["']?path["']?\s*=\s*("(?:[^"\\]|\\.)*"|'[^']*')`)
	pathsKey = regexp.MustCompile(`^["']?paths["']?\s*=\s*`)
	patchKey = regexp.MustCompile(`^["']?patch["']?\s*[.=]`)
	strLit   = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'[^']*'`)
)

// scan returns the path strings of the [patch] tables of a TOML file and,
// for a cargo config, of its top-level paths array. A missing file has
// none. It reads the TOML cargo writes by hand line by line; a path key
// whose value is not a one-line string is an error, so an override it cannot
// read is never skipped.
func scan(file string, config bool) ([]string, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	table, inPaths := "", false
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(uncomment(line))
		bad := fmt.Errorf("%s:%d: cannot read the path override %q: write it as a one-line string", file, i+1, line)
		var vals []string
		switch {
		case inPaths:
			vals, inPaths = strLit.FindAllString(line, -1), open(line)
		case strings.HasPrefix(line, "["):
			table = strings.NewReplacer("[", "", "]", "", " ", "", "\t", "", `"`, "", "'", "").Replace(line)
		case config && table == "" && pathsKey.MatchString(line):
			line = pathsKey.ReplaceAllString(line, "")
			if !strings.HasPrefix(line, "[") {
				return nil, bad
			}
			vals, inPaths = strLit.FindAllString(line, -1), open(line)
		case patch(table, line):
			subs := pathVal.FindAllStringSubmatch(line, -1)
			if len(subs) != len(pathKey.FindAllString(line, -1)) {
				return nil, bad
			}
			for _, v := range subs {
				vals = append(vals, v[1])
			}
		}
		for _, v := range vals {
			p, err := unquote(v)
			if err != nil {
				return nil, bad
			}
			out = append(out, p)
		}
	}
	return out, nil
}

// patch reports whether line, in table, is part of a [patch] table.
func patch(table, line string) bool {
	return table == "patch" || strings.HasPrefix(table, "patch.") || table == "" && patchKey.MatchString(line)
}

// open reports whether a line of the paths array leaves it open: no ]
// outside its strings.
func open(line string) bool {
	return !strings.Contains(strLit.ReplaceAllString(line, ""), "]")
}

// uncomment drops a # comment that is outside a string.
func uncomment(line string) string {
	var quote rune
	escaped := false
	for i, r := range line {
		switch {
		case escaped:
			escaped = false
		case quote == '"' && r == '\\':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#':
			return line[:i]
		}
	}
	return line
}

// unquote reads a TOML basic ("...") or literal ('...') string.
func unquote(s string) (string, error) {
	if strings.HasPrefix(s, "'") {
		return s[1 : len(s)-1], nil
	}
	return strconv.Unquote(s)
}
