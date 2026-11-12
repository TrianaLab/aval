// Package dotnet is the .NET adapter of aval gen: one unit per project
// (.csproj, .fsproj, .vbproj), read from the solution and project files
// themselves, so generating needs no .NET SDK.
package dotnet

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/gen"
)

// Adapter lists the projects of the solution at the contract root (.sln or
// .slnx), or of every project file under it when there is none, plus every
// project they reference. A unit's files are its project directory, the
// files it imports or includes from outside it and the Directory.Build.*
// and Directory.Packages.props files above it. Its test is
// dotnet test <project>.
type Adapter struct{}

var projectExt = map[string]bool{".csproj": true, ".fsproj": true, ".vbproj": true}

// implicit are the files MSBuild imports from a project's directory or the
// directories above it without the project naming them.
var implicit = []string{"Directory.Build.props", "Directory.Build.targets", "Directory.Packages.props"}

// notFiles are the items whose Include is a name, not a path.
var notFiles = map[string]bool{
	"PackageReference": true, "PackageVersion": true, "GlobalPackageReference": true,
	"FrameworkReference": true, "Reference": true, "Using": true, "InternalsVisibleTo": true,
	"AssemblyAttribute": true, "ProjectCapability": true, "SupportedPlatform": true,
	"DotNetCliToolReference": true,
}

// toolchain are the Import prefixes that point into the .NET SDK or MSBuild,
// which the hand-written environment covers.
var toolchain = []string{"$(MSBuildExtensionsPath", "$(MSBuildToolsPath", "$(MSBuildBinPath", "$(MSBuildSDKsPath", "$(VSToolsPath"}

var slnProject = regexp.MustCompile(`(?m)^Project\("[^"]*"\)\s*=\s*"[^"]*",\s*"([^"]*)"`)

// Units implements gen.Adapter.
func (Adapter) Units(root string) ([]gen.Unit, error) { return units(os.DirFS(root)) }

// units lists the units of the repository fsys.
func units(fsys fs.FS) ([]gen.Unit, error) {
	queue, err := projects(fsys)
	if err != nil {
		return nil, err
	}
	paths := map[string]string{} // ID -> project path
	var units []gen.Unit
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		id := name(p)
		if other, ok := paths[id]; ok {
			if other != p {
				return nil, fmt.Errorf("%s and %s: two projects named %s", other, p, id)
			}
			continue
		}
		paths[id] = p
		r := reader{fsys: fsys, dir: path.Dir(p), seen: map[string]bool{}}
		if err := r.project(p); err != nil {
			return nil, err
		}
		u := gen.Unit{ID: id, Source: r.src, Command: []string{"dotnet", "test", p}}
		for _, ref := range r.refs {
			u.Deps = append(u.Deps, name(ref))
		}
		units = append(units, u)
		queue = append(queue, r.refs...)
	}
	return units, nil
}

// name is a project's ID: its file name without the extension, as MSBuild
// names it.
func name(p string) string { return strings.TrimSuffix(path.Base(p), path.Ext(p)) }

// projects returns the project paths the solution at the root lists, or
// every project file under the root when it has no solution.
func projects(fsys fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var slns []string
	for _, e := range entries {
		if ext := path.Ext(e.Name()); ext == ".sln" || ext == ".slnx" {
			slns = append(slns, e.Name())
		}
	}
	switch len(slns) {
	case 0:
		return walk(fsys)
	case 1:
	default:
		return nil, fmt.Errorf("several solutions at the root (%s): keep one", strings.Join(slns, ", "))
	}
	data, err := fs.ReadFile(fsys, slns[0])
	if err != nil {
		return nil, err
	}
	var listed []string
	if path.Ext(slns[0]) == ".sln" {
		for _, m := range slnProject.FindAllStringSubmatch(string(data), -1) {
			listed = append(listed, m[1])
		}
	} else {
		var doc node
		if err := xml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", slns[0], err)
		}
		doc.each(func(n *node) {
			if n.XMLName.Local == "Project" {
				listed = append(listed, n.attr("Path"))
			}
		})
	}
	var out []string
	for _, l := range listed {
		p, err := resolve(".", l)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", slns[0], err)
		}
		if projectExt[strings.ToLower(path.Ext(p))] { // not a solution folder
			out = append(out, p)
		}
	}
	return out, nil
}

