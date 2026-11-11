package architecture

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const aggregator = "required"

type workflow struct {
	Jobs map[string]struct {
		If              string    `yaml:"if"`
		Needs           yamlNeeds `yaml:"needs"`
		Steps           []step    `yaml:"steps"`
		ContinueOnError string    `yaml:"continue-on-error"`
	} `yaml:"jobs"`
}

type step struct {
	If              string            `yaml:"if"`
	Env             map[string]string `yaml:"env"`
	Run             string            `yaml:"run"`
	Uses            string            `yaml:"uses"`
	Shell           string            `yaml:"shell"`
	With            map[string]string `yaml:"with"`
	ContinueOnError string            `yaml:"continue-on-error"`
}

// yamlNeeds accepts both `needs: job` and `needs: [a, b]`.
type yamlNeeds []string

func (n *yamlNeeds) UnmarshalYAML(v *yaml.Node) error {
	if v.Kind == yaml.ScalarNode {
		*n = []string{v.Value}
		return nil
	}
	return v.Decode((*[]string)(n))
}

// aggregatorProblems lists every way the workflow lets a job go red without
// blocking a merge.
func aggregatorProblems(data []byte) ([]string, error) {
	var wf workflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, err
	}
	agg, ok := wf.Jobs[aggregator]
	if !ok {
		return []string{fmt.Sprintf("no %q aggregator job", aggregator)}, nil
	}
	var problems []string
	if strings.TrimSpace(agg.If) != "always()" {
		problems = append(problems, fmt.Sprintf("%s: needs `if: always()` so it reports when a leg fails", aggregator))
	}
	for name := range wf.Jobs {
		if name != aggregator && !slices.Contains(agg.Needs, name) {
			problems = append(problems, fmt.Sprintf("job %q is not in %s.needs", name, aggregator))
		}
	}
	slices.Sort(problems)
	return problems, nil
}

