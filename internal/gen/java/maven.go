package java

import (
	"encoding/xml"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/TrianaLab/aval/internal/gen"
)

type coord struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
}

// parentRef is <parent>. RelativePath is nil when the element is absent
// (Maven reads ../pom.xml) and "" for <relativePath/> (the repository).
type parentRef struct {
	coord
	RelativePath *string `xml:"relativePath"`
}

type plugin struct {
	coord
	Dependencies []coord `xml:"dependencies>dependency"`
}

type build struct {
	SourceDirectory     string   `xml:"sourceDirectory"`
	TestSourceDirectory string   `xml:"testSourceDirectory"`
	Resources           []string `xml:"resources>resource>directory"`
	TestResources       []string `xml:"testResources>testResource>directory"`
	Plugins             []plugin `xml:"plugins>plugin"`
}

// model is what a POM and each of its profiles share. dependencyManagement
// and pluginManagement are left out on purpose: they declare versions, not
// dependencies, and a parent that manages every module would otherwise
// depend on all of them.
type model struct {
	Modules      []string `xml:"modules>module"`
	Subprojects  []string `xml:"subprojects>subproject"` // Maven 4
	Dependencies []coord  `xml:"dependencies>dependency"`
	Build        build    `xml:"build"`
}

type pom struct {
	XMLName xml.Name
	coord
	Parent     parentRef `xml:"parent"`
	Properties props     `xml:"properties"`
	model
	Profiles []model `xml:"profiles>profile"`
}

// props holds the children of <properties> by element name.
type props map[string]string

func (p *props) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*p = props{}
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			var v string
			if err := d.DecodeElement(&v, &t); err != nil {
				return err
			}
			(*p)[t.Name.Local] = strings.TrimSpace(v)
		case xml.EndElement:
			return nil
		}
	}
}

// models returns the POM's own model followed by its profiles: a module or
// dependency in any profile counts, since a profile may be active.
func (p *pom) models() []model { return append([]model{p.model}, p.Profiles...) }

func (p *pom) group() string {
	if p.GroupID != "" {
		return p.GroupID
	}
	return p.Parent.GroupID
}

// reactor is a Maven multi-module build: every POM by directory ("" is the
// root), and each local POM by its literal coordinates for parent lookup.
type reactor struct {
	root    string
	poms    map[string]*pom
	byCoord map[coord]*pom
}

// maven walks <modules> (and Maven 4 <subprojects>) from the root POM, in
// the root project and every profile, and returns one unit per module.
func maven(root string) ([]gen.Unit, error) {
	r := reactor{root: root, poms: map[string]*pom{}, byCoord: map[coord]*pom{}}
	var dirs []string
	seen := map[string]bool{"": true}
	for queue := []string{""}; len(queue) > 0; queue = queue[1:] {
		dir := queue[0]
		p, err := readPom(root, dir)
		if err != nil {
			return nil, err
		}
		r.poms[dir] = p
		r.byCoord[coord{p.group(), p.ArtifactID}] = p
		if dir != "" {
			dirs = append(dirs, dir)
		}
		for _, m := range p.models() {
			for _, mod := range slices.Concat(m.Modules, m.Subprojects) {
				d := path.Join(dir, filepath.ToSlash(strings.TrimSpace(mod)))
				if strings.HasSuffix(d, ".xml") {
					return nil, fmt.Errorf("%s: module %q names a POM file: %w, list its directory", pomPath(dir), mod, errUnsupported)
				}
				if err := checkDir(d); err != nil {
					return nil, fmt.Errorf("%s: module %q: %w", pomPath(dir), mod, err)
				}
				if !seen[d] {
					seen[d] = true
					queue = append(queue, d)
				}
			}
		}
	}
	index := map[string]string{} // groupId:artifactId -> module directory
	for _, dir := range dirs {
		p := r.poms[dir]
		key, err := r.key(p, coord{p.group(), p.ArtifactID})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", pomPath(dir), err)
		}
		if other, dup := index[key]; dup {
			return nil, fmt.Errorf("modules %s and %s are both %s", other, dir, key)
		}
		index[key] = dir
	}
	mvn := tool(root, "mvnw", "mvn")
	units := make([]gen.Unit, 0, len(dirs))
	for _, dir := range dirs {
		deps, err := r.deps(r.poms[dir], dir, index)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", pomPath(dir), err)
		}
		units = append(units, gen.Unit{
			ID:      dir,
			Source:  source(root, dir, "pom.xml"),
			Deps:    deps,
			Command: []string{mvn, "-B", "-pl", dir, "test"},
		})
	}
	return units, nil
}

func pomPath(dir string) string { return path.Join(dir, "pom.xml") }

