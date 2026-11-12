// Package rust is the aval gen adapter for Cargo workspaces: one unit per
// workspace member, read from cargo metadata, with path dependencies between
// members as edges.
package rust

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
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
	Packages []struct {
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
// scope (invariant 3).
func (Adapter) Units(root string) ([]gen.Unit, error) {
	root, err := filepath.EvalSymlinks(root) // cargo prints canonical, absolute paths
	if err == nil {
		root, err = filepath.Abs(root)
	}
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
	units := make([]gen.Unit, 0, len(m.Packages))
	for _, p := range m.Packages {
		dir, err := filepath.Rel(root, filepath.Dir(p.ManifestPath))
		if err != nil || !filepath.IsLocal(dir) {
			return nil, fmt.Errorf("member %s: %s is outside the contract root %s", p.Name, p.ManifestPath, root)
		}
		source := "**"
		if dir != "." {
			source = filepath.ToSlash(dir) + "/**"
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
		units = append(units, u)
	}
	return units, nil
}
