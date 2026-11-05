package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/fingerprint"
	"github.com/TrianaLab/aval/internal/policy"
	"github.com/TrianaLab/aval/pkg/evidence"
)

const (
	actionsIssuer = "https://token.actions.githubusercontent.com"
	// ownWorkflow prefixes every identity the main policy may trust.
	ownWorkflow = `https://github\.com/TrianaLab/aval/\.github/workflows/`
)

func loadPolicy(t *testing.T, name string) *policy.Policy {
	t.Helper()
	p, err := policy.Load(filepath.Join(root, ".github", "aval", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// mainPolicyProblems: main and releases trust only Aval's own workflows,
// keyless, at runner level, and require runner level (#28).
func mainPolicyProblems(p *policy.Policy) []string {
	var problems []string
	if p.MinTrustLevel != evidence.TrustRunner {
		problems = append(problems, "minTrustLevel must be runner")
	}
	for _, id := range p.Identities {
		if id.PublicKey != "" || id.Issuer != actionsIssuer || !strings.HasPrefix(id.SubjectRegexp, ownWorkflow) || id.TrustLevel != evidence.TrustRunner {
			problems = append(problems, "untrusted identity "+id.Issuer+" "+id.SubjectRegexp)
		}
	}
	return problems
}

// prPolicyProblems: a PR may also accept developer signatures, but grants
// more than developer level only to identities the main policy trusts.
func prPolicyProblems(pr, main *policy.Policy) []string {
	var problems []string
	for _, id := range pr.Identities {
		if id.TrustLevel > evidence.TrustDeveloper && !slices.Contains(main.Identities, id) {
			problems = append(problems, "identity above developer level not in the main policy: "+id.Issuer+" "+id.SubjectRegexp)
		}
	}
	return problems
}

func TestDogfoodPolicies(t *testing.T) {
	main, pr := loadPolicy(t, "main.yaml"), loadPolicy(t, "pr.yaml")
	for _, p := range append(mainPolicyProblems(main), prPolicyProblems(pr, main)...) {
		t.Error(p)
	}
}

func TestDogfoodPolicyChecksCatchLooseTrust(t *testing.T) {
	ci := evidence.Identity{Issuer: actionsIssuer, SubjectRegexp: ownWorkflow + `ci\.yaml@refs/heads/main`, TrustLevel: evidence.TrustRunner}
	main := &policy.Policy{MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{ci}}
	if p := mainPolicyProblems(main); len(p) != 0 {
		t.Fatalf("valid main policy reported %v", p)
	}
	dev := evidence.Identity{Issuer: "https://github.com/login/oauth", SubjectRegexp: `a@example\.com`, TrustLevel: evidence.TrustDeveloper}
	bad := map[string]*policy.Policy{
		"developer level":     {MinTrustLevel: evidence.TrustDeveloper, Identities: []evidence.Identity{ci}},
		"developer identity":  {MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{ci, dev}},
		"key":                 {MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{{PublicKey: "k", TrustLevel: evidence.TrustRunner}}},
		"other repository":    {MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{{Issuer: actionsIssuer, SubjectRegexp: `https://github\.com/evil/aval/.*`, TrustLevel: evidence.TrustRunner}}},
		"other issuer":        {MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{{Issuer: "https://evil", SubjectRegexp: ci.SubjectRegexp, TrustLevel: evidence.TrustRunner}}},
		"isolated self-grant": {MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{{Issuer: actionsIssuer, SubjectRegexp: ci.SubjectRegexp, TrustLevel: evidence.TrustIsolated}}},
	}
	for name, p := range bad {
		if len(mainPolicyProblems(p)) == 0 {
			t.Errorf("main policy with %s: no problem reported", name)
		}
	}
	if p := prPolicyProblems(&policy.Policy{Identities: []evidence.Identity{ci, dev}}, main); len(p) != 0 {
		t.Errorf("PR policy with a developer identity reported %v", p)
	}
	runnerDev := dev
	runnerDev.TrustLevel = evidence.TrustRunner
	if len(prPolicyProblems(&policy.Policy{Identities: []evidence.Identity{ci, runnerDev}}, main)) == 0 {
		t.Error("PR policy granting runner level to a developer: no problem reported")
	}
}

type permJob struct {
	If          string            `yaml:"if"`
	Permissions map[string]string `yaml:"permissions"`
	Steps       []step            `yaml:"steps"`
}

// liveSigningJobs may run a test with `id-token: write`: the test is the
// live keyless signing check itself (#6), push-only, and publishes nothing.
var liveSigningJobs = []string{"sigstore-keyless"}

func writeScoped(p map[string]string) bool {
	return p["packages"] == "write" || p["id-token"] == "write"
}

// writeScopeProblems: a job that can publish or sign (packages or id-token
// write) runs only on push, never on pull_request, and runs no test suite,
// so no credential reaches code under test. Write scopes are never granted
// workflow-wide.
func writeScopeProblems(data []byte) ([]string, error) {
	var wf struct {
		Permissions map[string]string  `yaml:"permissions"`
		Jobs        map[string]permJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, err
	}
	var problems []string
	if writeScoped(wf.Permissions) {
		problems = append(problems, "workflow-level write scopes")
	}
	for name, j := range wf.Jobs {
		if !writeScoped(j.Permissions) {
			continue
		}
		if !strings.Contains(j.If, "github.event_name == 'push'") || strings.Contains(j.If, "pull_request") || strings.Contains(j.If, "||") {
			problems = append(problems, name+": write scopes without a push-only condition")
		}
		if slices.Contains(liveSigningJobs, name) {
			continue
		}
		if slices.ContainsFunc(j.Steps, func(s step) bool {
			return slices.ContainsFunc([]string{"make ci-e2e", "aval run --shadow", "go test", "make test"}, func(c string) bool {
				return strings.Contains(s.Run, c)
			})
		}) {
			problems = append(problems, name+": runs tests with write scopes")
		}
	}
	slices.Sort(problems)
	return problems, nil
}

func TestWriteScopedJobsRunOnlyOnPush(t *testing.T) {
	problems, err := writeScopeProblems(readCI(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

func TestWriteScopeCheckCatchesPRJobs(t *testing.T) {
	cases := map[string]string{
		"no condition":   "jobs:\n  a:\n    permissions: {id-token: write}\n",
		"on PRs":         "jobs:\n  a:\n    if: github.event_name == 'pull_request'\n    permissions: {packages: write}\n",
		"runs the suite": "jobs:\n  a:\n    if: github.event_name == 'push'\n    permissions: {packages: write}\n    steps: [{run: make ci-e2e}]\n",
		"push or PR":     "jobs:\n  a:\n    if: github.event_name == 'push' || github.event_name == 'pull_request'\n    permissions: {packages: write}\n",
		"go test":        "jobs:\n  a:\n    if: github.event_name == 'push'\n    permissions: {id-token: write}\n    steps: [{run: go test ./...}]\n",
		"make test":      "jobs:\n  a:\n    if: github.event_name == 'push'\n    permissions: {id-token: write}\n    steps: [{run: make test-acceptance}]\n",
		"workflow-wide":  "permissions: {packages: write}\njobs:\n  a:\n    steps: [{run: echo}]\n",
	}
	for name, wf := range cases {
		if p, err := writeScopeProblems([]byte(wf)); err != nil || len(p) == 0 {
			t.Errorf("%s: no problem reported (%v)", name, err)
		}
	}
	ok := "jobs:\n  a:\n    if: github.event_name == 'push'\n    permissions: {id-token: write}\n    steps: [{run: aval push}]\n  b:\n    steps: [{run: make ci-e2e}]\n"
	if p, err := writeScopeProblems([]byte(ok)); err != nil || len(p) != 0 {
		t.Errorf("valid workflow reported %v (%v)", p, err)
	}
}

// suiteDirs: the directories whose tests a runner.command runs, and the
// further directories it only builds.
type suiteDirs struct{ run, built []string }

// dogfoodSuites maps each dogfood contract to its suite's directories.
// Update it with runner.command.
var dogfoodSuites = map[string]suiteDirs{
	"aval.yaml":     {run: []string{"cmd", "internal", "pkg"}},
	"aval.e2e.yaml": {run: []string{"tests/integration", "tests/acceptance"}, built: []string{"cmd", "internal", "pkg"}},
}

// literalPaths returns the relative paths a Go file names as string
// literals: "../x" literals and filepath.Join/path.Join calls over literals
// that start with "..". ponytail: static and literal only; a path built at
// run time is not seen.
func literalPaths(t *testing.T, name string, src []byte) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BasicLit:
			if s, err := strconv.Unquote(n.Value); err == nil && n.Kind == token.STRING && strings.HasPrefix(s, "../") {
				out = append(out, s)
			}
		case *ast.CallExpr:
			sel, ok := n.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Join" {
				return true
			}
			var parts []string
			for _, a := range n.Args {
				if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					s, _ := strconv.Unquote(lit.Value)
					parts = append(parts, s)
				}
			}
			if len(parts) > 0 && len(parts) == len(n.Args) && parts[0] == ".." {
				out = append(out, path.Join(parts...))
			}
		}
		return true
	})
	return out
}

