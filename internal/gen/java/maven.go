package java

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

// model is what a POM and each of its profiles share.
type model struct {
	Properties  props    `xml:"properties"`
	Modules     []string `xml:"modules>module"`
	Subprojects []string `xml:"subprojects>subproject"` // Maven 4
}

type pom struct {
	XMLName xml.Name
	coord
	Parent parentRef `xml:"parent"`
	model
	Profiles []model `xml:"profiles>profile"`
	refs     []coord // every coordinate the POM names, see coordRefs
	texts    []elem  // every element text, for checkPath
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
	config  map[string]string   // user property -> the .mvn file that sets it
	defined map[string][]string // every property the reactor POMs define -> its values
}

// maven walks <modules> (and Maven 4 <subprojects>) from the root POM, in
// the root project and every profile, and returns one unit per module.
func maven(root string) ([]gen.Unit, error) {
	config, err := mavenConfig(root)
	if err != nil {
		return nil, err
	}
	r := reactor{root: root, poms: map[string]*pom{}, byCoord: map[coord]*pom{}, config: config}
	dirs, err := r.walk()
	if err != nil {
		return nil, err
	}
	index, err := r.index(dirs)
	if err != nil {
		return nil, err
	}
	modules := map[string]bool{}
	for _, d := range dirs {
		modules[d] = true
	}
	if err := r.checkPaths(modules); err != nil {
		return nil, err
	}
	shared, err := rootContext(root, modules)
	if err != nil {
		return nil, err
	}
	strays, err := r.strays()
	if err != nil {
		return nil, err
	}
	mvn := tool(root, "mvnw", "mvn")
	units := make([]gen.Unit, 0, len(dirs))
	for _, dir := range dirs {
		deps, err := r.deps(r.poms[dir], dir, index, strays)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", pomPath(dir), err)
		}
		units = append(units, gen.Unit{
			ID:      dir,
			Source:  append(slices.Clone(shared), under(dir, "**")),
			Deps:    deps,
			Command: []string{mvn, "-B", "-pl", dir, "test"},
		})
	}
	return units, nil
}

