package architecture

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
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

// dogfoodRegistry is where Aval's own CI publishes its results.
const dogfoodRegistry = "ghcr.io/trianalab/aval-attestations"

// policyProblems: the one policy (#216) names the registry and grants runner
// level, or more, only to Aval's own workflows, keyless (#28). Any other
// identity is at developer level only, so main and releases, which raise the
// minimum to runner (mainMinTrust), never reuse its results, and a result it
// signed is a miss there, never a verification failure (#130). Pull requests
// keep the policy's minimum, which must not exceed runner, or CI could never
// reuse its own results.
func policyProblems(p *policy.Policy) []string {
	var problems []string
	if p.Registry != dogfoodRegistry {
		problems = append(problems, "registry must be "+dogfoodRegistry)
	}
	if p.MinTrustLevel > evidence.TrustRunner {
		problems = append(problems, "minTrustLevel must not exceed runner")
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

func TestDogfoodPolicy(t *testing.T) {
	for _, p := range policyProblems(loadPolicy(t, "policy.yaml")) {
		t.Error(p)
	}
}

func TestDogfoodPolicyCheckCatchesLooseTrust(t *testing.T) {
	ci := evidence.Identity{Issuer: actionsIssuer, SubjectRegexp: ownWorkflow + `ci\.yaml@refs/heads/main`, TrustLevel: evidence.TrustRunner}
	dev := evidence.Identity{Issuer: "https://github.com/login/oauth", SubjectRegexp: `a@example\.com`, TrustLevel: evidence.TrustDeveloper}
	pol := func(min evidence.TrustLevel, ids ...evidence.Identity) *policy.Policy {
		return &policy.Policy{Registry: dogfoodRegistry, MinTrustLevel: min, Identities: ids}
	}
	for _, p := range []*policy.Policy{pol(evidence.TrustDeveloper, ci, dev), pol(evidence.TrustRunner, ci, dev), pol(evidence.TrustDeveloper, ci, evidence.Identity{PublicKey: "k", TrustLevel: evidence.TrustDeveloper})} {
		if got := policyProblems(p); len(got) != 0 {
			t.Errorf("valid policy reported %v", got)
		}
	}
	runnerDev := dev
	runnerDev.TrustLevel = evidence.TrustRunner
	otherRegistry := pol(evidence.TrustDeveloper, ci)
	otherRegistry.Registry = "ghcr.io/evil/x"
	bad := map[string]*policy.Policy{
		"other registry":               otherRegistry,
		"no registry":                  {MinTrustLevel: evidence.TrustDeveloper, Identities: []evidence.Identity{ci}},
		"isolated minimum":             pol(evidence.TrustIsolated, ci),
		"developer identity at runner": pol(evidence.TrustDeveloper, ci, runnerDev),
		"developer key at runner":      pol(evidence.TrustDeveloper, ci, evidence.Identity{PublicKey: "k", TrustLevel: evidence.TrustRunner}),
		"other repository":             pol(evidence.TrustDeveloper, evidence.Identity{Issuer: actionsIssuer, SubjectRegexp: `https://github\.com/evil/aval/.*`, TrustLevel: evidence.TrustRunner}),
		"pull request at runner":       pol(evidence.TrustDeveloper, evidence.Identity{Issuer: actionsIssuer, SubjectRegexp: ownWorkflow + `ci\.yaml@refs/pull/.*`, TrustLevel: evidence.TrustRunner}),
		"other issuer":                 pol(evidence.TrustDeveloper, evidence.Identity{Issuer: "https://evil", SubjectRegexp: ci.SubjectRegexp, TrustLevel: evidence.TrustRunner}),
		"wildcard developer":           pol(evidence.TrustDeveloper, ci, evidence.Identity{Issuer: actionsIssuer, SubjectRegexp: ".*", TrustLevel: evidence.TrustDeveloper}),
		"wildcard after own workflow":  pol(evidence.TrustDeveloper, evidence.Identity{Issuer: actionsIssuer, SubjectRegexp: ownWorkflow + ".*|.*", TrustLevel: evidence.TrustRunner}),
		"invalid developer regexp":     pol(evidence.TrustDeveloper, ci, evidence.Identity{Issuer: actionsIssuer, SubjectRegexp: "(", TrustLevel: evidence.TrustDeveloper}),
		"isolated self-grant":          pol(evidence.TrustDeveloper, evidence.Identity{Issuer: actionsIssuer, SubjectRegexp: ci.SubjectRegexp, TrustLevel: evidence.TrustIsolated}),
	}
	for name, p := range bad {
		if len(policyProblems(p)) == 0 {
			t.Errorf("policy with %s: no problem reported", name)
		}
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
// write) runs only on push, never on pull_request or workflow_dispatch (the
// version PR's run, on a ref main does not protect), and runs no test suite,
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
		"no condition":     "jobs:\n  a:\n    permissions: {id-token: write}\n",
		"on PRs":           "jobs:\n  a:\n    if: github.event_name == 'pull_request'\n    permissions: {packages: write}\n",
		"runs the suite":   "jobs:\n  a:\n    if: github.event_name == 'push'\n    permissions: {packages: write}\n    steps: [{run: make ci-e2e}]\n",
		"push or PR":       "jobs:\n  a:\n    if: github.event_name == 'push' || github.event_name == 'pull_request'\n    permissions: {packages: write}\n",
		"on dispatch":      "jobs:\n  a:\n    if: github.event_name == 'workflow_dispatch'\n    permissions: {id-token: write}\n",
		"push or dispatch": "jobs:\n  a:\n    if: github.event_name == 'push' || github.event_name == 'workflow_dispatch'\n    permissions: {packages: write}\n",
		"go test":          "jobs:\n  a:\n    if: github.event_name == 'push'\n    permissions: {id-token: write}\n    steps: [{run: go test ./...}]\n",
		"make test":        "jobs:\n  a:\n    if: github.event_name == 'push'\n    permissions: {id-token: write}\n    steps: [{run: make test-acceptance}]\n",
		"workflow-wide":    "permissions: {packages: write}\njobs:\n  a:\n    steps: [{run: echo}]\n",
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

// dogfoodSuites maps each test of aval.yaml to its suite's directories.
// Update it with runner.command.
var dogfoodSuites = map[string]suiteDirs{
	"aval-engine": {run: []string{"cmd", "internal", "pkg"}},
	"aval-e2e":    {run: []string{"tests/integration", "tests/acceptance"}, built: []string{"cmd", "internal", "pkg"}},
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
	for test, dirs := range dogfoodSuites {
		c, err := contract.Load(filepath.Join(root, "aval.yaml"), test)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range undeclaredReads(t, c, dirs) {
			t.Errorf("%s: the suite reads %s, which is outside scope: a change to it alone would be a false hit", test, f)
		}
	}
}

func TestDogfoodScopeCheckCatchesUndeclaredReads(t *testing.T) {
	c, err := contract.Load(filepath.Join(root, "aval.yaml"), "aval-engine")
	if err != nil {
		t.Fatal(err)
	}
	// Go sources only: testdata, the CLI reference and the fuzz seeds drop out.
	c.Scope.Source = []string{"cmd/**/*.go", "internal/**/*.go", "pkg/**/*.go"}
	got := undeclaredReads(t, c, dogfoodSuites["aval-engine"])
	for _, want := range []string{"pkg/evidence/testdata/verify-report-v1.golden.json", "docs/cli-reference.md", "aval.yaml", "examples/go-compose/aval.yaml"} {
		if !slices.Contains(got, want) {
			t.Errorf("undeclared read of %s not reported (got %v)", want, got)
		}
	}
}

// mainMinTrust is the AVAL_MIN_TRUST_LEVEL of the suite jobs (#214): runner
// on a push to main, the policy's minimum anywhere else.
const mainMinTrust = "${{ github.event_name == 'push' && github.ref == 'refs/heads/main' && 'runner' || '' }}"

// dogfoodJobs maps each job that uses Aval as a user would to the action it
// uses (#216) and, for a suite, its test in aval.yaml and shadow input.
var dogfoodJobs = map[string]struct{ uses, test, shadow string }{
	"ci-e2e":          {runAction, "aval-e2e", ""},
	"dogfood-shadow":  {runAction, "aval-engine", "true"},
	"dogfood-publish": {"./integrations/github/publish", "", ""},
}

type actionJob struct {
	Permissions map[string]string `yaml:"permissions"`
	Env         map[string]string `yaml:"env"`
	Steps       []step            `yaml:"steps"`
}

// dogfoodJobProblems: each dogfood job reaches aval only through its action,
// which installs the pinned, verified release: it builds and runs no aval of
// its own. A suite job has no id-token (the action refuses one anyway) and
// raises the minimum on main; only the publish job has id-token: write. No
// other job of ci.yaml uses the run action with id-token.
func dogfoodJobProblems(ci []byte) ([]string, error) {
	var wf struct {
		Jobs map[string]actionJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(ci, &wf); err != nil {
		return nil, err
	}
	var problems []string
	for name, want := range dogfoodJobs {
		j, ok := wf.Jobs[name]
		if !ok {
			problems = append(problems, name+": missing")
			continue
		}
		problems = append(problems, dogfoodJobStepProblems(name, want.uses, want.test, want.shadow, j.Steps)...)
		if want.uses == runAction {
			if _, ok := j.Permissions["id-token"]; ok {
				problems = append(problems, name+": a suite job must not have id-token")
			}
			if j.Env["AVAL_MIN_TRUST_LEVEL"] != mainMinTrust {
				problems = append(problems, name+": AVAL_MIN_TRUST_LEVEL must be "+mainMinTrust)
			}
		} else if j.Permissions["id-token"] != "write" {
			problems = append(problems, name+": the publish job needs id-token: write")
		}
	}
	for name, j := range wf.Jobs {
		if _, ok := j.Permissions["id-token"]; ok && slices.ContainsFunc(j.Steps, func(s step) bool { return s.Uses == runAction }) {
			problems = append(problems, name+": uses "+runAction+" with id-token")
		}
	}
	slices.Sort(problems)
	return problems, nil
}

// dogfoodJobStepProblems: the job uses action once, with the given test and
// shadow inputs, and no other step mentions aval.
func dogfoodJobStepProblems(name, action, test, shadow string, steps []step) []string {
	var problems []string
	uses := 0
	for _, s := range steps {
		switch {
		case s.Uses == action:
			uses++
			if s.With["test"] != test || s.With["shadow"] != shadow {
				problems = append(problems, name+": "+action+" with test "+strconv.Quote(s.With["test"])+" and shadow "+strconv.Quote(s.With["shadow"]))
			}
		case !statsOnly(s.Run):
			problems = append(problems, name+": runs aval outside "+action)
		}
	}
	if uses != 1 {
		problems = append(problems, name+": must use "+action+" once")
	}
	return problems
}

// statsOnly: every line of run that mentions aval builds the stats binary or
// runs it against the public stats registry, and nothing else on that line
// mentions aval. The informational stats summary is the only such step.
func statsOnly(run string) bool {
	const (
		build = `go build -o "$RUNNER_TEMP/stats-bin/aval" ./cmd/aval`
		stats = `"$RUNNER_TEMP/stats-bin/aval" stats --registry "$AVAL_STATS_REGISTRY" `
	)
	for _, line := range strings.Split(run, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "aval") && line != build && (!strings.HasPrefix(line, stats) || strings.Count(line, "aval") != 1) {
			return false
		}
	}
	return true
}

func TestDogfoodJobsUseTheActions(t *testing.T) {
	problems, err := dogfoodJobProblems(readCI(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

func TestDogfoodJobCheckCatchesDrift(t *testing.T) {
	const (
		env     = "    env: {AVAL_MIN_TRUST_LEVEL: \"" + mainMinTrust + "\"}\n"
		e2e     = "  ci-e2e:\n" + env + "    steps: [{uses: actions/checkout@x}, {uses: ./integrations/github/run, with: {test: aval-e2e}}]\n"
		shadow  = "  dogfood-shadow:\n" + env + "    steps: [{uses: ./integrations/github/run, with: {test: aval-engine, shadow: 'true'}}, {run: '\"$RUNNER_TEMP/stats-bin/aval\" stats --registry \"$AVAL_STATS_REGISTRY\" --since 30d'}]\n"
		publish = "  dogfood-publish:\n    permissions: {id-token: write, packages: write}\n    steps: [{uses: ./integrations/github/publish}]\n"
	)
	good := "jobs:\n" + e2e + shadow + publish
	if p, err := dogfoodJobProblems([]byte(good)); err != nil || len(p) != 0 {
		t.Fatalf("valid workflow reported %v (%v)", p, err)
	}
	bad := map[string]string{
		"suite with id-token":  strings.Replace(good, "  ci-e2e:\n", "  ci-e2e:\n    permissions: {id-token: write}\n", 1),
		"no raised minimum":    strings.Replace(good, env, "", 1),
		"runs aval directly":   strings.Replace(good, "{uses: ./integrations/github/run, with: {test: aval-e2e}}", "{run: aval run -f aval.yaml --test aval-e2e}", 1),
		"builds aval":          strings.Replace(good, "{uses: actions/checkout@x}", "{run: go build -o bin/aval ./cmd/aval}", 1),
		"other test":           strings.Replace(good, "test: aval-e2e", "test: aval-engine", 1),
		"shadow off":           strings.Replace(good, ", shadow: 'true'", "", 1),
		"publish without oidc": strings.Replace(good, "{id-token: write, packages: write}", "{packages: write}", 1),
		"stats elsewhere":      strings.Replace(good, `--registry "$AVAL_STATS_REGISTRY"`, "--registry evil.io/x", 1),
		"stats then run":       strings.Replace(good, "--since 30d", "--since 30d; aval run -f aval.yaml", 1),
		"publish by hand":      strings.Replace(good, "{uses: ./integrations/github/publish}", "{run: aval push -f aval.yaml}", 1),
		"no publish job":       "jobs:\n" + e2e + shadow,
		"other job signs":      good + "  x:\n    permissions: {id-token: write}\n    steps: [{uses: ./integrations/github/run, with: {test: t}}]\n",
	}
	if statsOnly("go build -o \"$RUNNER_TEMP/stats-bin/aval\" ./cmd/aval\naval run -f aval.yaml") {
		t.Error("run beside stats: not reported")
	}
	for name, wf := range bad {
		if p, err := dogfoodJobProblems([]byte(wf)); err != nil || len(p) == 0 {
			t.Errorf("%s: no problem reported (%v)", name, err)
		}
	}
}

// cosignCheckProblems: the job records `command -v cosign` before the run
// action and fails when it differs after (#248), so an action that writes
// GITHUB_PATH breaks Aval's own CI first.
func cosignCheckProblems(steps []step) []string {
	action := slices.IndexFunc(steps, func(s step) bool { return s.Uses == runAction })
	if action < 0 {
		return []string{"no " + runAction + " step"}
	}
	var problems []string
	if !slices.ContainsFunc(steps[:action], func(s step) bool {
		return s.ID == "cosign-before" && strings.Contains(s.Run, "command -v cosign")
	}) {
		problems = append(problems, "no cosign-before step before "+runAction)
	}
	if !slices.ContainsFunc(steps[action+1:], func(s step) bool {
		return s.If == "" && s.Env["BEFORE"] == "${{ steps.cosign-before.outputs.path }}" &&
			strings.Contains(s.Run, "command -v cosign") && strings.Contains(s.Run, `"$now" != "$BEFORE"`) && strings.Contains(s.Run, "exit 1")
	}) {
		problems = append(problems, "no step after "+runAction+" that fails when cosign changed")
	}
	return problems
}

func TestDogfoodChecksTheJobPath(t *testing.T) {
	var wf workflow
	if err := yaml.Unmarshal(readCI(t), &wf); err != nil {
		t.Fatal(err)
	}
	for _, p := range cosignCheckProblems(wf.Jobs["dogfood-shadow"].Steps) {
		t.Error("dogfood-shadow: " + p)
	}
}

func TestCosignCheckCatchesDrift(t *testing.T) {
	before := step{ID: "cosign-before", Run: `echo "path=$(command -v cosign || echo none)" >> "$GITHUB_OUTPUT"`}
	action := step{Uses: runAction}
	after := step{Env: map[string]string{"BEFORE": "${{ steps.cosign-before.outputs.path }}"},
		Run: "now=$(command -v cosign || echo none)\nif [ \"$now\" != \"$BEFORE\" ]; then exit 1; fi"}
	if p := cosignCheckProblems([]step{before, action, after}); len(p) != 0 {
		t.Fatalf("valid steps reported %v", p)
	}
	skipped := after
	skipped.If = "false"
	for name, steps := range map[string][]step{
		"no action":      {before, after},
		"no before":      {action, after},
		"before after":   {action, before, after},
		"no after":       {before, action},
		"after before":   {before, after, action},
		"after skipped":  {before, action, skipped},
		"after compares": {before, action, {Env: after.Env, Run: "command -v cosign"}},
	} {
		if len(cosignCheckProblems(steps)) == 0 {
			t.Errorf("%s: no problem reported", name)
		}
	}
}