// undeclaredReads lists the tracked files a suite compiles or reads that
// contract c leaves out of its fingerprint (invariant 2): every tracked file
// under the suite's directories, and every tracked file or directory that
// the Go files under its run directories name by a relative literal path.
func undeclaredReads(t *testing.T, c *contract.Contract, dirs suiteDirs) []string {
	t.Helper()
	fp, err := fingerprint.Compute(root, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	inScope := map[string]bool{}
	for _, l := range strings.Split(fp.Manifest, "\n") {
		if f := strings.SplitN(l, " ", 4); len(f) == 4 && (f[0] == "source" || f[0] == "dependency") {
			inScope[f[3]] = true
		}
	}
	all := trackedFiles(t)
	read := map[string]bool{}
	// built may be empty, and ls-files without a pathspec lists every file.
	for _, f := range trackedFiles(t, append(dirs.built, dirs.run...)...) {
		read[f] = true
	}
	for _, f := range trackedFiles(t, dirs.run...) {
		read[f] = true
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		for _, rel := range literalPaths(t, f, []byte(readFile(t, f))) {
			p := path.Join(path.Dir(f), rel)
			for _, g := range all {
				if g == p || strings.HasPrefix(g, p+"/") {
					read[g] = true
				}
			}
		}
	}
	var problems []string
	for f := range read {
		if !inScope[f] {
			problems = append(problems, f)
		}
	}
	slices.Sort(problems)
	return problems
}

func TestDogfoodScopeCoversEveryRead(t *testing.T) {
	for file, dirs := range dogfoodSuites {
		c, err := contract.Load(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range undeclaredReads(t, c, dirs) {
			t.Errorf("%s: the suite reads %s, which is outside scope: a change to it alone would be a false hit", file, f)
		}
	}
}

func TestDogfoodScopeCheckCatchesUndeclaredReads(t *testing.T) {
	c, err := contract.Load(filepath.Join(root, "aval.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// Go sources only: testdata, the CLI reference and the fuzz seeds drop out.
	c.Scope.Source = []string{"cmd/**/*.go", "internal/**/*.go", "pkg/**/*.go"}
	got := undeclaredReads(t, c, dogfoodSuites["aval.yaml"])
	for _, want := range []string{"pkg/evidence/testdata/report.golden.json", "docs/cli-reference.md", "aval.yaml", "examples/go-compose/aval.yaml"} {
		if !slices.Contains(got, want) {
			t.Errorf("undeclared read of %s not reported (got %v)", want, got)
		}
	}
}
