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

// trustStep sets AVAL_POLICY and AVAL_REGISTRY from main on pull requests
// (#134).
const trustStep = ".github/aval/trust-from-base.sh"

// installStep installs the pinned, verified aval release.
const installStep = ".github/aval/install.sh"

var (
	// trustFlag matches --policy and --registry (not --registry-timeout) and
	// their value.
	trustFlag = regexp.MustCompile(`--(policy|registry)(?:=|\s+|$)(\S*)`)
	// allowedFlag is the one form in which a pull_request job may name
	// AVAL_POLICY or AVAL_REGISTRY outside the trust step.
	allowedFlag = regexp.MustCompile(`--(?:policy|registry)[= ]"?\$\{?AVAL_(?:POLICY|REGISTRY)\}?"?`)
	trustVar    = regexp.MustCompile(`\bAVAL_(?:POLICY|REGISTRY)\b`)
	makeCI      = regexp.MustCompile(`\bmake\b.*\sci(?:-e2e)?(?:\s|$)`)
	// pinnedAval: a policy step is one `aval` command (the pinned release on
	// PATH) with no other command, substitution or redirect in it.
	pinnedAval = regexp.MustCompile("^aval\\s(?:[^;&|<>`$\\n]|\\$[{A-Za-z_])*$")
	// registryDecl is the sed in trust-from-base.sh.
	registryDecl = regexp.MustCompile(`(?m)^ *AVAL_REGISTRY: *(.*)$`)
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

// usesPolicy: the step passes --policy, directly or through `make ci` or
// `make ci-e2e` (the Makefile passes $AVAL_POLICY).
func usesPolicy(run string) bool {
	return strings.Contains(run, "--policy") || makeCI.MatchString(run)
}

// mentionsTrustVar: v, as YAML without comments, names AVAL_POLICY or
// AVAL_REGISTRY other than as an allowed --policy/--registry value.
func mentionsTrustVar(v any) bool {
	out, err := yaml.Marshal(v)
	return err != nil || trustVar.Match(allowedFlag.ReplaceAll(out, nil))
}

type policyJob struct {
	If       string `yaml:"if"`
	Defaults any    `yaml:"defaults"`
	Steps    []step `yaml:"steps"`
}

// policySourceProblems: in a workflow that runs on pull requests, only
// trustStep sets AVAL_POLICY and AVAL_REGISTRY (no env, inline assignment,
// make variable, GITHUB_ENV write or shell names them), and a job that uses
// the policy runs nothing from the checkout before its policy steps: remote
// actions and installStep, then trustStep, then the policy steps, each one
// pinned `aval` command whose --policy/--registry name only those variables.
func policySourceProblems(data []byte) ([]string, error) {
	var wf struct {
		On       yaml.Node            `yaml:"on"`
		Defaults any                  `yaml:"defaults"`
		Jobs     map[string]policyJob `yaml:"jobs"`
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if !onPullRequest(wf.On) {
		return nil, nil
	}
	var problems []string
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
			problems = append(problems, name+": names AVAL_POLICY or AVAL_REGISTRY outside "+trustStep+" and the --policy/--registry values")
		}
		if !slices.ContainsFunc(j.Steps, func(s step) bool { return usesPolicy(s.Run) || strings.TrimSpace(s.Run) == trustStep }) {
			continue
		}
		if wf.Defaults != nil || j.Defaults != nil {
			problems = append(problems, name+": sets defaults in a job that uses the policy")
		}
		problems = append(problems, jobPolicyProblems(name, j.Steps)...)
	}
	slices.Sort(problems)
	return slices.Compact(problems), nil
}