// walk returns every project file in fsys, skipping hidden, bin, obj and
// node_modules directories, in lexical order.
func walk(fsys fs.FS) ([]string, error) {
	var out []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		n := d.Name()
		if d.IsDir() && p != "." && (strings.HasPrefix(n, ".") || n == "bin" || n == "obj" || n == "node_modules") {
			return fs.SkipDir
		}
		if d.Type().IsRegular() && projectExt[strings.ToLower(path.Ext(n))] {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

// resolve returns v, a path from a solution or project file, relative to
// root, where base is the directory v is relative to. A path that MSBuild
// properties build, or that leaves the repository, is an error: Aval cannot
// prove what it names.
func resolve(base, v string) (string, error) {
	v = strings.ReplaceAll(strings.TrimSpace(v), `\`, "/")
	if strings.Contains(v, "$(") || strings.Contains(v, "@(") || strings.Contains(v, "%(") {
		return "", fmt.Errorf("%q: MSBuild properties in paths are not supported", v)
	}
	p := path.Clean(path.Join(base, v))
	if path.IsAbs(v) || (len(v) > 1 && v[1] == ':') || p == ".." || strings.HasPrefix(p, "../") {
		return "", fmt.Errorf("%q: outside the repository", v)
	}
	return p, nil
}

// reader collects one project's files and references.
type reader struct {
	fsys fs.FS           // the contract root
	dir  string          // the project's directory, relative to root
	seen map[string]bool // the MSBuild files already read
	src  []string
	refs []string
}

// project reads the project file p and the Directory.* files above it.
func (r *reader) project(p string) error {
	own := "**"
	if r.dir != "." {
		own = r.dir + "/**"
	}
	if strings.ContainsAny(r.dir, "*?[") {
		return fmt.Errorf("%s: the directory holds glob characters (*?[)", p)
	}
	if err := r.add(own, false); err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	for d := r.dir; ; d = path.Dir(d) {
		for _, f := range implicit {
			if err := r.file(path.Join(d, f), true); err != nil {
				return err
			}
		}
		if d == "." {
			break
		}
	}
	return r.file(p, false)
}

// file reads the MSBuild file p (a project, or a file it imports) and
// follows its references and imports. A missing file is skipped when
// optional.
func (r *reader) file(p string, optional bool) error {
	if r.seen[p] {
		return nil
	}
	r.seen[p] = true
	data, err := fs.ReadFile(r.fsys, p)
	if optional && errors.Is(err, fs.ErrNotExist) {
		// ponytail: an Import under a Condition that names a missing file;
		// once the file exists, aval gen --check reports the new source.
		return nil
	}
	if err != nil {
		return err
	}
	var doc node
	if err := xml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	if !r.inside(p) {
		if err := r.add(p, true); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	if err := r.node(&doc, "", path.Dir(p)); err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	return nil
}

// node handles n, a child of parent in an MSBuild file in directory this.
// Item paths are relative to the project; Import paths to the importing
// file.
func (r *reader) node(n *node, parent, this string) error {
	var err error
	switch local := n.XMLName.Local; {
	case local == "Import":
		err = r.imp(n, this)
	case local == "HintPath":
		err = r.path(this, n.Text, true)
	case parent != "ItemGroup" || notFiles[local]:
	case local == "ProjectReference":
		for _, v := range values(n.attr("Include")) {
			p, perr := r.resolve(r.dir, this, v)
			if perr == nil && !projectExt[strings.ToLower(path.Ext(p))] {
				perr = fmt.Errorf("ProjectReference %q: not a .csproj, .fsproj or .vbproj", v)
			}
			if perr == nil {
				r.refs = append(r.refs, p)
			}
			err = errors.Join(err, perr)
		}
	default:
		for _, v := range values(n.attr("Include")) {
			err = errors.Join(err, r.path(this, v, false))
		}
	}
	for i := range n.Nodes {
		err = errors.Join(err, r.node(&n.Nodes[i], n.XMLName.Local, this))
	}
	return err
}

// imp follows an Import of a local file.
func (r *reader) imp(n *node, this string) error {
	v := n.attr("Project")
	if v == "" || n.attr("Sdk") != "" || strings.Contains(v, "GetPathOfFileAbove") || slices.ContainsFunc(toolchain, func(t string) bool { return strings.HasPrefix(v, t) }) {
		// An SDK or toolchain file, or the Directory.* file above, which
		// project already reads up to the root.
		return nil
	}
	p, err := r.resolve(this, this, v)
	if err != nil {
		return err
	}
	return r.file(p, n.attr("Condition") != "")
}

// resolve is the package resolve for a path in an MSBuild file in directory
// this, relative to base unless it starts with $(MSBuildThisFileDirectory)
// or $(MSBuildProjectDirectory).
func (r *reader) resolve(base, this, v string) (string, error) {
	v = strings.TrimSpace(v)
	if rest, ok := strings.CutPrefix(v, "$(MSBuildThisFileDirectory)"); ok {
		base, v = this, rest
	} else if rest, ok := strings.CutPrefix(v, "$(MSBuildProjectDirectory)"); ok {
		base, v = r.dir, "./"+rest
	}
	return resolve(base, v)
}

// path adds the file or MSBuild wildcard v, an item path in an MSBuild file
// in directory this, when it lies outside the project directory, which the
// unit's own glob covers.
func (r *reader) path(this, v string, literal bool) error {
	p, err := r.resolve(r.dir, this, v)
	if err != nil || r.inside(p) {
		return err
	}
	return r.add(p, literal)
}

// inside reports whether p lies in the project directory.
func (r *reader) inside(p string) bool {
	return r.dir == "." || strings.HasPrefix(p, r.dir+"/")
}

// add adds the glob g to the unit's files. A literal path must not hold
// glob characters, and no path may hold "[": Aval would read either as a
// pattern and could miss the file.
func (r *reader) add(g string, literal bool) error {
	meta := "["
	if literal {
		meta = "*?["
	}
	if strings.ContainsAny(g, meta) {
		return fmt.Errorf("%q: the path holds glob characters (%s)", g, meta)
	}
	if err := contract.ValidateGlob(g); err != nil {
		return err
	}
	if !slices.Contains(r.src, g) {
		r.src = append(r.src, g)
	}
	return nil
}

// values yields the non-empty entries of an MSBuild list ("a;b").
func values(s string) []string {
	var out []string
	for v := range strings.SplitSeq(s, ";") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// node is any XML element.
type node struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Text    string     `xml:",chardata"`
	Nodes   []node     `xml:",any"`
}

func (n *node) attr(name string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (n *node) each(f func(*node)) {
	f(n)
	for i := range n.Nodes {
		n.Nodes[i].each(f)
	}
}
