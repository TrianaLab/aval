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

// trustStep sets AVAL_POLICY and AVAL_REGISTRY from the base branch on pull
// requests (#134).
const trustStep = ".github/aval/trust-from-base.sh"

var (
	// trustFlag matches --policy and --registry (not --registry-timeout) and
	// their value.
	trustFlag = regexp.MustCompile(`--(policy|registry)(?:=|\s+|$)(\S*)`)
	// registryDecl is the sed in trust-from-base.sh.
	registryDecl = regexp.MustCompile(`(?m)^ *AVAL_REGISTRY: *(.*)$`)
)

// pushOnly: the job's condition limits it to push events.
func pushOnly(cond string) bool {
	return strings.Contains(cond, "github.event_name == 'push'") && !strings.Contains(cond, "pull_request") && !strings.Contains(cond, "||")
}

// onPullRequest: the workflow triggers on pull_request or
// pull_request_target, whatever form `on` takes.
func onPullRequest(on yaml.Node) bool {
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
	return slices.Contains(names, "pull_request") || slices.Contains(names, "pull_request_target")
}

// usesPolicy: the step passes --policy, directly or through `make ci-e2e`
// (the Makefile passes $AVAL_POLICY).
func usesPolicy(run string) bool {
	return strings.Contains(run, "--policy") || strings.Contains(run, "make ci-e2e")
}

// policySourceProblems: a job that can run on a pull request and passes
// --policy takes the policy and the registry from trustStep, run before any
// step that uses them, and never from the working tree: no AVAL_POLICY or
// AVAL_REGISTRY in its env, and --policy/--registry name only those variables.
func policySourceProblems(data []byte) ([]string, error) {
	var wf struct {
		On   yaml.Node         `yaml:"on"`
		Env  map[string]string `yaml:"env"`
		Jobs map[string]struct {
			If    string            `yaml:"if"`
			Env   map[string]string `yaml:"env"`
			Steps []step            `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, err
	}
	if !onPullRequest(wf.On) {
		return nil, nil
	}
	var problems []string
	for _, k := range []string{"AVAL_POLICY", "AVAL_REGISTRY"} {
		if _, ok := wf.Env[k]; ok {
			problems = append(problems, "sets "+k+" in the workflow env")
		}
	}
	for name, j := range wf.Jobs {
		if !pushOnly(j.If) {
			problems = append(problems, jobPolicyProblems(name, j.Env, j.Steps)...)
		}
	}
	slices.Sort(problems)
	return slices.Compact(problems), nil
}

// jobPolicyProblems checks one job that can run on a pull request.
func jobPolicyProblems(name string, jobEnv map[string]string, steps []step) []string {
	if !slices.ContainsFunc(steps, func(s step) bool { return usesPolicy(s.Run) }) {
		return nil
	}
	var problems []string
	envs := []map[string]string{jobEnv}
	trusted := false
	for _, s := range steps {
		envs = append(envs, s.Env)
		if strings.TrimSpace(s.Run) == trustStep {
			trusted = true
			continue
		}
		if !usesPolicy(s.Run) {
			continue
		}
		if !trusted {
			problems = append(problems, name+": uses a policy before "+trustStep)
		}
		for _, m := range trustFlag.FindAllStringSubmatch(s.Run, -1) {
			if want := "AVAL_" + strings.ToUpper(m[1]); strings.Trim(m[2], `"{}$`) != want {
				problems = append(problems, name+": --"+m[1]+" is not $"+want+": "+m[0])
			}
		}
	}
	for _, env := range envs {
		for _, k := range []string{"AVAL_POLICY", "AVAL_REGISTRY"} {
			if _, ok := env[k]; ok {
				problems = append(problems, name+": sets "+k+" in env instead of taking it from "+trustStep)
			}
		}
	}
	return problems
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
		"  a:\n    steps: [{run: " + trustStep + "}, {run: make ci-e2e}, {run: 'aval run --registry \"$AVAL_REGISTRY\" --registry-timeout 1m --policy=\"${AVAL_POLICY}\"'}, {run: 'aval stats --registry \"$STATS\"'}]\n" +
		"  b:\n    if: github.event_name == 'push'\n    env: {POLICY: p.yaml}\n    steps: [{run: aval run --policy p.yaml}]\n" +
		"  c:\n    steps: [{run: make test}]\n"
	if p, err := policySourceProblems([]byte(ok)); err != nil || len(p) != 0 {
		t.Fatalf("valid workflow reported %v (%v)", p, err)
	}
	if p, err := policySourceProblems([]byte("on: push\njobs:\n  a:\n    steps: [{run: aval run --policy p.yaml}]\n")); err != nil || len(p) != 0 {
		t.Errorf("push-only workflow reported %v (%v)", p, err)
	}
	bad := map[string]string{
		"literal policy":        "on: pull_request\njobs:\n  a:\n    steps: [{run: " + trustStep + "}, {run: aval run --policy .github/aval/pr.yaml}]\n",
		"policy=":               "on: [pull_request]\njobs:\n  a:\n    steps: [{run: " + trustStep + "}, {run: aval run --policy=p.yaml}]\n",
		"other variable":        "on: {pull_request: {}}\njobs:\n  a:\n    steps: [{run: " + trustStep + "}, {run: 'aval run --policy \"$POLICY\"'}]\n",
		"job env policy":        "on: {pull_request: {}}\njobs:\n  a:\n    env: {AVAL_POLICY: .github/aval/pr.yaml}\n    steps: [{run: " + trustStep + "}, {run: make ci-e2e}]\n",
		"step env registry":     "on: {pull_request: {}}\njobs:\n  a:\n    steps: [{run: " + trustStep + "}, {env: {AVAL_REGISTRY: evil.io/x}, run: make ci-e2e}]\n",
		"literal registry":      "on: {pull_request: {}}\njobs:\n  a:\n    steps: [{run: " + trustStep + "}, {run: 'aval run --registry evil.io/x --policy \"$AVAL_POLICY\"'}]\n",
		"workflow env registry": "on: {pull_request: {}}\nenv: {AVAL_REGISTRY: evil.io/x}\njobs:\n  a:\n    steps: [{run: " + trustStep + "}, {run: make ci-e2e}]\n",
		"no trust step":         "on: {pull_request: {}}\njobs:\n  a:\n    steps: [{run: make ci-e2e}]\n",
		"trust step too late":   "on: {pull_request: {}}\njobs:\n  a:\n    steps: [{run: make ci-e2e}, {run: " + trustStep + "}]\n",
		"push or PR job":        "on: [push, pull_request]\njobs:\n  a:\n    if: github.event_name == 'push' || github.event_name == 'pull_request'\n    steps: [{run: aval run --policy p.yaml}]\n",
		"pull_request_target":   "on: pull_request_target\njobs:\n  a:\n    steps: [{run: aval verify --policy p.yaml}]\n",
		"policy flag, no value": "on: pull_request\njobs:\n  a:\n    steps: [{run: " + trustStep + "}, {run: aval run --policy}]\n",
	}
	for name, wf := range bad {
		if p, err := policySourceProblems([]byte(wf)); err != nil || len(p) == 0 {
			t.Errorf("%s: no problem reported (%v)", name, err)
		}
	}
}