// walk reads the root POM and every module it reaches, in the root project
// and every profile, and returns the module directories.
func (r reactor) walk() ([]string, error) {
	var dirs []string
	seen := map[string]bool{"": true}
	for queue := []string{""}; len(queue) > 0; queue = queue[1:] {
		dir := queue[0]
		p, err := readPom(r.root, dir)
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
	return dirs, nil
}

// checkPaths runs checkPath on every element of the root POM and the
// modules.
func (r *reactor) checkPaths(modules map[string]bool) error {
	r.defined = map[string][]string{}
	for _, p := range r.poms {
		for _, m := range p.models() {
			for k, v := range m.Properties {
				r.defined[k] = append(r.defined[k], v)
			}
		}
	}
	for dir, p := range r.poms {
		for _, e := range p.texts {
			err := r.checkPath(e, dir, modules)
			if err != nil && dir == "" && strings.Contains(strings.ReplaceAll(e.text, `\`, "/"), "../") {
				err = fmt.Errorf("%w; for a file of the root write ${maven.multiModuleProjectDirectory}/", err)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", pomPath(dir), err)
			}
		}
	}
	return nil
}

// index returns the module directories by groupId:artifactId.
func (r reactor) index(dirs []string) (map[string]string, error) {
	index := map[string]string{}
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
	return index, nil
}

// strays indexes the pom.xml files under the root that are not in the
// reactor, by groupId:artifactId. It skips target, build, node_modules and
// hidden directories at any depth: a POM there is build output, a
// downloaded package or tool state, not a source Maven builds. A module
// named target or build is still read, through <modules>. A module that
// refers to one would depend on files no unit covers, so deps rejects it.
// A POM it cannot parse or resolve is no build Maven could resolve a
// reference to, and is left out.
func (r reactor) strays() (map[string]string, error) {
	strays := map[string]string{}
	err := filepath.WalkDir(r.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(r.root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if n := d.Name(); rel != "." && (n == "target" || n == "build" || n == "node_modules" || strings.HasPrefix(n, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		dir := strings.TrimSuffix(path.Dir(rel), ".")
		if d.Name() != "pom.xml" || r.poms[dir] != nil {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var q pom
		if xml.Unmarshal(data, &q) != nil {
			return nil
		}
		if key, err := r.key(&q, coord{q.group(), q.ArtifactID}); err == nil {
			strays[key] = rel
		}
		return nil
	})
	return strays, err
}

// mavenConfig returns the properties .mvn/maven.config (-D, --define) and
// .mvn/jvm.config (-D system properties) set, which override the POM's,
// with the file that sets each.
func mavenConfig(root string) (map[string]string, error) {
	keys := map[string]string{}
	for _, f := range []string{".mvn/maven.config", ".mvn/jvm.config"} {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		args := strings.Fields(string(data))
		for i, a := range args {
			switch {
			case (a == "-D" || a == "--define") && i+1 < len(args):
				a = args[i+1]
			case strings.HasPrefix(a, "--define="):
				a = a[len("--define="):]
			case strings.HasPrefix(a, "-D"):
				a = a[2:]
			default:
				continue
			}
			k, _, _ := strings.Cut(a, "=")
			keys[k] = f
		}
	}
	return keys, nil
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
	if p.refs, p.texts, err = coordRefs(data); err != nil { // also rejects what follows </project>
		return nil, fmt.Errorf("%s: %w", pomPath(dir), err)
	}
	if p.XMLName.Local != "project" {
		return nil, fmt.Errorf("%s: the root element is <%s>, want <project>", pomPath(dir), p.XMLName.Local)
	}
	return &p, nil
}

// elem is the trimmed text of a POM element (a property value too).
type elem struct{ name, text string }

// rootStartRe matches a value that starts with a property naming the build
// root (the working directory under mvn -pl included).
var rootStartRe = regexp.MustCompile(`^\$\{(maven\.multiModuleProjectDirectory|rootDirectory|project\.rootDirectory|session\.rootDirectory|session\.executionRootDirectory|user\.dir)\}(/|$)`)

// rootVar reports whether a property name points outside the module: the
// build root, the working directory, the environment, the session or a
// parent project (its coordinates excepted: they are not files).
func rootVar(name string) bool {
	switch name {
	case "user.dir", "maven.multiModuleProjectDirectory", "project.rootDirectory", "rootDirectory":
		return true
	case "project.parent.groupId", "project.parent.artifactId", "project.parent.version",
		"pom.parent.groupId", "pom.parent.artifactId", "pom.parent.version":
		return false
	}
	for _, prefix := range []string{"env.", "session.", "project.parent.", "pom.parent."} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// builtin reports whether Maven or the JVM sets the property name.
func builtin(name string) bool {
	for _, prefix := range []string{"project.", "pom.", "settings.", "maven.", "java.", "os.", "user.", "env.", "session."} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return name == "basedir"
}

// rootish reports whether a property points outside the module: rootVar,
// or a reactor property whose value uses one (main.basedir set to
// ${maven.multiModuleProjectDirectory}). A property defined nowhere in the
// reactor, nor by Maven, is set at run time (directory-maven-plugin) and
// counts too.
func (r reactor) rootish(name string, depth int) bool {
	vals, ok := r.defined[name]
	if rootVar(name) || !ok && !builtin(name) || depth > 8 {
		return true
	}
	for _, v := range vals {
		for _, m := range propRe.FindAllStringSubmatch(v, -1) {
			if r.rootish(m[1], depth+1) {
				return true
			}
		}
	}
	return false
}

// checkPath rejects the text of an element of the POM of the module in dir
// ("" for the root POM) that refers to files outside the module, which its
// <dir>/** glob would miss: filters, suiteXmlFile, profile activation
// files, systemPath, an argLine with -Dx=../y. A backslash counts as a
// separator. It rejects "../" anywhere, an absolute value, a rootish
// property next to a separator (${env.X} alone is fine), and a path that
// starts with another module's directory. A value that starts with a root
// property (${maven.multiModuleProjectDirectory}/x) is resolved against
// the root first; files there outside every module are in the root context.
func (r reactor) checkPath(e elem, dir string, modules map[string]bool) error {
	t := strings.ReplaceAll(e.text, `\`, "/")
	if m := rootStartRe.FindString(t); m != "" {
		t = strings.TrimPrefix(t[len(m):], "/")
		if c := path.Clean(t); c == ".." || strings.HasPrefix(c, "../") || foreign(c, dir, modules) {
			return pathErr(e)
		}
	} else if outside(t) {
		return pathErr(e)
	}
	if strings.Contains(t, "../") || r.rootishPath(t) {
		return pathErr(e)
	}
	for _, prefix := range []string{"${project.basedir}/", "${basedir}/"} {
		t = strings.TrimPrefix(t, prefix)
	}
	if strings.Contains(t, "/") && foreign(path.Clean(t), dir, modules) {
		return pathErr(e)
	}
	return nil
}

// rootishPath reports whether t has a rootish property next to a "/".
func (r reactor) rootishPath(t string) bool {
	for _, loc := range propRe.FindAllStringSubmatchIndex(t, -1) {
		sep := loc[1] < len(t) && t[loc[1]] == '/' || loc[0] > 0 && t[loc[0]-1] == '/'
		if sep && r.rootish(t[loc[2]:loc[3]], 0) {
			return true
		}
	}
	return false
}

func pathErr(e elem) error {
	return fmt.Errorf("<%s>%s</%s> refers to files outside the module directory: %w", e.name, e.text, e.name, errUnsupported)
}

// outside reports whether a path, relative to the module directory, is
// absolute or leaves it.
func outside(p string) bool {
	for _, prefix := range []string{"${project.basedir}/", "${basedir}/", "${projectDir}/", "${project.projectDir}/", "$projectDir/"} {
		p = strings.TrimPrefix(p, prefix)
	}
	s := filepath.ToSlash(p)
	c := path.Clean(s)
	return c == ".." || strings.HasPrefix(c, "../") || absolute(s)
}

// absolute reports whether s is an absolute path: /x or C:\x (a lone "/"
// is a separator, not a path).
func absolute(s string) bool {
	return len(s) > 1 && s[0] == '/' && isAlnum(s[1]) ||
		len(s) > 2 && s[1] == ':' && (s[2] == '/' || s[2] == '\\') && isAlnum(s[0])
}

func isAlnum(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// coordRefs returns every element of a POM with an <artifactId> child,
// wherever it is: <parent>, dependencies, plugins and their dependencies,
// extensions, reporting plugins, annotationProcessorPaths, artifactItems,
// pluginManagement, in the project and in every profile. Reading only the
// places Maven itself resolves would drop the edge of a local module named
// in a plugin's configuration, and a change to that module would then be
// reused. A managed plugin is an edge even if no module uses it: more
// re-runs, never a false hit. It also rejects any element that refers to
// files outside the module: it returns the text of every element but
// relativePath and modules (checked elsewhere) for checkPath.
func coordRefs(data []byte) ([]coord, []elem, error) {
	var refs []coord
	var texts []elem
	var stack []*frame
	d := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return refs, texts, nil
		}
		if err != nil {
			return nil, nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, open(stack, t.Name.Local))
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text.Write(t)
			}
		case xml.EndElement:
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if v := strings.TrimSpace(f.text.String()); v != "" && f.name != "relativePath" && f.name != "module" && f.name != "subproject" {
				texts = append(texts, elem{f.name, v})
			}
			if len(stack) == 0 {
				continue // the project's own coordinates
			}
			if c, ok := f.close(stack[len(stack)-1]); ok {
				refs = append(refs, c)
			}
		}
	}
}

// frame is an open POM element: its name, whether it is inside
// dependencyManagement or a subtree whose coordinates are no reference
// (exclusions remove artifacts, properties are free-form names), the
// coordinates and scope of its children and its text.
type frame struct {
	name  string
	dm    bool
	noRef bool
	c     coord
	scope string
	text  strings.Builder
}

// open returns a frame for element name, a child of the top of stack.
func open(stack []*frame, name string) *frame {
	f := &frame{name: name, dm: name == "dependencyManagement", noRef: name == "exclusions" || name == "properties"}
	if len(stack) > 0 {
		up := stack[len(stack)-1]
		f.dm, f.noRef = f.dm || up.dm, f.noRef || up.noRef
	}
	return f
}

// close hands a groupId, artifactId or scope to the enclosing element up and
// returns f's own coordinates when it has an artifactId and is a reference:
// outside exclusions and properties and, inside dependencyManagement, an
// imported BOM (it sets the versions the module resolves).
func (f *frame) close(up *frame) (coord, bool) {
	switch f.name {
	case "groupId":
		up.c.GroupID = strings.TrimSpace(f.text.String())
	case "artifactId":
		up.c.ArtifactID = strings.TrimSpace(f.text.String())
	case "scope":
		up.scope = strings.TrimSpace(f.text.String())
	}
	if f.c.GroupID == "" && f.name == "plugin" {
		f.c.GroupID = "org.apache.maven.plugins"
	}
	return f.c, f.c.ArtifactID != "" && !f.noRef && (!f.dm || f.scope == "import")
}

// deps returns the local modules p refers to (see coordRefs), its own
// references and those it inherits from its local parents and the root POM
// (assumed inherited by every module: more re-runs, never a false hit), all
// resolved in p's context as Maven does. A reference to a local POM outside
// the reactor is an error.
func (r reactor) deps(p *pom, dir string, index, strays map[string]string) ([]string, error) {
	if err := r.checkParent(p, dir); err != nil {
		return nil, err
	}
	var deps []string
	for _, q := range r.chain(p) {
		for _, c := range q.refs {
			key, err := r.key(p, c)
			if err != nil {
				return nil, err
			}
			if d, ok := index[key]; ok {
				if d != dir {
					deps = append(deps, d)
				}
			} else if f, ok := strays[key]; ok {
				return nil, fmt.Errorf("%s refers to %s, a local POM outside the build: %w, list its directory in <modules>", key, f, errUnsupported)
			}
		}
		deps = append(deps, r.textRefs(p, q, dir, index)...)
	}
	return slices.Compact(slices.Sorted(slices.Values(deps))), nil
}

// textRefs returns the modules q's element texts name as one
// groupId:artifactId[:...] string (a resourceBundle, dependenciesToScan,
// an <artifact>), properties resolved in p's context where they can be.
func (r reactor) textRefs(p, q *pom, dir string, index map[string]string) []string {
	var deps []string
	for _, e := range q.texts {
		t := propRe.ReplaceAllStringFunc(e.text, func(m string) string {
			if v, ok, err := r.prop(p, m[2:len(m)-1]); ok && err == nil {
				return v
			}
			return m
		})
		for key, d := range index {
			for i := strings.Index(t, key); i >= 0 && d != dir; i = indexFrom(t, key, i+1) {
				end := i + len(key)
				if (i == 0 || !coordByte(t[i-1])) && (end == len(t) || t[end] == ':') {
					deps = append(deps, d)
					break
				}
			}
		}
	}
	return deps
}

// indexFrom is strings.Index from position i, or -1.
func indexFrom(s, sub string, i int) int {
	if j := strings.Index(s[i:], sub); j >= 0 {
		return i + j
	}
	return -1
}

// coordByte reports whether b can be part of a groupId or artifactId.
func coordByte(b byte) bool { return isAlnum(b) || b == '.' || b == '-' || b == '_' }

// chain returns p, its local parents and, when it is not one of them, the
// root POM.
func (r reactor) chain(p *pom) []*pom {
	var out []*pom
	// ponytail: depth cap instead of cycle tracking; a deeper local parent chain is not real.
	for q, depth := p, 0; q != nil && depth < 32; q, depth = r.byCoord[q.Parent.coord], depth+1 {
		out = append(out, q)
	}
	if root := r.poms[""]; !slices.Contains(out, root) {
		out = append(out, root)
	}
	return out
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
// property it cannot resolve, or that a profile sets (the value depends on
// which profiles are active), is an error: guessing could drop an edge.
func (r reactor) key(p *pom, c coord) (string, error) {
	var err error
	s := strings.TrimSpace(c.GroupID) + ":" + strings.TrimSpace(c.ArtifactID)
	s = propRe.ReplaceAllStringFunc(s, func(m string) string {
		v, ok, perr := r.prop(p, m[2:len(m)-1])
		if perr != nil {
			err = perr
		}
		if ok {
			return v
		}
		return m
	})
	if err != nil {
		return "", err
	}
	if strings.Contains(s, "${") {
		return "", fmt.Errorf("cannot resolve the properties in %s", s)
	}
	return s, nil
}

// prop resolves a property from p, then from its local parents and the
// root POM.
func (r reactor) prop(p *pom, name string) (string, bool, error) {
	switch name {
	case "project.groupId", "pom.groupId", "groupId":
		return p.group(), true, nil
	case "project.artifactId", "pom.artifactId", "artifactId":
		return p.ArtifactID, true, nil
	case "project.parent.groupId":
		return p.Parent.GroupID, true, nil
	}
	if f, ok := r.config[name]; ok {
		return "", false, fmt.Errorf("property %s set by %s: %w", name, f, errUnsupported)
	}
	chain := r.chain(p)
	for _, q := range chain {
		for _, m := range q.Profiles {
			if _, ok := m.Properties[name]; ok {
				return "", false, fmt.Errorf("property %s set by a profile: %w", name, errUnsupported)
			}
		}
	}
	for _, q := range chain {
		if v, ok := q.Properties[name]; ok {
			return v, true, nil
		}
	}
	return "", false, nil
}
