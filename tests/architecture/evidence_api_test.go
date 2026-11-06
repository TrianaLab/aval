package architecture

import (
	"fmt"
	"go/ast"
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
)

// Issue #18: pkg/evidence is semver-stable public API. Its exported surface
// is snapshotted in pkg/evidence/testdata/api-v<major>.txt, one line per
// identifier, field and method, with types and signatures. The snapshot is
// append-only: a new identifier is a new line, and removing or changing a
// line is a breaking change that needs a new major module path
// (github.com/TrianaLab/aval/v2), which starts a new snapshot file.
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
	if o.IsAlias() {
		return []string{"type " + name + " = " + typ(o.Type())}
	}
	var out []string
	switch u := o.Type().Underlying().(type) {
	case *types.Struct:
		out = append(out, "type "+name+" struct")
		for f := range u.Fields() {
			if f.Exported() {
				out = append(out, fmt.Sprintf("field %s.%s %s", name, f.Name(), typ(f.Type())))
			}
		}
	case *types.Interface:
		var ms []string
		for m := range u.Methods() {
			ms = append(ms, m.Name()+signature(m.Type().(*types.Signature), typ))
		}
		slices.Sort(ms)
		out = append(out, "type "+name+" interface { "+strings.Join(ms, "; ")+" }")
	default:
		out = append(out, "type "+name+" "+typ(u))
	}
	if named, ok := o.Type().(*types.Named); ok {
		for m := range named.Methods() {
			if m.Exported() {
				out = append(out, fmt.Sprintf("method (%s) %s%s", typ(m.Type().(*types.Signature).Recv().Type()), m.Name(), signature(m.Type().(*types.Signature), typ)))
			}
		}
	}
	return out
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
	out := "(" + strings.Join(tuple(sig.Params(), sig.Variadic()), ", ") + ")"
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

func TestEvidenceAPIIsAppendOnly(t *testing.T) {
	files := map[string]string{}
	paths, _ := filepath.Glob(filepath.Join(root, "pkg", "evidence", "*.go"))
	for _, f := range paths {
		if !strings.HasSuffix(f, "_test.go") {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			files[f] = string(b)
		}
	}
	snapshot := "pkg/evidence/testdata/api-v" + moduleMajor(readFile(t, "go.mod")) + ".txt"
	current := apiSurface(checkSource(t, "github.com/TrianaLab/aval/pkg/evidence", files))
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
		"removed field":        strings.Replace(base, "KeyID string;", "", 1),
		"field type":           strings.Replace(base, "KeyID string", "KeyID []byte", 1),
		"interface method":     strings.Replace(base, "(Signer, error) }", "(Signer, error); Close() }", 1),
		"parameter type":       strings.Replace(base, "data []byte", "data string", 1),
		"not variadic":         strings.Replace(base, "opts ...string", "opts []string", 1),
		"result":               strings.Replace(base, "(*Signer, error) { return nil, nil }", "*Signer { return nil }", 1),
		"pointer receiver":     strings.Replace(base, "(s Signer)", "(s *Signer)", 1),
		"typed constant":       strings.Replace(base, `Version = "v1"`, `Version string = "v1"`, 1),
		"removed variable":     strings.Replace(base, "var ErrX error\n", "", 1),
		"struct to named type": strings.Replace(base, "type Signer struct{ KeyID string; hidden int }", "type Signer string\nfunc (s Signer) KeyID() {}", 1),
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
