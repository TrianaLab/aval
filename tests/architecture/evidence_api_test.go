package architecture

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Issue #18: pkg/evidence is semver-stable public API. Its exported surface
// is snapshotted in pkg/evidence/testdata/api-v<major>.txt, one line per
// identifier, field and method, with types and signatures. The snapshot is
// append-only: a new identifier is a new line, and removing or changing a
// line is a breaking change that needs a new major module path
// (github.com/TrianaLab/aval/v2), which starts a new snapshot file. Every
// line of the snapshot at the base revision must still be there, so the
// snapshot cannot be edited together with a break.
// ponytail: names, types and signatures only, like a reduced apidiff;
// constant values are format identifiers and follow artifact-format.md.

const consumerModule = "example.com/evidence-consumer"

// apiSurface renders the exported API of pkg as sorted lines. Parameter
// names are left out: renaming one is compatible. An interface is one line
// with its whole method set, since adding a method breaks implementers.
func apiSurface(pkg *types.Package) []string {
	qual := types.RelativeTo(pkg)
	typ := func(t types.Type) string { return types.TypeString(t, qual) }
	var out []string
	for _, name := range pkg.Scope().Names() {
		obj := pkg.Scope().Lookup(name)
		if !obj.Exported() {
			continue
		}
		switch o := obj.(type) {
		case *types.Const:
			out = append(out, "const "+name+" "+typ(o.Type()))
		case *types.Var:
			out = append(out, "var "+name+" "+typ(o.Type()))
		case *types.Func:
			out = append(out, "func "+name+signature(o.Type().(*types.Signature), typ))
		case *types.TypeName:
			out = append(out, typeLines(o, typ)...)
		}
	}
	slices.Sort(out)
	return out
}

func typeLines(o *types.TypeName, typ func(types.Type) string) []string {
	name := o.Name()
	switch t := o.Type().(type) {
	case *types.Alias:
		return []string{"type " + name + typeParams(t.TypeParams(), typ) + " = " + typ(types.Unalias(t))}
	case *types.Named:
		name += typeParams(t.TypeParams(), typ)
	}
	var out []string
	switch u := o.Type().Underlying().(type) {
	case *types.Struct:
		out = append(out, "type "+name+" struct")
		// Losing comparability breaks == and map keys, so it is a line too.
		if types.Comparable(o.Type()) {
			out = append(out, "comparable "+o.Name())
		}
		for f := range u.Fields() {
			// An embedded field is rendered even when unexported: its
			// promoted members are API, and the unexported-type check
			// rejects it.
			if f.Exported() || f.Embedded() {
				out = append(out, fmt.Sprintf("field %s.%s %s", o.Name(), f.Name(), typ(f.Type())))
			}
		}
	case *types.Interface:
		if !u.IsMethodSet() { // a constraint: its type set is API too
			return append(out, "type "+name+" "+typ(u))
		}
		var ms []string
		for m := range u.Methods() {
			ms = append(ms, m.Name()+signature(m.Type().(*types.Signature), typ))
		}
		slices.Sort(ms)
		return append(out, "type "+name+" interface { "+strings.Join(ms, "; ")+" }")
	default:
		out = append(out, "type "+name+" "+typ(u))
	}
	// The method set, promoted methods included, with the receiver it
	// needs: T if the value has it, *T if only the pointer does.
	vset := types.NewMethodSet(o.Type())
	for sel := range types.NewMethodSet(types.NewPointer(o.Type())).Methods() {
		m := sel.Obj()
		if !m.Exported() {
			continue
		}
		recv := "*" + o.Name()
		if vset.Lookup(m.Pkg(), m.Name()) != nil {
			recv = o.Name()
		}
		out = append(out, fmt.Sprintf("method (%s) %s%s", recv, m.Name(), signature(m.Type().(*types.Signature), typ)))
	}
	return out
}

func typeParams(l *types.TypeParamList, typ func(types.Type) string) string {
	if l.Len() == 0 {
		return ""
	}
	var s []string
	for tp := range l.TypeParams() {
		s = append(s, tp.Obj().Name()+" "+typ(tp.Constraint()))
	}
	return "[" + strings.Join(s, ", ") + "]"
}

