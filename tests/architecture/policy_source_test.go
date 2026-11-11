package architecture

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// runAction is how ci.yaml uses the aval run action (#211, #216). It
// fetches the trust policy, and the registry inside it, from the default
// branch on every event, so no job needs a policy of its own.
const runAction = "./integrations/github/run"

var (
	trustVar = regexp.MustCompile(`\bAVAL_(?:POLICY|REGISTRY)\b`)
	// checkoutPolicy: a --policy flag or an AVAL_POLICY value that names a
	// file under .github/aval/, which in a workflow is the checkout's copy.
	// The run action's policy input names a path in the default branch.
	checkoutPolicy = regexp.MustCompile(`(?:--policy(?:=|\s+)|AVAL_POLICY(?::\s*|=))["']?(?:\./)?\.github/aval/`)
)

// pushOnly: the job's condition is a conjunction with the exact term
// `github.event_name == 'push'`. A condition with `!` or `||` is not.
func pushOnly(cond string) bool {
	cond = strings.Join(strings.Fields(cond), " ")
	cond = strings.TrimSuffix(strings.TrimPrefix(cond, "${{ "), " }}")
	if strings.Contains(cond, "!") || strings.Contains(cond, "||") || strings.Contains(cond, "pull_request") {
		return false
	}
	return slices.ContainsFunc(strings.Split(cond, "&&"), func(t string) bool {
		return strings.TrimSpace(t) == "github.event_name == 'push'"
	})
}

// onPullRequest: the workflow triggers on pull_request or
// pull_request_target, whatever form `on` takes.
func onPullRequest(on yaml.Node) bool {
	names := triggers(on)
	return slices.Contains(names, "pull_request") || slices.Contains(names, "pull_request_target")
}

// triggers: the event names in `on`, whatever form it takes.
func triggers(on yaml.Node) []string {
	var names []string
	switch on.Kind {
	case yaml.ScalarNode:
		names = []string{on.Value}
	case yaml.SequenceNode:
		_ = on.Decode(&names)
	case yaml.MappingNode:
		for i := 0; i < len(on.Content); i += 2 {
			names = append(names, on.Content[i].Value)
		}
	}
	return names
}

// dispatchUnchecked: the workflow runs on workflow_dispatch but not on
// pull_request, so policySourceProblems skips it. ci.yaml's dispatch runs
// (the version PR) take the same jobs as its pull_request runs, so only
// keeping pull_request in its `on` keeps them checked. release.yaml is
// dispatch-only on purpose: its release job is tag-gated.
func dispatchUnchecked(data []byte) (bool, error) {
	var wf struct {
		On yaml.Node `yaml:"on"`
	}
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return false, err
	}
	return slices.Contains(triggers(wf.On), "workflow_dispatch") && !onPullRequest(wf.On), nil
}

func TestCIDispatchStaysPolicyChecked(t *testing.T) {
	if bad, err := dispatchUnchecked(readCI(t)); err != nil || bad {
		t.Errorf("ci.yaml runs on workflow_dispatch without pull_request, so policySourceProblems skips it (%v)", err)
	}
	for name, wf := range map[string]string{
		"dispatch without PR": "on: [push, workflow_dispatch]\njobs:\n  a:\n    steps: [{run: aval run --policy .github/aval/main.yaml}]\n",
		"mapping form":        "on: {push: {}, workflow_dispatch: {}}\n",
	} {
		if bad, err := dispatchUnchecked([]byte(wf)); err != nil || !bad {
			t.Errorf("%s: not reported (%v)", name, err)
		}
	}
	if bad, _ := dispatchUnchecked([]byte("on: [push, pull_request, workflow_dispatch]\n")); bad {
		t.Error("dispatch with pull_request reported")
	}
}

// mentionsTrustVar: v, as YAML without comments, names AVAL_POLICY or
// AVAL_REGISTRY.
func mentionsTrustVar(v any) bool {
	out, err := yaml.Marshal(v)
	return err != nil || trustVar.Match(out)
}

type policyJob struct {
	If    string            `yaml:"if"`
	Env   map[string]string `yaml:"env"`
	Steps []step            `yaml:"steps"`
}

// envProblems: env sets a variable that runs code before, or changes, the
// program the run action's bash steps execute (PATH, BASH_ENV, ENV).
func envProblems(where string, env map[string]string) []string {
	var problems []string
	for _, k := range []string{"PATH", "BASH_ENV", "ENV"} {
		if _, ok := env[k]; ok {
			problems = append(problems, where+" sets "+k)
		}
	}
	return problems
}

// runActionEnvProblems: envProblems of every env a job that uses runAction
// sees, on any event.
func runActionEnvProblems(env map[string]string, jobs map[string]policyJob) []string {
	var problems []string
	for name, j := range jobs {
		if slices.ContainsFunc(j.Steps, func(s step) bool { return s.Uses == runAction }) {
			problems = append(problems, envProblems("workflow", env)...)
			problems = append(problems, envProblems(name, j.Env)...)
			for _, s := range j.Steps {
				problems = append(problems, envProblems(name+" step", s.Env)...)
			}
		}
	}
	return problems
}