func readPom(root, dir string) (*pom, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(pomPath(dir))))
	if err != nil {
		return nil, err
	}
	var p pom
	if err := xml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", pomPath(dir), err)
	}
	if p.XMLName.Local != "project" {
		return nil, fmt.Errorf("%s: the root element is <%s>, want <project>", pomPath(dir), p.XMLName.Local)
	}
	for _, m := range p.models() {
		b := m.Build
		for _, d := range slices.Concat([]string{b.SourceDirectory, b.TestSourceDirectory}, b.Resources, b.TestResources) {
			if err := checkSrc(d); err != nil {
				return nil, fmt.Errorf("%s: %w", pomPath(dir), err)
			}
		}
	}
	return &p, nil
}

// checkSrc rejects a source or resource directory outside src, which the
// generated globs (pom.xml and src/**) would miss.
func checkSrc(d string) error {
	d = strings.TrimSpace(d)
	if d == "" {
		return nil
	}
	c := d
	for _, prefix := range []string{"${project.basedir}/", "${basedir}/"} {
		c = strings.TrimPrefix(c, prefix)
	}
	if c = path.Clean(filepath.ToSlash(c)); c != "src" && !strings.HasPrefix(c, "src/") {
		return fmt.Errorf("source or resource directory %q outside src: %w", d, errUnsupported)
	}
	return nil
}

// deps returns the modules p refers to: its parent, its dependencies and
// its build plugins and their dependencies, in the project and in every
// profile.
func (r reactor) deps(p *pom, dir string, index map[string]string) ([]string, error) {
	if err := r.checkParent(p, dir); err != nil {
		return nil, err
	}
	refs := []coord{p.Parent.coord}
	for _, m := range p.models() {
		refs = append(refs, m.Dependencies...)
		for _, pl := range m.Build.Plugins {
			if pl.GroupID == "" {
				pl.GroupID = "org.apache.maven.plugins"
			}
			refs = append(refs, pl.coord)
			refs = append(refs, pl.Dependencies...)
		}
	}
	var deps []string
	for _, c := range refs {
		if c.ArtifactID == "" {
			continue // no <parent>
		}
		key, err := r.key(p, c)
		if err != nil {
			return nil, err
		}
		if d, ok := index[key]; ok && d != dir {
			deps = append(deps, d)
		}
	}
	return deps, nil
}

// checkParent rejects a parent POM read from disk inside the root that is
// neither the root POM nor a module: no unit covers its file, so a change to
// it would not change the fingerprint.
func (r reactor) checkParent(p *pom, dir string) error {
	rel := ".."
	if p.Parent.RelativePath != nil {
		rel = strings.TrimSpace(*p.Parent.RelativePath)
	}
	if p.Parent.ArtifactID == "" || rel == "" {
		return nil
	}
	f := path.Join(dir, filepath.ToSlash(rel))
	if info, err := os.Stat(filepath.Join(r.root, filepath.FromSlash(f))); err == nil && info.IsDir() {
		f = path.Join(f, "pom.xml")
	}
	if f == ".." || strings.HasPrefix(f, "../") || !isFile(filepath.Join(r.root, filepath.FromSlash(f))) {
		return nil // outside the root or not on disk: resolved from a repository
	}
	if d := strings.TrimSuffix(path.Dir(f), "."); path.Base(f) == "pom.xml" && r.poms[d] != nil {
		return nil
	}
	return fmt.Errorf("parent %s is a local POM outside the build: %w, list its directory in <modules>", f, errUnsupported)
}

var propRe = regexp.MustCompile(`\$\{([^}]*)\}`)

// key returns groupId:artifactId of c with the properties of p resolved. A
// property it cannot resolve is an error: guessing could drop an edge.
func (r reactor) key(p *pom, c coord) (string, error) {
	s := strings.TrimSpace(c.GroupID) + ":" + strings.TrimSpace(c.ArtifactID)
	s = propRe.ReplaceAllStringFunc(s, func(m string) string {
		if v, ok := r.prop(p, m[2:len(m)-1]); ok {
			return v
		}
		return m
	})
	if strings.Contains(s, "${") {
		return "", fmt.Errorf("cannot resolve the properties in %s", s)
	}
	return s, nil
}

// prop resolves a property from p, then from its local parents.
func (r reactor) prop(p *pom, name string) (string, bool) {
	switch name {
	case "project.groupId", "pom.groupId", "groupId":
		return p.group(), true
	case "project.artifactId", "pom.artifactId", "artifactId":
		return p.ArtifactID, true
	case "project.parent.groupId":
		return p.Parent.GroupID, true
	}
	// ponytail: depth cap instead of cycle tracking; a deeper local parent chain is not real.
	for q, depth := p, 0; q != nil && depth < 32; q, depth = r.byCoord[q.Parent.coord], depth+1 {
		if v, ok := q.Properties[name]; ok {
			return v, true
		}
	}
	return "", false
}