func signature(sig *types.Signature, typ func(types.Type) string) string {
	tuple := func(t *types.Tuple, variadic bool) []string {
		var s []string
		for i := range t.Len() {
			v := typ(t.At(i).Type())
			if variadic && i == t.Len()-1 {
				v = "..." + typ(t.At(i).Type().(*types.Slice).Elem())
			}
			s = append(s, v)
		}
		return s
	}
	out := typeParams(sig.TypeParams(), typ) + "(" + strings.Join(tuple(sig.Params(), sig.Variadic()), ", ") + ")"
	switch res := tuple(sig.Results(), false); len(res) {
	case 0:
	case 1:
		out += " " + res[0]
	default:
		out += " (" + strings.Join(res, ", ") + ")"
	}
	return out
}

// checkSource type-checks Go files (name -> source) as one package.
func checkSource(t *testing.T, path string, files map[string]string) *types.Package {
	t.Helper()
	fset := token.NewFileSet()
	var parsed []*ast.File
	for name, src := range files {
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, f)
	}
	pkg, err := (&types.Config{Importer: importer.Default()}).Check(path, fset, parsed, nil)
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

// apiProblems compares the current surface with the snapshot: a snapshot
// line that is gone is a breaking change; a new line must be appended.
func apiProblems(snapshot, current []string, file string) []string {
	var problems []string
	for _, l := range snapshot {
		if !slices.Contains(current, l) {
			problems = append(problems, "removed or changed: "+l+" (breaking: needs a new major module path and a new snapshot)")
		}
	}
	for _, l := range current {
		if !slices.Contains(snapshot, l) {
			problems = append(problems, "not in "+file+": "+l+" (append it if the change is additive)")
		}
	}
	return problems
}

var majorRe = regexp.MustCompile(`(?m)^module \S+?/v([0-9]+)$`)

// moduleMajor returns the major version a go.mod module path declares.
func moduleMajor(gomod string) string {
	if m := majorRe.FindStringSubmatch(gomod); m != nil {
		return m[1]
	}
	return "1"
}

// unexportedTypes reports surface lines that name an unexported type of
// pkg: its exported fields and methods are API a consumer reaches, yet they
// are not rendered, so they could change unseen.
// ponytail: matches words against the package scope; a false match fails
// closed.
func unexportedTypes(pkg *types.Package, surface []string) []string {
	var problems []string
	for _, l := range surface {
		for _, w := range identRe.FindAllString(l, -1) {
			if tn, ok := pkg.Scope().Lookup(w).(*types.TypeName); ok && !tn.Exported() {
				problems = append(problems, l+": exposes the unexported type "+w+"; export it or hide it")
			}
		}
	}
	return problems
}

// Go identifiers are Unicode letters, digits and underscores; a
// leftmost-longest run needs no word boundary.
var identRe = regexp.MustCompile(`[\p{L}_][\p{L}\p{N}_]*`)

// releasePlatforms returns every GOOS/GOARCH pair the release builds, read
// from .goreleaser.yaml, each with cgo off and on: consumers build pkg/evidence
// either way. It fails if the config yields no platform.
func releasePlatforms(t *testing.T, config string) []build.Context {
	t.Helper()
	var cfg struct {
		Builds []struct {
			Goos   []string `yaml:"goos"`
			Goarch []string `yaml:"goarch"`
		} `yaml:"builds"`
	}
	if err := yaml.Unmarshal([]byte(config), &cfg); err != nil {
		t.Fatal(err)
	}
	var out []build.Context
	for _, b := range cfg.Builds {
		for _, goos := range b.Goos {
			for _, goarch := range b.Goarch {
				for _, cgo := range []bool{false, true} {
					ctx := build.Default
					ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = goos, goarch, cgo
					out = append(out, ctx)
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatal(".goreleaser.yaml lists no goos/goarch to check pkg/evidence against")
	}
	return out
}

// platformSplit returns the platforms that build dir/name and those that do
// not, judged by its file name suffix and //go:build line.
// ponytail: custom tags (-tags foo) are not tried; a file excluded by one is
// out everywhere and never in the surface.
func platformSplit(dir, name string, platforms []build.Context) (in, out []string, err error) {
	for _, ctx := range platforms {
		ok, err := ctx.MatchFile(dir, name)
		if err != nil {
			return nil, nil, err
		}
		p := fmt.Sprintf("%s/%s cgo=%t", ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled)
		if ok {
			in = append(in, p)
		} else {
			out = append(out, p)
		}
	}
	return in, out, nil
}

// gitIn runs git in dir with extra environment and returns trimmed stdout.
func gitIn(dir string, env ...string) func(...string) (string, error) {
	return func(args ...string) (string, error) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), env...)
		out, err := c.Output()
		return strings.TrimSpace(string(out)), err
	}
}