// policySourceProblems: no step of any workflow reads Aval's trust policy
// from the checkout (#216). In a workflow that runs on pull requests, a job
// that is not push-only passes no --policy and names neither AVAL_POLICY nor
// AVAL_REGISTRY, anywhere (env, inline assignment, make variable, GITHUB_ENV
// write): such a job reuses results only through runAction, which takes the
// policy and its registry from the default branch.
func policySourceProblems(data []byte) ([]string, error) {
	var wf struct {
		On   yaml.Node            `yaml:"on"`
		Env  map[string]string    `yaml:"env"`
		Jobs map[string]policyJob `yaml:"jobs"`
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	var problems []string
	for _, line := range strings.Split(string(data), "\n") {
		if l := strings.TrimSpace(line); !strings.HasPrefix(l, "#") && checkoutPolicy.MatchString(l) {
			problems = append(problems, "reads the trust policy from the checkout: "+l)
		}
	}
	problems = append(problems, runActionEnvProblems(wf.Env, wf.Jobs)...)
	if !onPullRequest(wf.On) {
		return problems, nil
	}
	rawJobs, _ := raw["jobs"].(map[string]any)
	delete(raw, "jobs")
	if mentionsTrustVar(raw) {
		problems = append(problems, "names AVAL_POLICY or AVAL_REGISTRY outside the jobs")
	}
	for name, j := range wf.Jobs {
		if pushOnly(j.If) {
			continue
		}
		if mentionsTrustVar(rawJobs[name]) {
			problems = append(problems, name+": names AVAL_POLICY or AVAL_REGISTRY; use "+runAction)
		}
		for _, s := range j.Steps {
			if strings.Contains(s.Run, "--policy") {
				problems = append(problems, name+": passes --policy; use "+runAction)
			}
		}
	}
	slices.Sort(problems)
	return slices.Compact(problems), nil
}

func TestPRPolicyComesFromBaseBranch(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no workflows: %v", err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		problems, err := policySourceProblems(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range problems {
			t.Error(filepath.Base(f) + ": " + p)
		}
	}
}

func TestPolicySourceCheckCatchesWorkingTreePolicies(t *testing.T) {
	ok := "on: [push, pull_request]\njobs:\n" +
		"  a:\n    steps: [{uses: actions/checkout@x}, {uses: " + runAction + ", with: {test: unit}}, {run: 'aval stats --registry \"$STATS\"'}, {run: make ci-e2e}]\n" +
		"  b:\n    if: github.event_name == 'push'\n    env: {AVAL_POLICY: p.yaml}\n    steps: [{run: aval run --policy p.yaml}]\n" +
		"  c:\n    if: ${{ always() && github.event_name == 'push' && github.ref == 'refs/heads/main' }}\n    steps: [{run: 'git show origin/main:.github/aval/policy.yaml > p.yaml'}]\n" +
		"  d:\n    steps: [{uses: " + runAction + ", with: {test: unit, policy: .github/aval/policy.yaml}}]\n"
	if p, err := policySourceProblems([]byte(ok)); err != nil || len(p) != 0 {
		t.Fatalf("valid workflow reported %v (%v)", p, err)
	}
	if p, err := policySourceProblems([]byte("on: push\njobs:\n  a:\n    steps: [{run: aval run --policy p.yaml}]\n")); err != nil || len(p) != 0 {
		t.Errorf("push-only workflow reported %v (%v)", p, err)
	}
	pr := func(steps string) string { return "on: pull_request\njobs:\n  a:\n    steps: [" + steps + "]\n" }
	bad := map[string]string{
		"literal policy":        pr("{run: aval run --policy p.yaml}"),
		"policy=":               pr("{run: aval run --policy=p.yaml}"),
		"variable policy":       pr("{run: 'aval verify --policy \"$POLICY\"'}"),
		"job env policy":        "on: {pull_request: {}}\njobs:\n  a:\n    env: {AVAL_POLICY: p.yaml}\n    steps: [{run: make ci-e2e}]\n",
		"step env registry":     pr("{env: {AVAL_REGISTRY: evil.io/x}, run: aval run}"),
		"workflow env registry": "on: {pull_request: {}}\nenv: {AVAL_REGISTRY: evil.io/x}\njobs:\n  a:\n    steps: [{run: aval run}]\n",
		"inline assignment":     pr("{run: AVAL_POLICY=evil.yaml make ci-e2e}"),
		"make variable":         pr("{run: make ci-e2e AVAL_POLICY=evil.yaml}"),
		"GITHUB_ENV write":      pr("{run: 'echo AVAL_POLICY=evil.yaml >> \"$GITHUB_ENV\"'}"),
		"push or PR job":        "on: [push, pull_request]\njobs:\n  a:\n    if: github.event_name == 'push' || github.event_name == 'pull_request'\n    steps: [{run: aval run --policy p.yaml}]\n",
		"negated push":          "on: [push, pull_request]\njobs:\n  a:\n    if: \"!(github.event_name == 'push')\"\n    env: {AVAL_POLICY: evil.yaml}\n    steps: [{run: aval run}]\n",
		"not push":              "on: [push, pull_request]\njobs:\n  a:\n    if: github.event_name != 'push'\n    steps: [{run: aval run --policy p.yaml}]\n",
		"dispatch job":          "on: [push, pull_request, workflow_dispatch]\njobs:\n  a:\n    if: github.event_name == 'workflow_dispatch'\n    steps: [{run: aval run --policy p.yaml}]\n",
		"pull_request_target":   "on: pull_request_target\njobs:\n  a:\n    steps: [{run: aval verify --policy p.yaml}]\n",
		// BASH_ENV, ENV and PATH run code before the run action's bash steps, on any event.
		"job BASH_ENV":  "on: push\njobs:\n  a:\n    env: {BASH_ENV: x}\n    steps: [{uses: " + runAction + ", with: {test: unit}}]\n",
		"workflow PATH": "on: push\nenv: {PATH: x}\njobs:\n  a:\n    steps: [{uses: " + runAction + ", with: {test: unit}}]\n",
		"step ENV":      "on: push\njobs:\n  a:\n    steps: [{env: {ENV: x}, run: 'true'}, {uses: " + runAction + ", with: {test: unit}}]\n",
		// Push-only and dispatch-only workflows read main's copy too.
		"checkout policy on push": "on: push\njobs:\n  a:\n    steps: [{run: aval verify --policy .github/aval/policy.yaml}]\n",
		"checkout policy, ./":     "on: workflow_dispatch\njobs:\n  a:\n    steps: [{run: 'aval verify --policy=./.github/aval/policy.yaml'}]\n",
		"checkout policy in env":  "on: push\njobs:\n  a:\n    env: {AVAL_POLICY: .github/aval/policy.yaml}\n    steps: [{run: aval verify}]\n",
		"checkout policy inline":  "on: push\njobs:\n  a:\n    steps: [{run: AVAL_POLICY=.github/aval/policy.yaml aval verify}]\n",
	}
	for name, wf := range bad {
		if p, err := policySourceProblems([]byte(wf)); err != nil || len(p) == 0 {
			t.Errorf("%s: no problem reported (%v)", name, err)
		}
	}
}