func readCI(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A CI job outside the aggregator can fail and the PR still merges.
func TestEveryCIJobIsAggregated(t *testing.T) {
	problems, err := aggregatorProblems(readCI(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

func TestAggregatorCheckCatchesUnwiredJobs(t *testing.T) {
	cases := map[string]string{
		"unwired job": `
jobs:
  a: {}
  new-leg: {}
  required: {if: always(), needs: [a]}`,
		"no always": `
jobs:
  a: {}
  required: {needs: a}`,
		"no aggregator": `
jobs:
  a: {}`,
	}
	for name, wf := range cases {
		problems, err := aggregatorProblems([]byte(wf))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(problems) == 0 {
			t.Errorf("%s: no problem reported", name)
		}
	}
	ok := "jobs:\n  a: {}\n  required: {if: always(), needs: a}\n"
	if problems, _ := aggregatorProblems([]byte(ok)); len(problems) != 0 {
		t.Errorf("valid workflow reported %v", problems)
	}
}

// pinnedLegs run in CI through integrations/github/run and its pinned
// release instead of `make`, with the same test: `make ci-e2e` would verify
// with bin/aval and the Makefile from the PR (#134, #216).
var pinnedLegs = map[string]string{"ci-e2e": "aval-e2e"}

// runsLeg: step s runs `make leg`, or the run action on its pinnedLegs test.
func runsLeg(leg string, s step) bool {
	if test, ok := pinnedLegs[leg]; ok {
		return s.Uses == runAction && s.With["test"] == test
	}
	return strings.TrimSpace(s.Run) == "make "+leg
}

// Every leg of `make ci` runs in the CI job of the same name and has a row in
// the AGENTS.md Commands table, so local gates, CI and docs cannot drift apart.
func TestMakeCILegsMirrorCIJobs(t *testing.T) {
	mk, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^ci:(.*)$`).FindSubmatch(mk)
	if m == nil {
		t.Fatal("Makefile has no ci target")
	}
	legs := strings.Fields(string(m[1]))
	if len(legs) == 0 {
		t.Fatal("make ci has no legs")
	}
	var wf workflow
	if err := yaml.Unmarshal(readCI(t), &wf); err != nil {
		t.Fatal(err)
	}
	for _, leg := range legs {
		job, ok := wf.Jobs[leg]
		if !ok {
			t.Errorf("make leg %s has no CI job %q", leg, leg)
			continue
		}
		if !slices.ContainsFunc(job.Steps, func(s step) bool { return runsLeg(leg, s) }) {
			t.Errorf("CI job %s must run `make %s` (or its pinnedLegs command)", leg, leg)
		}
	}
	agents, err := os.ReadFile(filepath.Join("..", "..", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, leg := range legs {
		if !strings.Contains(string(agents), "| `make "+leg+"` |") {
			t.Errorf("AGENTS.md Commands table has no row for `make %s`", leg)
		}
	}
	for name := range wf.Jobs {
		if strings.HasPrefix(name, "ci-") && !slices.Contains(legs, name) {
			t.Errorf("CI job %s is not a leg of `make ci`", name)
		}
	}
}

var (
	usesLine  = regexp.MustCompile(`(?m)^\s*(?:-\s*)?uses:\s*['"]?([^'"\s#]+)`)
	pinnedSHA = regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`)
)

// unpinnedActions lists every `uses:` that is not a local action or a full
// commit SHA. A tag or branch can be moved to new code; a SHA cannot.
func unpinnedActions(data []byte) []string {
	var bad []string
	for _, m := range usesLine.FindAllSubmatch(data, -1) {
		ref := string(m[1])
		if !strings.HasPrefix(ref, "./") && !pinnedSHA.MatchString(ref) {
			bad = append(bad, ref)
		}
	}
	return bad
}

func TestActionsArePinnedBySHA(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.y*ml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no workflows found")
	}
	// The composite actions (#211) pin their dependencies the same way.
	actions, err := filepath.Glob(filepath.Join("..", "..", "integrations", "github", "*", "action.yml"))
	if err != nil || len(actions) == 0 {
		t.Fatalf("no actions under integrations/github: %v", err)
	}
	files = append(files, actions...)
	// So do the example workflows readers copy (#217).
	examples, err := filepath.Glob(filepath.Join("..", "..", "examples", "*", ".github", "workflows", "*.y*ml"))
	if err != nil || len(examples) == 0 {
		t.Fatalf("no example workflows: %v", err)
	}
	files = append(files, examples...)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, ref := range unpinnedActions(data) {
			t.Errorf("%s: %s is not pinned by commit SHA", filepath.Base(f), ref)
		}
	}
}

func TestPinCheckCatchesTagsAndBranches(t *testing.T) {
	wf := `
jobs:
  a:
    uses: org/repo/.github/workflows/x.yaml@main
    steps:
      - uses: actions/checkout@v4
      - uses: "actions/setup-go@v5.6.0"
      - uses: actions/cache@0123456789abcdef0123456789abcdef0123456 # 39 hex
      - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4.4.0
      - uses: ./local-action
`
	got := unpinnedActions([]byte(wf))
	want := []string{
		"org/repo/.github/workflows/x.yaml@main",
		"actions/checkout@v4",
		"actions/setup-go@v5.6.0",
		"actions/cache@0123456789abcdef0123456789abcdef0123456",
	}
	if !slices.Equal(got, want) {
		t.Errorf("unpinned = %v, want %v", got, want)
	}
}

// goldenBase is the only AVAL_GOLDEN_BASE the false-hit job may set: the
// commit before the push, the PR base branch, or origin/main on a dispatch
// (the version PR). Anything else (HEAD, the PR branch, the dispatched ref)
// would let a change rewrite a committed golden unseen. A push with an empty
// `before` gets a base that does not resolve, so the job fails closed.
const goldenBase = "${{ github.event_name == 'push' && (github.event.before || 'no-push-before') || format('origin/{0}', github.base_ref || 'main') }}"

// falseHitRecipe is the only recipe `make ci-false-hit` may have: both
// packages, uncached, nothing skipped, and AVAL_GOLDEN_BASE always set (an
// unset base is the only way TestGoldensAppendOnly skips).
const falseHitRecipe = "\tAVAL_GOLDEN_BASE=\"$${AVAL_GOLDEN_BASE-origin/main}\" go test -count=1 ./internal/fingerprint/... ./internal/contract/...\n"

// falseHitGateProblems lists every way the false-hit gate (#15) could stop
// binding: dropped from `make ci` (and so, by TestMakeCILegsMirrorCIJobs,
// from CI), its recipe narrowed, made conditional or allowed to fail
// (continue-on-error), or run without full history or against a base the PR
// chooses.
func falseHitGateProblems(data, mk []byte) ([]string, error) {
	const name = "ci-false-hit"
	var wf workflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, err
	}
	var problems []string
	m := regexp.MustCompile(`(?m)^ci:(.*)$`).FindSubmatch(mk)
	if m == nil || !slices.Contains(strings.Fields(string(m[1])), name) {
		problems = append(problems, "make ci has no "+name+" leg")
	}
	if !strings.Contains(string(mk), "\n"+name+":\n"+falseHitRecipe) {
		problems = append(problems, "make "+name+" recipe must be exactly: "+strings.TrimSpace(falseHitRecipe))
	}
	job, ok := wf.Jobs[name]
	if !ok {
		return append(problems, "no "+name+" CI job"), nil
	}
	if job.If != "" || len(job.Needs) > 0 {
		problems = append(problems, name+" must run on every PR and push: no if, no needs")
	}
	mayFail := func(v string) bool { return v != "" && v != "false" }
	if mayFail(job.ContinueOnError) || slices.ContainsFunc(job.Steps, func(s step) bool { return mayFail(s.ContinueOnError) }) {
		problems = append(problems, name+" must not set continue-on-error: a failing test would report success to "+aggregator)
	}
	if !slices.ContainsFunc(job.Steps, func(s step) bool {
		return strings.HasPrefix(s.Uses, "actions/checkout@") && s.With["fetch-depth"] == "0"
	}) {
		problems = append(problems, name+" must check out with fetch-depth: 0")
	}
	if !slices.ContainsFunc(job.Steps, func(s step) bool {
		return strings.TrimSpace(s.Run) == "make "+name && s.Env["AVAL_GOLDEN_BASE"] == goldenBase
	}) {
		problems = append(problems, name+" must run `make "+name+"` with AVAL_GOLDEN_BASE: "+goldenBase)
	}
	return problems, nil
}

func TestFalseHitGateIsBinding(t *testing.T) {
	mk, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	problems, err := falseHitGateProblems(readCI(t), mk)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

func TestFalseHitGateCheckCatchesLooseGates(t *testing.T) {
	const env = "        env:\n          AVAL_GOLDEN_BASE: " + goldenBase + "\n"
	const good = `
jobs:
  ci-false-hit:
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
        with: {fetch-depth: 0}
      - run: make ci-false-hit
` + env
	const recipe = falseHitRecipe
	const mk = "ci: ci-static ci-false-hit\n\nci-false-hit:\n" + recipe
	for _, wf := range []string{good, strings.Replace(good, "    steps:", "    continue-on-error: false\n    steps:", 1)} {
		if p, err := falseHitGateProblems([]byte(wf), []byte(mk)); err != nil || len(p) != 0 {
			t.Fatalf("valid gate reported %v, %v", p, err)
		}
	}
	cases := map[string]struct{ wf, mk string }{
		"no leg":                 {good, strings.Replace(mk, " ci-false-hit\n", "\n", 1)},
		"recipe emptied":         {good, strings.Replace(mk, recipe, "", 1)},
		"recipe skips":           {good, strings.Replace(mk, "-count=1 ", "-count=1 -skip Golden ", 1)},
		"recipe cached":          {good, strings.Replace(mk, "-count=1 ", "", 1)},
		"recipe narrowed":        {good, strings.Replace(mk, " ./internal/contract/...", "", 1)},
		"recipe without a base":  {good, strings.Replace(mk, "AVAL_GOLDEN_BASE=\"$${AVAL_GOLDEN_BASE-origin/main}\" ", "", 1)},
		"empty base becomes set": {good, strings.Replace(mk, "BASE-origin", "BASE:-origin", 1)},
		"no job":                 {"jobs: {}\n", mk},
		"conditional":            {strings.Replace(good, "    steps:", "    if: false\n    steps:", 1), mk},
		"needs":                  {strings.Replace(good, "    steps:", "    needs: changes\n    steps:", 1), mk},
		"job continue-on-error":  {strings.Replace(good, "    steps:", "    continue-on-error: true\n    steps:", 1), mk},
		"step continue-on-error": {strings.Replace(good, "      - run: make ci-false-hit\n", "      - run: make ci-false-hit\n        continue-on-error: ${{ true }}\n", 1), mk},
		"shallow":                {strings.Replace(good, "{fetch-depth: 0}", "{fetch-depth: 1}", 1), mk},
		"base is HEAD":           {strings.Replace(good, goldenBase, "HEAD", 1), mk},
		"no base":                {strings.Replace(good, env, "", 1), mk},
	}
	for name, tc := range cases {
		if tc.wf == good && tc.mk == mk {
			t.Fatalf("%s: case changes nothing", name)
		}
		p, err := falseHitGateProblems([]byte(tc.wf), []byte(tc.mk))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(p) == 0 {
			t.Errorf("%s: not reported", name)
		}
	}
}

// dispatchGuard is the first step of `changes` in a ci.yaml that runs on
// workflow_dispatch: only the version PR and pin PR (#213) branches may be
// dispatched.
const dispatchGuard = "github.event_name == 'workflow_dispatch' && github.ref != 'refs/heads/changeset-release/main' && github.ref != 'refs/heads/release-pins/main'"

// dispatchProblems: the concurrency group separates events, so a dispatch on
// main cannot cancel main's push run, and a workflow_dispatch trigger comes
// with the ref guard as the first step of `changes`.
func dispatchProblems(data []byte) ([]string, error) {
	var wf struct {
		On          yaml.Node `yaml:"on"`
		Concurrency struct {
			Group string `yaml:"group"`
		} `yaml:"concurrency"`
	}
	var jobs workflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, &jobs); err != nil {
		return nil, err
	}
	var problems []string
	if !strings.Contains(wf.Concurrency.Group, "github.event_name") {
		problems = append(problems, "concurrency group must include github.event_name: "+wf.Concurrency.Group)
	}
	if !slices.Contains(triggers(wf.On), "workflow_dispatch") {
		return problems, nil
	}
	steps := jobs.Jobs["changes"].Steps
	if len(steps) == 0 || steps[0].If != dispatchGuard || !strings.HasSuffix(strings.TrimSpace(steps[0].Run), "exit 1") {
		problems = append(problems, "changes: first step must be `if: "+dispatchGuard+"` ending in `exit 1`")
	}
	return problems, nil
}

func TestCIDispatchIsGuarded(t *testing.T) {
	problems, err := dispatchProblems(readCI(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

func TestDispatchCheckCatchesLooseGuards(t *testing.T) {
	guard := "      - if: " + dispatchGuard + "\n        run: |\n          echo no\n          exit 1\n"
	good := "on: [push, pull_request, workflow_dispatch]\nconcurrency:\n  group: ci-${{ github.event_name }}-${{ github.ref }}\njobs:\n  changes:\n    steps:\n" +
		guard + "      - uses: actions/checkout@x\n"
	if p, err := dispatchProblems([]byte(good)); err != nil || len(p) != 0 {
		t.Fatalf("valid workflow reported %v (%v)", p, err)
	}
	cases := map[string]string{
		"group without event": strings.Replace(good, "${{ github.event_name }}-", "", 1),
		"guard removed":       strings.Replace(good, guard, "", 1),
		"other ref":           strings.Replace(good, "changeset-release/main", "evil", 1),
		"other pin ref":       strings.Replace(good, "release-pins/main", "evil", 1),
		"guard does not fail": strings.Replace(good, "exit 1", "exit 0", 1),
	}
	for name, wf := range cases {
		if wf == good {
			t.Fatalf("%s: case changes nothing", name)
		}
		if p, err := dispatchProblems([]byte(wf)); err != nil || len(p) == 0 {
			t.Errorf("%s: no problem reported (%v)", name, err)
		}
	}
}

// runActionRef is how a workflow uses the aval run action (#211).
const runActionRef = "TrianaLab/aval/integrations/github/run@"

// actionExampleProblems checks the workflow docs/guides/github-actions.md
// shows against the action: every input it passes exists, every required
// input is passed, and no job that uses the action can sign (id-token), at
// job or workflow level.
func actionExampleProblems(example, action []byte) ([]string, error) {
	var a struct {
		Inputs map[string]struct {
			Required bool `yaml:"required"`
		} `yaml:"inputs"`
	}
	var wf struct {
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
			Steps       []step            `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(action, &a); err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(example, &wf); err != nil {
		return nil, err
	}
	var problems []string
	uses := 0
	for name, j := range wf.Jobs {
		for _, s := range j.Steps {
			if !strings.HasPrefix(s.Uses, runActionRef) {
				continue
			}
			uses++
			if _, ok := j.Permissions["id-token"]; ok || wf.Permissions["id-token"] != "" {
				problems = append(problems, name+": a job that uses the run action must not have id-token")
			}
			for in := range s.With {
				if _, ok := a.Inputs[in]; !ok {
					problems = append(problems, name+": input "+in+" is not in action.yml")
				}
			}
			for in, spec := range a.Inputs {
				if _, ok := s.With[in]; spec.Required && !ok {
					problems = append(problems, name+": required input "+in+" is missing")
				}
			}
		}
	}
	if uses == 0 {
		problems = append(problems, "no job uses "+runActionRef)
	}
	slices.Sort(problems)
	return problems, nil
}

func TestGitHubActionExampleMatchesTheAction(t *testing.T) {
	p, err := actionExampleProblems([]byte(readFile(t, "docs/includes/github-actions.yml")), []byte(readFile(t, "integrations/github/run/action.yml")))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p {
		t.Error(s)
	}
}

func TestActionExampleCheckCatchesDrift(t *testing.T) {
	action := "inputs:\n  test: {required: true}\n  file: {default: aval.yaml}\n"
	bad := "permissions: {id-token: write}\njobs:\n  a:\n    steps:\n      - uses: " + runActionRef + "x\n        with: {tests: u}\n"
	got, err := actionExampleProblems([]byte(bad), []byte(action))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a: a job that uses the run action must not have id-token", "a: input tests is not in action.yml", "a: required input test is missing"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, _ := actionExampleProblems([]byte("jobs: {}\n"), []byte(action)); len(got) != 1 {
		t.Errorf("no use of the action: got %q", got)
	}
}

// publishActionRef is how a workflow uses the aval publish action (#212).
const publishActionRef = "TrianaLab/aval/integrations/github/publish@"

// publishCondition and publishPermissions are what the documented publish
// job must have: a push to the default branch, and only the scopes signing
// and pushing need.
const publishCondition = "${{ !cancelled() && github.event_name == 'push' && github.ref == format('refs/heads/{0}', github.event.repository.default_branch) }}"

var publishPermissions = map[string]string{"contents": "read", "packages": "write", "id-token": "write"}

// publishExampleProblems checks the publish job docs/guides/github-actions.md
// shows against the publish action: its condition, its permissions and the
// inputs it passes.
func publishExampleProblems(example, action []byte) ([]string, error) {
	var a struct {
		Inputs map[string]any `yaml:"inputs"`
	}
	var wf struct {
		Jobs map[string]permJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(action, &a); err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(example, &wf); err != nil {
		return nil, err
	}
	var problems []string
	uses := 0
	for name, j := range wf.Jobs {
		for _, s := range j.Steps {
			if !strings.HasPrefix(s.Uses, publishActionRef) {
				continue
			}
			uses++
			if j.If != publishCondition {
				problems = append(problems, name+": condition is not "+publishCondition)
			}
			if !maps.Equal(j.Permissions, publishPermissions) {
				problems = append(problems, name+": permissions are not contents: read, packages: write, id-token: write")
			}
			for in := range s.With {
				if _, ok := a.Inputs[in]; !ok {
					problems = append(problems, name+": input "+in+" is not in action.yml")
				}
			}
		}
	}
	if uses == 0 {
		problems = append(problems, "no job uses "+publishActionRef)
	}
	slices.Sort(problems)
	return problems, nil
}

func TestGitHubPublishExampleMatchesTheAction(t *testing.T) {
	p, err := publishExampleProblems([]byte(readFile(t, "docs/includes/github-actions.yml")), []byte(readFile(t, "integrations/github/publish/action.yml")))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p {
		t.Error(s)
	}
}

func TestPublishExampleCheckCatchesDrift(t *testing.T) {
	action := "inputs:\n  file: {default: aval.yaml}\n"
	bad := "jobs:\n  p:\n    if: github.event_name == 'push'\n    permissions: {contents: write, id-token: write}\n    steps:\n      - uses: " + publishActionRef + "x\n        with: {files: u}\n"
	got, err := publishExampleProblems([]byte(bad), []byte(action))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"p: condition is not " + publishCondition, "p: input files is not in action.yml", "p: permissions are not contents: read, packages: write, id-token: write"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, _ := publishExampleProblems([]byte("jobs: {}\n"), []byte(action)); len(got) != 1 {
		t.Errorf("no use of the action: got %q", got)
	}
}

// installStep is the step of an action.yml that installs the pinned aval
// release, as YAML, or "" when there is none.
func installStep(t *testing.T, path string) string {
	t.Helper()
	var a struct {
		Runs struct {
			Steps []map[string]any `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal([]byte(readFile(t, path)), &a); err != nil {
		t.Fatal(err)
	}
	for _, s := range a.Runs.Steps {
		if s["id"] == "install" {
			out, err := yaml.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			return string(out)
		}
	}
	return ""
}

// Pinning either action by commit SHA pins the same verified aval release
// (#212): the run and publish actions install it with identical steps.
func TestActionsInstallTheSameRelease(t *testing.T) {
	run, publish := installStep(t, "integrations/github/run/action.yml"), installStep(t, "integrations/github/publish/action.yml")
	if run == "" || run != publish {
		t.Errorf("the install steps of the run and publish actions differ:\n--- run\n%s--- publish\n%s", run, publish)
	}
}
