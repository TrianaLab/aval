package architecture

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
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

// mainPolicyProblems: main and releases require runner level (#28) and
// grant it only to Aval's own workflows, keyless. Any other identity may be
// listed at developer level only, below the minimum, so its results are a
// miss on main, never reused and never a verification failure (#130).
func mainPolicyProblems(p *policy.Policy) []string {
	var problems []string
	if p.MinTrustLevel != evidence.TrustRunner {
		problems = append(problems, "minTrustLevel must be runner")
	}
	for _, id := range p.Identities {
		ownCI := id.PublicKey == "" && id.Issuer == actionsIssuer && strings.HasPrefix(id.SubjectRegexp, ownWorkflow) && literalRegexp(id.SubjectRegexp) && id.TrustLevel == evidence.TrustRunner
		// A catch-all at developer level would turn a forged signature from
		// a verification failure into a miss (invariant 4): keyless
		// developers must name one subject.
		if !ownCI && (id.TrustLevel != evidence.TrustDeveloper || (id.PublicKey == "" && !literalRegexp(id.SubjectRegexp))) {
			problems = append(problems, "untrusted identity "+id.Issuer+" "+id.SubjectRegexp)
		}
	}
	return problems
}

// literalRegexp reports whether s matches exactly one string.
func literalRegexp(s string) bool {
	re, err := regexp.Compile(s)
	if err != nil {
		return false
	}
	_, complete := re.LiteralPrefix()
	return complete
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
	if p := mainPolicyProblems(&policy.Policy{MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{ci, dev}}); len(p) != 0 {
		t.Fatalf("main policy with a developer identity at developer level reported %v", p)
	}
	runnerDev := dev
	runnerDev.TrustLevel = evidence.TrustRunner
	bad := map[string]*policy.Policy{
		"developer level":              {MinTrustLevel: evidence.TrustDeveloper, Identities: []evidence.Identity{ci}},
		"lowered minimum beside dev":   {MinTrustLevel: evidence.TrustDeveloper, Identities: []evidence.Identity{ci, dev}},
		"developer identity at runner": {MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{ci, runnerDev}},
		"developer key at runner":      {MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{ci, {PublicKey: "k", TrustLevel: evidence.TrustRunner}}},
		"key":                          {MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{{PublicKey: "k", TrustLevel: evidence.TrustRunner}}},
		"other repository":             {MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{{Issuer: actionsIssuer, SubjectRegexp: `https://github\.com/evil/aval/.*`, TrustLevel: evidence.TrustRunner}}},
		"other issuer":                 {MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{{Issuer: "https://evil", SubjectRegexp: ci.SubjectRegexp, TrustLevel: evidence.TrustRunner}}},
		"wildcard developer":           {MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{ci, {Issuer: actionsIssuer, SubjectRegexp: ".*", TrustLevel: evidence.TrustDeveloper}}},
		"wildcard after own workflow":  {MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{{Issuer: actionsIssuer, SubjectRegexp: ownWorkflow + ".*|.*", TrustLevel: evidence.TrustRunner}}},
		"invalid developer regexp":     {MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{ci, {Issuer: actionsIssuer, SubjectRegexp: "(", TrustLevel: evidence.TrustDeveloper}}},
		"isolated self-grant":          {MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{{Issuer: actionsIssuer, SubjectRegexp: ci.SubjectRegexp, TrustLevel: evidence.TrustIsolated}}},
	}
	for name, p := range bad {
		if len(mainPolicyProblems(p)) == 0 {
			t.Errorf("main policy with %s: no problem reported", name)
		}
	}
	if p := prPolicyProblems(&policy.Policy{Identities: []evidence.Identity{ci, dev}}, main); len(p) != 0 {
		t.Errorf("PR policy with a developer identity reported %v", p)
	}
	if len(prPolicyProblems(&policy.Policy{Identities: []evidence.Identity{ci, runnerDev}}, main)) == 0 {
		t.Error("PR policy granting runner level to a developer: no problem reported")
	}
	// Main listing the developer (at developer level) does not let a PR raise it.
	withDev := &policy.Policy{MinTrustLevel: evidence.TrustRunner, Identities: []evidence.Identity{ci, dev}}
	if len(prPolicyProblems(&policy.Policy{Identities: []evidence.Identity{ci, runnerDev}}, withDev)) == 0 {
		t.Error("PR policy raising main's developer identity to runner: no problem reported")
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
		if !pushOnly(j.If) {
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
	fp, err := fingerprint.Compute(context.Background(), root, c, nil)
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
	for _, want := range []string{"pkg/evidence/testdata/verify-report-v1.golden.json", "docs/cli-reference.md", "aval.yaml", "examples/go-compose/aval.yaml"} {
		if !slices.Contains(got, want) {
			t.Errorf("undeclared read of %s not reported (got %v)", want, got)
		}
	}
}

// releaseJobs use Aval as a user would, so they run the pinned release that
// .github/aval/install.sh verifies, never a binary built from the PR.
var releaseJobs = []string{"ci-e2e", "example-go-compose", "dogfood-shadow", "dogfood-publish", "dogfood-record"}

var (
	pinVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	pinSHA256  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// releasePinProblems: ci.yaml pins a released aval version (a CHANGELOG.md
// section) and a sha256, and each release job installs cosign, then runs
// install.sh before any aval step, and builds nothing. Offline, this cannot
// prove the sha256 belongs to the version; install.sh checks that at run
// time against the signed checksums.txt.
func releasePinProblems(ci, changelog []byte) ([]string, error) {
	var wf struct {
		Env  map[string]string  `yaml:"env"`
		Jobs map[string]permJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(ci, &wf); err != nil {
		return nil, err
	}
	var problems []string
	v := wf.Env["AVAL_VERSION"]
	if !pinVersion.MatchString(v) || !slices.Contains(strings.Split(string(changelog), "\n"), "## "+v) {
		problems = append(problems, "AVAL_VERSION "+strconv.Quote(v)+" is not a released version in CHANGELOG.md")
	}
	if !pinSHA256.MatchString(wf.Env["AVAL_SHA256"]) {
		problems = append(problems, "AVAL_SHA256 is not a sha256")
	}
	for _, name := range releaseJobs {
		problems = append(problems, releaseJobProblems(name, wf.Jobs[name].Steps)...)
	}
	slices.Sort(problems)
	return problems, nil
}

// releaseJobProblems checks one release job: install the release (after
// cosign) before any aval step, and build nothing.
func releaseJobProblems(name string, steps []step) []string {
	var problems []string
	cosign, installed := false, false
	for _, s := range steps {
		switch {
		case strings.HasPrefix(s.Uses, "sigstore/cosign-installer@"):
			cosign = true
		case strings.TrimSpace(s.Run) == ".github/aval/install.sh" || strings.TrimSpace(s.Run) == trustStep:
			if !cosign {
				problems = append(problems, name+": install.sh before cosign is installed")
			}
			installed = true
		case strings.Contains(s.Run, "make build") || strings.Contains(s.Run, "bin/aval"):
			problems = append(problems, name+": builds aval instead of using the release")
		case strings.Contains(s.Run, "aval ") && !installed:
			problems = append(problems, name+": runs aval before installing the release")
		}
	}
	if !installed {
		problems = append(problems, name+": does not install the pinned release")
	}
	return problems
}

func TestCIPinsVerifiedRelease(t *testing.T) {
	changelog, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	problems, err := releasePinProblems(readCI(t), changelog)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

func TestReleasePinCheckCatchesDrift(t *testing.T) {
	changelog := []byte("# aval\n\n## 0.2.0\n\n## 0.1.0\n")
	sha := strings.Repeat("a", 64)
	install := "[{uses: sigstore/cosign-installer@x}, {run: .github/aval/install.sh}, {run: aval run}]"
	wf := func(version, sha, steps string) []byte {
		s := "env: {AVAL_VERSION: " + version + ", AVAL_SHA256: " + sha + "}\njobs:\n"
		for _, j := range releaseJobs {
			s += "  " + j + ": {steps: " + steps + "}\n"
		}
		return []byte(s)
	}
	if p, err := releasePinProblems(wf("0.2.0", sha, install), changelog); err != nil || len(p) != 0 {
		t.Fatalf("valid workflow reported %v (%v)", p, err)
	}
	bad := map[string][]byte{
		"unreleased version":  wf("0.3.0", sha, install),
		"version with v":      wf("v0.2.0", sha, install),
		"short sha":           wf("0.2.0", "abc", install),
		"no install":          wf("0.2.0", sha, "[{run: aval run}]"),
		"no cosign":           wf("0.2.0", sha, "[{run: .github/aval/install.sh}]"),
		"builds from source":  wf("0.2.0", sha, "[{uses: sigstore/cosign-installer@x}, {run: .github/aval/install.sh}, {run: make build}]"),
		"runs bin/aval":       wf("0.2.0", sha, "[{uses: sigstore/cosign-installer@x}, {run: .github/aval/install.sh}, {run: ./bin/aval run}]"),
		"source build only":   wf("0.2.0", sha, "[{run: make build}, {run: aval run}]"),
		"aval before install": wf("0.2.0", sha, "[{run: aval run}, {uses: sigstore/cosign-installer@x}, {run: .github/aval/install.sh}]"),
	}
	for name, data := range bad {
		if p, err := releasePinProblems(data, changelog); err != nil || len(p) == 0 {
			t.Errorf("%s: no problem reported (%v)", name, err)
		}
	}
}