// codeOwned lists the paths whose change can alter what CI trusts or what
// the gates enforce (#134).
var codeOwned = []string{"/.github/aval/", "/integrations/", "/.github/workflows/", "/tests/architecture/", "/AGENTS.md", "/Makefile", "/aval*.yaml"}

func TestCodeOwnersCoverTrust(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(root, ".github", "CODEOWNERS"))
	if err != nil {
		t.Fatal(err)
	}
	owned := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Fields(line); len(f) > 1 && !strings.HasPrefix(f[0], "#") {
			owned[f[0]] = true
		}
	}
	for _, p := range codeOwned {
		if !owned[p] {
			t.Errorf(".github/CODEOWNERS has no owner for %s", p)
		}
	}
}

// The scripts of the aval run and publish actions use /bin/bash and call
// their tools by absolute path, so an earlier step that writes GITHUB_PATH
// cannot shim them (#134, #211, #212). cosign comes from get-cosign.sh, a
// private install the actions put first on get-aval.sh's PATH (#248).
func TestTrustScriptsCallToolsByAbsolutePath(t *testing.T) {
	bare := regexp.MustCompile(`(?m)^[^#\n]*(?:^|[^/\w-])(?:git|sed|sort|grep|curl|sha256sum|tar|mkdir|bash|jq|awk|rm|base64|tr)\s`)
	for _, f := range []string{"integrations/github/run/guard.sh", "integrations/github/run/run.sh", "integrations/github/publish/guard.sh", "integrations/github/publish/publish.sh", "integrations/github/get-cosign.sh", "integrations/github/policy-registry.sh"} {
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(data), "#!/bin/bash\n") {
			t.Errorf("%s: shebang is not #!/bin/bash", f)
		}
		if m := bare.FindAllString(string(data), -1); len(m) > 0 {
			t.Errorf("%s calls tools from PATH: %q", f, m)
		}
	}
}

// A job that uses the run action waits for ci-gates, so when the gate fails
// its check is skipped instead of green (#134).
func TestPolicyJobsNeedGates(t *testing.T) {
	var wf workflow
	if err := yaml.Unmarshal(readCI(t), &wf); err != nil {
		t.Fatal(err)
	}
	for name, j := range wf.Jobs {
		if slices.ContainsFunc(j.Steps, func(s step) bool { return s.Uses == runAction }) && !slices.Contains(j.Needs, "ci-gates") {
			t.Errorf("%s uses %s but does not need ci-gates", name, runAction)
		}
	}
}