// jobPolicyProblems checks the steps of one pull_request job that uses the
// policy.
func jobPolicyProblems(name string, steps []step) []string {
	var problems []string
	trusted, chain := false, false
	for _, s := range steps {
		if s.Shell != "" {
			problems = append(problems, name+": sets shell in a job that uses the policy")
		}
		run := strings.TrimSpace(strings.ReplaceAll(s.Run, "\\\n", " "))
		switch {
		case run == trustStep:
			trusted, chain = true, true
		case usesPolicy(run):
			if !chain {
				problems = append(problems, name+": a policy step does not follow "+trustStep+" directly")
			}
			if !pinnedAval.MatchString(run) {
				problems = append(problems, name+": a policy step must be one command of the pinned aval release: "+run)
			}
			for _, m := range trustFlag.FindAllStringSubmatch(run, -1) {
				if want := "AVAL_" + strings.ToUpper(m[1]); strings.Trim(m[2], `"{}$`) != want {
					problems = append(problems, name+": --"+m[1]+" is not $"+want+": "+m[0])
				}
			}
		case trusted:
			chain = false
		case run != installStep && (run != "" || s.Uses == "" || strings.HasPrefix(s.Uses, "./")):
			problems = append(problems, name+": runs code from the checkout before "+trustStep+": "+run+s.Uses)
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
	const pre = "{uses: actions/checkout@x}, {uses: sigstore/cosign-installer@x}, {run: " + installStep + "}, {run: " + trustStep + "}"
	ok := "on: [push, pull_request]\njobs:\n" +
		"  a:\n    steps: [" + pre + ", {run: \"aval run --registry \\\"$AVAL_REGISTRY\\\" --registry-timeout 1m \\\\\\n  --policy=\\\"${AVAL_POLICY}\\\"\"}, {run: 'aval stats --registry \"$STATS\"'}, {run: make test}]\n" +
		"  b:\n    if: github.event_name == 'push'\n    env: {AVAL_POLICY: p.yaml}\n    steps: [{run: aval run --policy p.yaml}]\n" +
		"  c:\n    if: ${{ always() && github.event_name == 'push' && github.ref == 'refs/heads/main' }}\n    steps: [{run: aval run --policy p.yaml}]\n" +
		"  d:\n    steps: [{run: make test}, {shell: pwsh, run: echo}]\n"
	if p, err := policySourceProblems([]byte(ok)); err != nil || len(p) != 0 {
		t.Fatalf("valid workflow reported %v (%v)", p, err)
	}
	if p, err := policySourceProblems([]byte("on: push\njobs:\n  a:\n    steps: [{run: aval run --policy p.yaml}]\n")); err != nil || len(p) != 0 {
		t.Errorf("push-only workflow reported %v (%v)", p, err)
	}
	const use = "{run: 'aval run --registry \"$AVAL_REGISTRY\" --policy \"$AVAL_POLICY\"'}"
	pr := func(steps string) string { return "on: pull_request\njobs:\n  a:\n    steps: [" + steps + "]\n" }
	bad := map[string]string{
		"literal policy":        pr(pre + ", {run: aval run --policy .github/aval/pr.yaml}"),
		"policy=":               pr(pre + ", {run: aval run --policy=p.yaml}"),
		"other variable":        pr(pre + ", {run: 'aval run --policy \"$POLICY\"'}"),
		"job env policy":        "on: {pull_request: {}}\njobs:\n  a:\n    env: {AVAL_POLICY: .github/aval/pr.yaml}\n    steps: [" + pre + ", " + use + "]\n",
		"step env registry":     pr(pre + ", {env: {AVAL_REGISTRY: evil.io/x}, run: 'aval run --policy \"$AVAL_POLICY\"'}"),
		"literal registry":      pr(pre + ", {run: 'aval run --registry evil.io/x --policy \"$AVAL_POLICY\"'}"),
		"workflow env registry": "on: {pull_request: {}}\nenv: {AVAL_REGISTRY: evil.io/x}\njobs:\n  a:\n    steps: [" + pre + ", " + use + "]\n",
		"no trust step":         pr("{run: " + installStep + "}, " + use),
		"trust step too late":   pr("{run: " + installStep + "}, " + use + ", {run: " + trustStep + "}"),
		"push or PR job":        "on: [push, pull_request]\njobs:\n  a:\n    if: github.event_name == 'push' || github.event_name == 'pull_request'\n    steps: [{run: aval run --policy p.yaml}]\n",
		"negated push":          "on: [push, pull_request]\njobs:\n  a:\n    if: \"!(github.event_name == 'push')\"\n    env: {AVAL_POLICY: evil.yaml}\n    steps: [" + pre + ", " + use + "]\n",
		"not push":              "on: [push, pull_request]\njobs:\n  a:\n    if: github.event_name != 'push'\n    steps: [{run: aval run --policy p.yaml}]\n",
		"pull_request_target":   "on: pull_request_target\njobs:\n  a:\n    steps: [{run: aval verify --policy p.yaml}]\n",
		"policy flag, no value": pr(pre + ", {run: aval run --policy}"),
		"inline assignment":     pr(pre + ", {run: AVAL_POLICY=evil.yaml make ci-e2e}"),
		"make variable":         pr(pre + ", {run: make ci-e2e AVAL_POLICY=evil.yaml AVAL_REGISTRY=evil.io/x}"),
		"make ci-e2e":           pr(pre + ", {run: make ci-e2e}"),
		"make ci":               pr(pre + ", {run: make ci}"),
		"bin/aval":              pr(pre + ", {run: 'bin/aval run --policy \"$AVAL_POLICY\"'}"),
		"two commands":          pr(pre + ", {run: 'aval run --policy \"$AVAL_POLICY\"; ./evil'}"),
		"substitution":          pr(pre + ", {run: 'aval run --policy \"$AVAL_POLICY\" $(./evil)'}"),
		"GITHUB_ENV write":      pr(pre + ", {run: 'echo AVAL_POLICY=evil.yaml >> \"$GITHUB_ENV\"'}, " + use),
		"step in between":       pr(pre + ", {run: ./evil.sh}, " + use),
		"code before trust":     pr("{run: ./evil.sh}, " + pre + ", " + use),
		"local action":          pr("{uses: ./.github/actions/evil}, " + pre + ", " + use),
		"custom shell":          pr(pre + ", {shell: 'env AVAL_POLICY=evil.yaml bash -e {0}', run: 'aval run --policy \"$AVAL_POLICY\"'}"),
		"shell, no variable":    pr(pre + ", {shell: './evil {0}', run: 'aval run --policy \"$AVAL_POLICY\"'}"),
		"job defaults":          "on: pull_request\njobs:\n  a:\n    defaults: {run: {shell: './evil {0}'}}\n    steps: [" + pre + ", " + use + "]\n",
		"workflow defaults":     "on: pull_request\ndefaults: {run: {shell: './evil {0}'}}\njobs:\n  a:\n    steps: [" + pre + ", " + use + "]\n",
		"make with ci-e2e":      pr(pre + ", {run: make -k ci-e2e test}"),
		"env, make ci":          "on: pull_request\njobs:\n  a:\n    env: {AVAL_POLICY: evil.yaml}\n    steps: [{run: make test}]\n",
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
var codeOwned = []string{"/.github/aval/", "/.github/workflows/", "/tests/architecture/", "/AGENTS.md", "/Makefile", "/aval*.yaml"}

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
	// As on a GitHub-hosted runner, so a script that behaves differently
	// there is caught; it cannot catch every hostile edit, which is why
	// CODEOWNERS must be enforced (#134). BASE_REF names a branch the PR
	// author controls: the script must ignore it and read main.
	tmp := filepath.Join(t.TempDir(), "_temp")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(tmp, "env")
	cmd := exec.Command("bash", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GITHUB_ACTIONS=true", "CI=true", "RUNNER_OS=Linux", "RUNNER_ENVIRONMENT=github-hosted",
		"GITHUB_EVENT_NAME="+event, "BASE_REF=evil-base", "GITHUB_BASE_REF=evil-base", "RUNNER_TEMP="+tmp, "GITHUB_ENV="+envFile)
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

// A PR that edits pr.yaml and the registry, or targets a base branch other
// than main, does not change the policy or registry its own CI uses; on push
// the checkout (main) is used (#134).
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
	// A base branch the attacker controls trusts the attacker (#134).
	git(t, origin, "checkout", "-qb", "evil-base")
	writeFiles(t, origin, map[string]string{".github/aval/pr.yaml": "evil base trusts the attacker\n"})
	git(t, origin, "commit", "-qam", "evil base")
	git(t, origin, "checkout", "-q", "main")
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
		t.Errorf("pull_request: policy %q, registry %q, want main's", got, vars["AVAL_REGISTRY"])
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
