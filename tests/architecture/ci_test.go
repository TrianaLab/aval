package architecture

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// #135: summary.sh gates every aval report in CI and the release job. A
// verification failure fails the step even when a trusted sibling verified
// (an entry in rejected); a below-minimum signer (rejected miss) does not
// (invariant 4).
func TestSummaryFailsOnSecurityEvent(t *testing.T) {
	cases := []struct {
		name, report string
		fail         bool
	}{
		{"verified", `{"outcome":"verified","result":"passed"}`, false},
		{"verified beside a below-minimum signer", `{"outcome":"verified","result":"passed","rejected":[{"reference":"r@sha256:bb","outcome":"miss","reason":"x"}]}`, false},
		{"miss", `{"outcome":"miss"}`, false},
		{"verification failed", `{"outcome":"verification-failed"}`, true},
		{"verified beside an untrusted signer", `{"outcome":"verified","result":"passed","rejected":[{"reference":"r@sha256:bb","outcome":"verification-failed","reason":"x"}]}`, true},
		{"error beside an untrusted signer", `{"outcome":"error","rejected":[{"reference":"r@sha256:aa","outcome":"error","reason":"x"},{"reference":"r@sha256:bb","outcome":"verification-failed","reason":"x"}]}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			report, summary := filepath.Join(dir, "report.json"), filepath.Join(dir, "summary.md")
			if err := os.WriteFile(report, []byte(tc.report), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", filepath.Join(root, ".github", "aval", "summary.sh"), "e2e", report)
			cmd.Env = append(os.Environ(), "GITHUB_STEP_SUMMARY="+summary)
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail {
				t.Fatalf("summary.sh error %v, want failure %v; output %s", err, tc.fail, out)
			}
			if tc.fail && !strings.Contains(string(out), "::error title=aval verification failed (e2e)::") {
				t.Fatalf("no error annotation: %s", out)
			}
		})
	}
}

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

// pinnedLegs run in CI through the pinned aval release instead of `make`,
// with the same contract: `make ci-e2e` would verify with bin/aval and the
// Makefile from the PR (#134).
var pinnedLegs = map[string]string{"ci-e2e": "aval run -f aval.e2e.yaml "}

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
		if !slices.ContainsFunc(job.Steps, func(s step) bool {
			if want, ok := pinnedLegs[leg]; ok {
				return strings.HasPrefix(strings.TrimSpace(s.Run), want)
			}
			return strings.TrimSpace(s.Run) == "make "+leg
		}) {
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
// would let a change rewrite a committed golden unseen.
const goldenBase = "${{ github.event_name == 'push' && github.event.before || format('origin/{0}', github.base_ref || 'main') }}"

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