// apiBase returns the revision whose snapshot must survive line for line:
// the merge base with origin/<base> on a pull request, the commit the branch
// pointed to before a push (`before` in the event at GITHUB_EVENT_PATH, so a
// multi-commit push is compared as a whole), the merge base with origin/main
// otherwise. Outside CI, a
// missing origin/main returns "" (nothing to compare with); in CI every
// failure is an error, so the check fails closed.
func apiBase(git func(...string) (string, error), getenv func(string) string) (string, error) {
	ci := getenv("CI") != ""
	var rev string
	var err error
	switch {
	case getenv("GITHUB_BASE_REF") != "":
		rev, err = git("merge-base", "HEAD", "origin/"+getenv("GITHUB_BASE_REF"))
	case getenv("GITHUB_EVENT_NAME") == "push":
		var before string
		if before, err = pushBefore(getenv("GITHUB_EVENT_PATH")); err == nil {
			rev, err = git("rev-parse", "--verify", before+"^{commit}")
		}
	default:
		if _, e := git("rev-parse", "--verify", "origin/main^{commit}"); e != nil && !ci {
			return "", nil
		}
		rev, err = git("merge-base", "HEAD", "origin/main")
	}
	if err != nil || rev == "" {
		return "", fmt.Errorf("cannot resolve the base revision to compare the API snapshot with (fetch full history): %v", err)
	}
	return rev, nil
}

var commitRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// pushBefore returns `before` from the push event JSON at path. A missing
// event, a malformed value or the all-zero id of a new branch is an error.
func pushBefore(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var ev struct {
		Before string `json:"before"`
	}
	if err := json.Unmarshal(b, &ev); err != nil {
		return "", err
	}
	if !commitRe.MatchString(ev.Before) || strings.Trim(ev.Before, "0") == "" {
		return "", fmt.Errorf("push event has no usable before commit: %q", ev.Before)
	}
	return ev.Before, nil
}

// snapshotRemovals returns the lines of snapshot at base that lines lacks.
// A snapshot absent at base (the first one of a major version) has none.
func snapshotRemovals(git func(...string) (string, error), base, snapshot string, lines []string) ([]string, error) {
	if ls, err := git("ls-tree", "--name-only", base, "--", snapshot); err != nil || ls == "" {
		return nil, err
	}
	old, err := git("show", base+":"+snapshot)
	if err != nil {
		return nil, err
	}
	var removed []string
	for l := range strings.Lines(old) {
		if l = strings.TrimSpace(l); l != "" && !slices.Contains(lines, l) {
			removed = append(removed, l)
		}
	}
	return removed, nil
}

// evidenceSources returns the non-test sources of pkg/evidence and reports
// any file that is built on some release platforms but not on others.
func evidenceSources(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join(root, "pkg", "evidence")
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	platforms := releasePlatforms(t, readFile(t, ".goreleaser.yaml"))
	files := map[string]string{}
	for _, f := range paths {
		name := filepath.Base(f)
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		// The surface is computed on one platform and consumers build on
		// all of them, so a file is built everywhere or nowhere.
		in, out, err := platformSplit(dir, name, platforms)
		if err != nil {
			t.Fatal(err)
		}
		if len(in) != 0 && len(out) != 0 {
			t.Errorf("pkg/evidence/%s is built on %v but not on %v: pkg/evidence must expose the same API on every platform", name, in, out)
		}
		if len(in) != 0 {
			files[f] = readFile(t, filepath.Join("pkg", "evidence", name))
		}
	}
	return files
}