// ci.yaml declares exactly one registry, which trust-from-base.sh reads from
// the base branch.
func TestCIDeclaresOneRegistry(t *testing.T) {
	var values []string
	for _, m := range registryDecl.FindAllStringSubmatch(string(readCI(t)), -1) {
		values = append(values, m[1])
	}
	if slices.Sort(values); len(slices.Compact(values)) != 1 {
		t.Errorf("ci.yaml declares AVAL_REGISTRY values %q, want exactly one", values)
	}
}

// codeOwned lists the paths whose change can alter what CI trusts or what
// the gates enforce (#134).
var codeOwned = []string{"/.github/aval/", "/.github/workflows/", "/tests/architecture/", "/AGENTS.md"}

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

// runTrustStep runs trust-from-base.sh in dir for event and returns the
// variables it exported.
func runTrustStep(t *testing.T, dir, event string) (map[string]string, error) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join(root, trustStep))
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	envFile := filepath.Join(tmp, "env")
	cmd := exec.Command("bash", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GITHUB_EVENT_NAME="+event, "BASE_REF=main", "RUNNER_TEMP="+tmp, "GITHUB_ENV="+envFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%w\n%s", err, out)
	}
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		vars[k] = v
	}
	return vars, nil
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A PR that edits pr.yaml and the registry does not change the policy or
// registry its own CI uses; on push the checkout (main) is used (#134).
func TestTrustStepReadsTheBaseBranch(t *testing.T) {
	origin, clone := t.TempDir(), t.TempDir()
	git(t, origin, "init", "-q")
	writeFiles(t, origin, map[string]string{
		".github/aval/pr.yaml":       "base pr policy\n",
		".github/aval/main.yaml":     "base main policy\n",
		".github/workflows/ci.yaml":  "jobs:\n  a:\n    env:\n      AVAL_REGISTRY: ghcr.io/base/attestations\n  b:\n    env:\n      AVAL_REGISTRY: ghcr.io/base/attestations\n",
		".github/aval/unrelated.txt": "x\n",
	})
	git(t, origin, "add", "-A")
	git(t, origin, "commit", "-qm", "base")
	git(t, clone, "clone", "-q", "file://"+origin, ".")

	vars, err := runTrustStep(t, clone, "push")
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := readAbs(t, vars["AVAL_POLICY"]); got != "base main policy\n" || vars["AVAL_REGISTRY"] != "ghcr.io/base/attestations" {
		t.Errorf("push: policy %q, registry %q", got, vars["AVAL_REGISTRY"])
	}

	// The PR head trusts its own identity and its own registry.
	git(t, clone, "checkout", "-qb", "pr")
	writeFiles(t, clone, map[string]string{
		".github/aval/pr.yaml":      "pr adds its own identity\n",
		".github/workflows/ci.yaml": "jobs:\n  a:\n    env:\n      AVAL_REGISTRY: evil.io/x\n",
	})
	git(t, clone, "commit", "-qam", "pr")
	vars, err = runTrustStep(t, clone, "pull_request")
	if err != nil {
		t.Fatalf("pull_request: %v", err)
	}
	if got := readAbs(t, vars["AVAL_POLICY"]); got != "base pr policy\n" || vars["AVAL_REGISTRY"] != "ghcr.io/base/attestations" {
		t.Errorf("pull_request: policy %q, registry %q, want the base branch's", got, vars["AVAL_REGISTRY"])
	}

	// A base branch with two registries, or none, fails closed.
	for name, ci := range map[string]string{
		"two registries": "jobs:\n  a:\n    env:\n      AVAL_REGISTRY: ghcr.io/a/x\n  b:\n    env:\n      AVAL_REGISTRY: ghcr.io/b/x\n",
		"no registry":    "jobs: {}\n",
		"quoted":         "env:\n  AVAL_REGISTRY: \"ghcr.io/a/x\"\n",
	} {
		writeFiles(t, origin, map[string]string{".github/workflows/ci.yaml": ci})
		git(t, origin, "commit", "-qam", name)
		if _, err := runTrustStep(t, clone, "pull_request"); err == nil {
			t.Errorf("%s: trust step succeeded", name)
		}
	}
}

func readAbs(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