func TestEvidenceAPIIsAppendOnly(t *testing.T) {
	files := evidenceSources(t)
	snapshot := "pkg/evidence/testdata/api-v" + moduleMajor(readFile(t, "go.mod")) + ".txt"
	pkg := checkSource(t, "github.com/TrianaLab/aval/pkg/evidence", files)
	current := apiSurface(pkg)
	for _, p := range unexportedTypes(pkg, current) {
		t.Error(p)
	}
	b, err := os.ReadFile(filepath.Join(root, snapshot))
	if err != nil {
		t.Fatalf("%v: a new major version starts a new snapshot with the current surface:\n%s", err, strings.Join(current, "\n"))
	}
	var lines []string
	for l := range strings.Lines(string(b)) {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	for _, p := range apiProblems(lines, current, snapshot) {
		t.Error(p)
	}
	// Append-only against the base revision: editing the snapshot together
	// with the code cannot hide a break.
	git := gitIn(root)
	base, err := apiBase(git, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if base == "" {
		t.Log("origin/main not found: append-only check against the base skipped (CI always runs it)")
		return
	}
	removed, err := snapshotRemovals(git, base, snapshot, lines)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range removed {
		t.Errorf("removed or changed in %s since %s: %s (breaking: needs a new major module path)", snapshot, base, l)
	}
}

func TestEvidenceAPICheckCatchesBreakingChanges(t *testing.T) {
	const base = `package p
import "context"
type Signer struct{ KeyID string; hidden int }
type Verifier interface { Verify(ctx context.Context, b []byte) (Signer, error) }
func Parse(data []byte, opts ...string) (*Signer, error) { return nil, nil }
func (s Signer) String() string { return "" }
const Version = "v1"
var ErrX error
`
	snapshot := apiSurface(checkSource(t, "p", map[string]string{"p.go": base}))
	want := []string{
		"comparable Signer",
		"const Version untyped string",
		"field Signer.KeyID string",
		"func Parse([]byte, ...string) (*Signer, error)",
		"method (Signer) String() string",
		"type Signer struct",
		"type Verifier interface { Verify(context.Context, []byte) (Signer, error) }",
		"var ErrX error",
	}
	if !slices.Equal(snapshot, want) {
		t.Fatalf("surface:\n%s", strings.Join(snapshot, "\n"))
	}
	compatible := map[string]string{
		"renamed parameter": strings.Replace(base, "data []byte", "raw []byte", 1),
		"new field":         strings.Replace(base, "KeyID string;", "KeyID string; Issuer string;", 1),
		"new function":      base + "func New() {}\n",
		"unexported change": strings.Replace(base, "hidden int", "hidden string", 1),
	}
	for name, src := range compatible {
		for _, p := range apiProblems(snapshot, apiSurface(checkSource(t, "p", map[string]string{"p.go": src})), "s") {
			if strings.HasPrefix(p, "removed") {
				t.Errorf("%s reported as breaking: %s", name, p)
			}
		}
	}
	breaking := map[string]string{
		"removed field":         strings.Replace(base, "KeyID string;", "", 1),
		"field type":            strings.Replace(base, "KeyID string", "KeyID []byte", 1),
		"interface method":      strings.Replace(base, "(Signer, error) }", "(Signer, error); Close() }", 1),
		"parameter type":        strings.Replace(base, "data []byte", "data string", 1),
		"not variadic":          strings.Replace(base, "opts ...string", "opts []string", 1),
		"result":                strings.Replace(base, "(*Signer, error) { return nil, nil }", "*Signer { return nil }", 1),
		"pointer receiver":      strings.Replace(base, "(s Signer)", "(s *Signer)", 1),
		"typed constant":        strings.Replace(base, `Version = "v1"`, `Version string = "v1"`, 1),
		"removed variable":      strings.Replace(base, "var ErrX error\n", "", 1),
		"not comparable":        strings.Replace(base, "KeyID string;", "KeyID string; Tags []string;", 1),
		"hidden not comparable": strings.Replace(base, "hidden int", "hidden []int", 1),
		"struct to named type":  strings.Replace(base, "type Signer struct{ KeyID string; hidden int }", "type Signer string\nfunc (s Signer) KeyID() {}", 1),
	}
	for name, src := range breaking {
		if !slices.ContainsFunc(apiProblems(snapshot, apiSurface(checkSource(t, "p", map[string]string{"p.go": src})), "s"), func(p string) bool {
			return strings.HasPrefix(p, "removed")
		}) {
			t.Errorf("%s not reported as breaking", name)
		}
	}
	if p := apiProblems(snapshot, append(slices.Clone(snapshot), "func New()"), "s"); len(p) != 1 || !strings.Contains(p[0], "append it") {
		t.Errorf("an unsnapshotted addition: %v", p)
	}
}

// Constructs the base surface above lacks: generics, embedding, constraint
// interfaces and unexported types reached through the exported API.
func TestEvidenceAPICheckCatchesHiddenSurface(t *testing.T) {
	surface := func(src string) ([]string, *types.Package) {
		pkg := checkSource(t, "p", map[string]string{"p.go": "package p\n" + src})
		return apiSurface(pkg), pkg
	}
	breaking := []struct{ name, base, changed string }{
		{"generic func constraint", "func First[T any](s []T) T { return s[0] }", "func First[T comparable](s []T) T { return s[0] }"},
		{"generic type parameters", "type Box[T any] struct{ V T }", "type Box[T comparable, U any] struct{ V T }"},
		{"generic method", "type Box[T any] struct{ V T }\nfunc (Box[T]) Get() T { var v T; return v }", "type Box[T any] struct{ V T }\nfunc (Box[T]) Get() int { return 0 }"},
		{"constraint type set", "type Num interface{ ~int | ~float64 }", "type Num interface{ ~int }"},
		{"promoted method", "type Inner struct{}\nfunc (Inner) Do() {}\ntype S struct{ Inner }", "type Inner struct{}\nfunc (Inner) Do(int) {}\ntype S struct{ Inner }"},
		{"promoted to pointer receiver", "type Inner struct{}\nfunc (Inner) Do() {}\ntype S struct{ Inner }", "type Inner struct{}\nfunc (*Inner) Do() {}\ntype S struct{ Inner }"},
	}
	for _, c := range breaking {
		old, _ := surface(c.base)
		cur, _ := surface(c.changed)
		if !slices.ContainsFunc(apiProblems(old, cur, "s"), func(p string) bool { return strings.HasPrefix(p, "removed") }) {
			t.Errorf("%s not reported as breaking:\n%s\n->\n%s", c.name, strings.Join(old, "\n"), strings.Join(cur, "\n"))
		}
	}
	// Unexported types whose exported members are API but not rendered.
	for name, src := range map[string]string{
		"embedded unexported struct":  "type extras struct{ Extra string }\nfunc (extras) Describe() string { return \"\" }\ntype Signer struct{ extras }",
		"embedded unexported pointer": "type extras struct{ Extra string }\ntype Signer struct{ *extras }",
		"returned unexported type":    "type state struct{ Public int }\nfunc (*state) Do() {}\nfunc NewState() *state { return nil }",
		"unexported field type":       "type state struct{ Public int }\ntype S struct{ F state }",
		"alias of an unexported type": "type state struct{ Public int }\ntype S = state",
		"unexported in an interface":  "type state struct{ Public int }\ntype I interface{ Get() state }",
		"non-ascii unexported":        "type état struct{ Public int }\nfunc New() *état { return nil }",
	} {
		if cur, pkg := surface(src); len(unexportedTypes(pkg, cur)) == 0 {
			t.Errorf("%s not reported:\n%s", name, strings.Join(cur, "\n"))
		}
	}
	cur, pkg := surface("type state struct{ Public int }\ntype S struct{ s state; Name string }\nfunc New() S { return S{} }")
	if p := unexportedTypes(pkg, cur); len(p) != 0 {
		t.Errorf("an unexported field of an unexported type is not API: %v", p)
	}
	// The promoted method is rendered on the embedding type.
	if cur, _ := surface("type Inner struct{}\nfunc (Inner) Do() {}\ntype S struct{ Inner }"); !slices.Contains(cur, "method (S) Do()") {
		t.Errorf("promoted method missing:\n%s", strings.Join(cur, "\n"))
	}
}

// throwawayRepo is an empty git repository that ignores global and system
// git config. commit writes name (if set) and commits.
func throwawayRepo(t *testing.T) (git func(...string) (string, error), commit func(name, content string) string) {
	dir := t.TempDir()
	git = gitIn(dir, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	run := func(args ...string) string {
		t.Helper()
		out, err := git(args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return out
	}
	run("init", "-q")
	return git, func(name, content string) string {
		t.Helper()
		if name != "" {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		run("add", "-A")
		run("commit", "-q", "--allow-empty", "-m", "c")
		return run("rev-parse", "HEAD")
	}
}

func envOf(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

// Without a base revision CI fails closed; only a local run skips.
func TestAPIBaseFailsClosedInCI(t *testing.T) {
	git, commit := throwawayRepo(t)
	if b, err := apiBase(git, envOf(nil)); err != nil || b != "" {
		t.Errorf("local without origin/main: %q %v, want a skip", b, err)
	}
	if _, err := apiBase(git, envOf(map[string]string{"CI": "true"})); err == nil {
		t.Error("CI without origin/main must fail closed")
	}
	commit("README", "x")
	dir := t.TempDir()
	for name, event := range map[string]string{
		"no event file":   "",
		"no before":       `{}`,
		"new branch":      `{"before":"0000000000000000000000000000000000000000"}`,
		"not a commit id": `{"before":"HEAD^"}`,
		"option-like":     `{"before":"--all"}`,
		"malformed":       `{`,
		"unknown commit":  `{"before":"1111111111111111111111111111111111111111"}`,
	} {
		path := filepath.Join(dir, "missing.json")
		if event != "" {
			path = filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".json")
			if err := os.WriteFile(path, []byte(event), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if b, err := apiBase(git, envOf(map[string]string{"CI": "true", "GITHUB_EVENT_NAME": "push", "GITHUB_EVENT_PATH": path})); err == nil {
			t.Errorf("a push with %s must fail closed, got base %q", name, b)
		}
	}
	if _, err := apiBase(git, envOf(map[string]string{"CI": "true", "GITHUB_BASE_REF": "gone"})); err == nil {
		t.Error("a pull request whose base branch is not fetched must fail closed")
	}
}

// A snapshot line removed or changed since the base revision is reported
// however the base is resolved; appended lines are not.
func TestSnapshotRemovalsAgainstBase(t *testing.T) {
	const snap = "api-v1.txt"
	git, commit := throwawayRepo(t)
	first := commit("README", "x")
	second := commit(snap, "type A struct\nfield A.X string\n")
	for _, ref := range []string{"refs/remotes/origin/main", "refs/remotes/origin/release"} {
		if _, err := git("update-ref", ref, "HEAD"); err != nil {
			t.Fatal(err)
		}
	}
	// One push of two commits: the first rewrites the snapshot together
	// with a break, the second is unrelated. The base is the pre-push tip,
	// not HEAD^.
	commit(snap, "type A struct\nfield A.X int\n")
	commit("", "")
	event := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(event, []byte(`{"before":"`+second+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, getenv := range map[string]func(string) string{
		"local":        envOf(nil),
		"pull request": envOf(map[string]string{"CI": "true", "GITHUB_BASE_REF": "release", "GITHUB_EVENT_NAME": "pull_request"}),
		"push":         envOf(map[string]string{"CI": "true", "GITHUB_EVENT_NAME": "push", "GITHUB_EVENT_PATH": event}),
	} {
		base, err := apiBase(git, getenv)
		if err != nil || base != second {
			t.Fatalf("%s: base %q, want %s: %v", name, base, second, err)
		}
		if removed, err := snapshotRemovals(git, base, snap, []string{"type A struct", "field A.X int"}); err != nil || !slices.Equal(removed, []string{"field A.X string"}) {
			t.Errorf("%s: removed %v, %v", name, removed, err)
		}
		if removed, err := snapshotRemovals(git, base, snap, []string{"field A.X string", "type A struct", "type B struct"}); err != nil || len(removed) != 0 {
			t.Errorf("%s: an appended line reported: %v, %v", name, removed, err)
		}
	}
	if removed, err := snapshotRemovals(git, first, snap, nil); err != nil || len(removed) != 0 {
		t.Errorf("no snapshot at the base: %v, %v", removed, err)
	}
	if _, err := snapshotRemovals(git, "0000000000000000000000000000000000000000", snap, nil); err == nil {
		t.Error("an unknown base must be an error")
	}
}

// A file built on some release platforms but not others is reported, by
// file name suffix or //go:build line; an unconstrained file is not.
func TestPlatformSplit(t *testing.T) {
	platforms := releasePlatforms(t, "builds:\n  - goos: [linux, darwin, windows]\n    goarch: [amd64, arm64]\n")
	if len(platforms) != 12 {
		t.Fatalf("got %d platforms, want 3 goos x 2 goarch x cgo off/on", len(platforms))
	}
	dir := t.TempDir()
	for name, src := range map[string]string{
		"a_windows.go": "package p\n",
		"b.go":         "//go:build !windows\n\npackage p\n",
		"c_arm64.go":   "package p\n",
		"d.go":         "//go:build cgo\n\npackage p\n",
		"e.go":         "package p\n",
		"f.go":         "//go:build ignore\n\npackage p\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, split := range map[string]bool{"a_windows.go": true, "b.go": true, "c_arm64.go": true, "d.go": true, "e.go": false, "f.go": false} {
		in, out, err := platformSplit(dir, name, platforms)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(in) != 0 && len(out) != 0; got != split {
			t.Errorf("%s: split %t, want %t (in %v, out %v)", name, got, split, in, out)
		}
	}
	if _, _, err := platformSplit(filepath.Join(dir, "gone"), "x.go", platforms); err == nil {
		t.Error("an unreadable file must be an error")
	}
}

func TestModuleMajor(t *testing.T) {
	for gomod, want := range map[string]string{
		"module github.com/TrianaLab/aval\n\ngo 1.27\n": "1",
		"module github.com/TrianaLab/aval/v2\n":         "2",
		"module github.com/TrianaLab/aval/v10\n":        "10",
		"module github.com/TrianaLab/aval/vx\n":         "1",
	} {
		if got := moduleMajor(gomod); got != want {
			t.Errorf("%q: got %s, want %s", gomod, got, want)
		}
	}
}

// consumerImportProblems reports what the example consumer must not import,
// given `go list -deps -test` import paths: from Aval, pkg/evidence only.
func consumerImportProblems(lines []string) []string {
	var problems []string
	for _, l := range lines {
		path, _, _ := strings.Cut(l, " ")
		switch {
		case strings.HasSuffix(l, " true"), path == consumerModule, strings.HasPrefix(path, consumerModule+" "), strings.HasPrefix(path, consumerModule+"."):
		case path == "github.com/TrianaLab/aval/pkg/evidence":
		default:
			problems = append(problems, path+": the example consumer may import only pkg/evidence and the standard library")
		}
	}
	return problems
}

func TestEvidenceConsumerImportsOnlyPkgEvidence(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "-test", "-f", "{{.ImportPath}} {{.Standard}}", "./...")
	cmd.Dir = filepath.Join(root, "examples", "evidence-consumer")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if !slices.Contains(lines, "github.com/TrianaLab/aval/pkg/evidence false") {
		t.Error("the example consumer does not import pkg/evidence")
	}
	for _, p := range consumerImportProblems(lines) {
		t.Error(p)
	}
}

func TestConsumerImportCheckCatchesOtherAvalPackages(t *testing.T) {
	bad := []string{
		"github.com/TrianaLab/aval/internal/attest false",
		"github.com/TrianaLab/aval/cmd/aval false",
		"github.com/sigstore/sigstore-go/pkg/verify false",
	}
	for _, l := range bad {
		if len(consumerImportProblems([]string{l})) != 1 {
			t.Errorf("%q not reported", l)
		}
	}
	ok := []string{"fmt true", "github.com/TrianaLab/aval/pkg/evidence false", consumerModule + " false", consumerModule + ".test false", consumerModule + " [" + consumerModule + ".test] false"}
	if p := consumerImportProblems(ok); len(p) != 0 {
		t.Errorf("valid imports reported %v", p)
	}
}
