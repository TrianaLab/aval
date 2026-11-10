package architecture

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// trustScript sets AVAL_POLICY and AVAL_REGISTRY from main on pull requests
// (#134); installScript installs the pinned, verified aval release. The
// registry may also come from the policy's registry field (#209): the policy
// is main's copy too, so a step that relies on it needs no --registry.
const (
	trustScript   = ".github/aval/trust-from-base.sh"
	installScript = ".github/aval/install.sh"
)

// trustStep is the one step that sets the policy and installs aval in a job
// that uses the policy: it runs main's copies of trustScript and
// installScript, not the PR's, with /bin/bash. If main has no trustScript
// (before #134 lands) it sets no policy, so nothing is reused (fail closed).
const trustStep = `/usr/bin/git fetch --no-tags --depth=1 origin +refs/heads/main:refs/remotes/origin/main
/usr/bin/git show origin/main:.github/aval/install.sh > "$RUNNER_TEMP/install.sh"
if /usr/bin/git show origin/main:.github/aval/trust-from-base.sh > "$RUNNER_TEMP/trust-from-base.sh"; then
  /bin/bash "$RUNNER_TEMP/trust-from-base.sh"
else
  echo "::warning::main has no .github/aval/trust-from-base.sh: no trust policy, nothing is reused"
fi
/bin/bash "$RUNNER_TEMP/install.sh"`

// remoteBeforeTrust are the only actions a job that uses the policy may run
// before trustStep: each can write GITHUB_PATH, so each is trusted not to
// shim the tools trustStep calls. setup-uv (SHA-pinned) is there for ci-e2e
// only, whose acceptance tests run examples/uv-monorepo; no other policy job
// should use it.
var remoteBeforeTrust = []string{"actions/checkout@", "actions/setup-go@", "sigstore/cosign-installer@", "astral-sh/setup-uv@"}

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
	// readsTrustEnv: an aval command that reads AVAL_POLICY and
	// AVAL_REGISTRY from the environment when no flag names them (#163).
	// Flags before the command count too.
	readsTrustEnv = regexp.MustCompile(`(?:^|/)aval(?:\s+-\S+(?:\s+[^-\s]\S*)?)*\s+(?:run|verify)\b`)
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
	If       string            `yaml:"if"`
	Defaults any               `yaml:"defaults"`
	Env      map[string]string `yaml:"env"`
	Steps    []step            `yaml:"steps"`
}

// envProblems: env sets a variable that changes which program runs or runs
// code before it (PATH, BASH_ENV, ENV).
func envProblems(where string, env map[string]string) []string {
	var problems []string
	for _, k := range []string{"PATH", "BASH_ENV", "ENV"} {
		if _, ok := env[k]; ok {
			problems = append(problems, where+" sets "+k)
		}
	}
	return problems
}

// policySourceProblems: in a workflow that runs on pull requests, only
// trustStep sets AVAL_POLICY and AVAL_REGISTRY (no env, inline assignment,
// make variable, GITHUB_ENV write or shell names them), and a job that uses
// the policy runs nothing from the checkout before its policy steps: allowed
// remote actions, then trustStep, then the policy steps, each one pinned
// `aval` command whose --policy/--registry name only those variables.
// Neither the workflow, the job nor a policy step sets PATH, BASH_ENV or ENV.
func policySourceProblems(data []byte) ([]string, error) {
	var wf struct {
		On       yaml.Node            `yaml:"on"`
		Defaults any                  `yaml:"defaults"`
		Env      map[string]string    `yaml:"env"`
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
			problems = append(problems, name+": names AVAL_POLICY or AVAL_REGISTRY outside the trust step and the --policy/--registry values")
		}
		if !slices.ContainsFunc(j.Steps, func(s step) bool { return usesPolicy(s.Run) || strings.TrimSpace(s.Run) == trustStep }) {
			continue
		}
		if wf.Defaults != nil || j.Defaults != nil {
			problems = append(problems, name+": sets defaults in a job that uses the policy")
		}
		problems = append(problems, envProblems(name+": the workflow", wf.Env)...)
		problems = append(problems, envProblems(name+": a job that uses the policy", j.Env)...)
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
		case usesPolicy(run) || trusted && readsTrustEnv.MatchString(run):
			// After the trust step, aval run and verify use the policy
			// even without --policy: they read $AVAL_POLICY.
			problems = append(problems, envProblems(name+": a policy step", s.Env)...)
			if !chain {
				problems = append(problems, name+": a policy step does not follow the trust step directly")
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
		case run != "" || !slices.ContainsFunc(remoteBeforeTrust, func(p string) bool { return strings.HasPrefix(s.Uses, p) }):
			problems = append(problems, name+": runs code other than "+strings.Join(remoteBeforeTrust, ", ")+" before the trust step: "+run+s.Uses)
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
	step := func(run string) string { return "{run: " + strconv.Quote(run) + "}" }
	pre := "{uses: actions/checkout@x}, {uses: actions/setup-go@x}, {uses: sigstore/cosign-installer@x}, " + step(trustStep)
	ok := "on: [push, pull_request]\njobs:\n" +
		"  a:\n    steps: [" + pre + ", {run: \"aval run --registry \\\"$AVAL_REGISTRY\\\" --registry-timeout 1m \\\\\\n  --policy=\\\"${AVAL_POLICY}\\\"\"}, {run: 'aval stats --registry \"$STATS\"'}, {run: make test}]\n" +
		"  b:\n    if: github.event_name == 'push'\n    env: {AVAL_POLICY: p.yaml}\n    steps: [{run: aval run --policy p.yaml}]\n" +
		"  c:\n    if: ${{ always() && github.event_name == 'push' && github.ref == 'refs/heads/main' }}\n    steps: [{run: aval run --policy p.yaml}]\n" +
		"  d:\n    steps: [{run: make test}, {shell: pwsh, run: echo}]\n" +
		"  e:\n    steps: [" + pre + ", {run: 'aval verify --policy \"$AVAL_POLICY\"'}]\n"
	if p, err := policySourceProblems([]byte(ok)); err != nil || len(p) != 0 {
		t.Fatalf("valid workflow reported %v (%v)", p, err)
	}
	if p, err := policySourceProblems([]byte("on: push\njobs:\n  a:\n    steps: [{run: aval run --policy p.yaml}]\n")); err != nil || len(p) != 0 {
		t.Errorf("push-only workflow reported %v (%v)", p, err)
	}
	const use = "{run: 'aval run --registry \"$AVAL_REGISTRY\" --policy \"$AVAL_POLICY\"'}"
	pr := func(steps string) string { return "on: pull_request\njobs:\n  a:\n    steps: [" + steps + "]\n" }
	bad := map[string]string{
		"literal policy":         pr(pre + ", {run: aval run --policy .github/aval/pr.yaml}"),
		"policy=":                pr(pre + ", {run: aval run --policy=p.yaml}"),
		"other variable":         pr(pre + ", {run: 'aval run --policy \"$POLICY\"'}"),
		"job env policy":         "on: {pull_request: {}}\njobs:\n  a:\n    env: {AVAL_POLICY: .github/aval/pr.yaml}\n    steps: [" + pre + ", " + use + "]\n",
		"step env registry":      pr(pre + ", {env: {AVAL_REGISTRY: evil.io/x}, run: 'aval run --policy \"$AVAL_POLICY\"'}"),
		"literal registry":       pr(pre + ", {run: 'aval run --registry evil.io/x --policy \"$AVAL_POLICY\"'}"),
		"registry over policy's": pr(pre + ", {run: 'aval verify --registry=evil.io/x --policy \"$AVAL_POLICY\"'}"),
		"workflow env registry":  "on: {pull_request: {}}\nenv: {AVAL_REGISTRY: evil.io/x}\njobs:\n  a:\n    steps: [" + pre + ", " + use + "]\n",
		"no trust step":          pr("{run: " + installScript + "}, " + use),
		"trust step too late":    pr(use + ", " + step(trustStep)),
		"head's scripts":         pr("{run: " + trustScript + "}, {run: " + installScript + "}, " + use),
		"bash from PATH":         pr(step(strings.ReplaceAll(trustStep, "/bin/bash", "bash")) + ", " + use),
		"head copy":              pr(step(strings.ReplaceAll(trustStep, "origin/main:", "HEAD:")) + ", " + use),
		"other remote action":    pr("{uses: evil/setup@x}, " + step(trustStep) + ", " + use),
		"lookalike action":       pr("{uses: actions/checkout-evil@x}, " + step(trustStep) + ", " + use),
		"step after trust":       pr(step(trustStep) + ", {uses: actions/checkout@x}, " + use),
		"PATH override":          "on: pull_request\njobs:\n  a:\n    env: {PATH: './bin:/usr/bin'}\n    steps: [" + pre + ", " + use + "]\n",
		"workflow PATH":          "on: pull_request\nenv: {PATH: './bin:/usr/bin'}\njobs:\n  a:\n    steps: [" + pre + ", " + use + "]\n",
		"BASH_ENV":               pr(pre + ", {env: {BASH_ENV: ./evil.sh}, run: 'aval run --policy \"$AVAL_POLICY\"'}"),
		"push or PR job":         "on: [push, pull_request]\njobs:\n  a:\n    if: github.event_name == 'push' || github.event_name == 'pull_request'\n    steps: [{run: aval run --policy p.yaml}]\n",
		"negated push":           "on: [push, pull_request]\njobs:\n  a:\n    if: \"!(github.event_name == 'push')\"\n    env: {AVAL_POLICY: evil.yaml}\n    steps: [" + pre + ", " + use + "]\n",
		"not push":               "on: [push, pull_request]\njobs:\n  a:\n    if: github.event_name != 'push'\n    steps: [{run: aval run --policy p.yaml}]\n",
		"dispatch job":           "on: [push, pull_request, workflow_dispatch]\njobs:\n  a:\n    if: github.event_name == 'workflow_dispatch'\n    steps: [{run: aval run --policy .github/aval/main.yaml}]\n",
		"push or dispatch":       "on: [push, pull_request, workflow_dispatch]\njobs:\n  a:\n    if: github.event_name == 'push' || github.event_name == 'workflow_dispatch'\n    env: {AVAL_POLICY: evil.yaml}\n    steps: [" + pre + ", " + use + "]\n",
		"pull_request_target":    "on: pull_request_target\njobs:\n  a:\n    steps: [{run: aval verify --policy p.yaml}]\n",
		"policy flag, no value":  pr(pre + ", {run: aval run --policy}"),
		"inline assignment":      pr(pre + ", {run: AVAL_POLICY=evil.yaml make ci-e2e}"),
		"make variable":          pr(pre + ", {run: make ci-e2e AVAL_POLICY=evil.yaml AVAL_REGISTRY=evil.io/x}"),
		"make ci-e2e":            pr(pre + ", {run: make ci-e2e}"),
		"make ci":                pr(pre + ", {run: make ci}"),
		"bin/aval":               pr(pre + ", {run: 'bin/aval run --policy \"$AVAL_POLICY\"'}"),
		"two commands":           pr(pre + ", {run: 'aval run --policy \"$AVAL_POLICY\"; ./evil'}"),
		"substitution":           pr(pre + ", {run: 'aval run --policy \"$AVAL_POLICY\" $(./evil)'}"),
		"GITHUB_ENV write":       pr(pre + ", {run: 'echo AVAL_POLICY=evil.yaml >> \"$GITHUB_ENV\"'}, " + use),
		"step in between":        pr(pre + ", {run: ./evil.sh}, " + use),
		"code before trust":      pr("{run: ./evil.sh}, " + pre + ", " + use),
		"local action":           pr("{uses: ./.github/actions/evil}, " + pre + ", " + use),
		"custom shell":           pr(pre + ", {shell: 'env AVAL_POLICY=evil.yaml bash -e {0}', run: 'aval run --policy \"$AVAL_POLICY\"'}"),
		"shell, no variable":     pr(pre + ", {shell: './evil {0}', run: 'aval run --policy \"$AVAL_POLICY\"'}"),
		"job defaults":           "on: pull_request\njobs:\n  a:\n    defaults: {run: {shell: './evil {0}'}}\n    steps: [" + pre + ", " + use + "]\n",
		"workflow defaults":      "on: pull_request\ndefaults: {run: {shell: './evil {0}'}}\njobs:\n  a:\n    steps: [" + pre + ", " + use + "]\n",
		"make with ci-e2e":       pr(pre + ", {run: make -k ci-e2e test}"),
		"env policy after code":  pr(pre + ", {run: make test}, {run: aval run}"),
		"env policy, flag first": pr(pre + ", {run: make test}, {run: aval -f x.yaml run}"),
		"env, make ci":           "on: pull_request\njobs:\n  a:\n    env: {AVAL_POLICY: evil.yaml}\n    steps: [{run: make test}]\n",
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

// runTrustStep runs trust-from-base.sh in dir for event on ref and returns
// the variables it exported.
func runTrustStep(t *testing.T, dir, event, ref string) (map[string]string, error) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join(root, trustScript))
	if err != nil {
		t.Fatal(err)
	}
	// As on a GitHub-hosted runner. This checks main's script, not what a
	// hostile PR runs: on pull_request the PR's own ci.yaml runs, so only
	// required code-owner review keeps such a PR off main (#134). BASE_REF
	// names a branch the PR author controls: the script must ignore it.
	tmp := filepath.Join(t.TempDir(), "_temp")
	if err := os.Mkdir(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(tmp, "env")
	cmd := exec.Command("/bin/bash", script)
	cmd.Dir = dir
	cmd.Env = append(runnerEnv(event, tmp, dir), "GITHUB_REF="+ref)
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

// runnerEnv is the environment of a GitHub-hosted runner for event, with
// RUNNER_TEMP tmp (GITHUB_ENV in it) and workspace ws.
func runnerEnv(event, tmp, ws string) []string {
	return append(os.Environ(), "GITHUB_ACTIONS=true", "CI=true", "RUNNER_OS=Linux", "RUNNER_ENVIRONMENT=github-hosted",
		"GITHUB_RUN_ID=1", "GITHUB_RUN_ATTEMPT=1", "GITHUB_WORKSPACE="+ws, "GITHUB_REPOSITORY=TrianaLab/aval",
		"GITHUB_SERVER_URL=https://github.com", "GITHUB_WORKFLOW=ci", "GITHUB_SHA=0000000000000000000000000000000000000000",
		"GITHUB_REF=refs/pull/1/merge", "GITHUB_HEAD_REF=pr", "GITHUB_ACTOR=attacker",
		"GITHUB_EVENT_NAME="+event, "BASE_REF=evil-base", "GITHUB_BASE_REF=evil-base",
		"RUNNER_TEMP="+tmp, "GITHUB_ENV="+filepath.Join(tmp, "env"))
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
// than main, does not change the policy or registry its own CI uses, nor does
// a dispatched ref (the version PR) that edits main.yaml; on push the
// checkout (main) is used (#134).
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

	vars, err := runTrustStep(t, clone, "push", "refs/heads/main")
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
		".github/aval/main.yaml":    "head adds its own identity\n",
		".github/workflows/ci.yaml": "jobs:\n  a:\n    env:\n      AVAL_REGISTRY: evil.io/x\n",
	})
	git(t, clone, "commit", "-qam", "pr")
	// Every event but a push to main reads main's pr.yaml: an unknown event,
	// or a push to another branch, fails closed too.
	for _, c := range [][2]string{
		{"pull_request", "refs/pull/1/merge"},
		{"workflow_dispatch", "refs/heads/changeset-release/main"},
		{"workflow_run", "refs/heads/main"},
		{"push", "refs/heads/pr"},
	} {
		vars, err = runTrustStep(t, clone, c[0], c[1])
		if err != nil {
			t.Fatalf("%s on %s: %v", c[0], c[1], err)
		}
		if got := readAbs(t, vars["AVAL_POLICY"]); got != "base pr policy\n" || vars["AVAL_REGISTRY"] != "ghcr.io/base/attestations" {
			t.Errorf("%s on %s: policy %q, registry %q, want main's", c[0], c[1], got, vars["AVAL_REGISTRY"])
		}
	}

	// A base branch with two registries, or none, fails closed.
	for name, ci := range map[string]string{
		"two registries": "jobs:\n  a:\n    env:\n      AVAL_REGISTRY: ghcr.io/a/x\n  b:\n    env:\n      AVAL_REGISTRY: ghcr.io/b/x\n",
		"no registry":    "jobs: {}\n",
		"quoted":         "env:\n  AVAL_REGISTRY: \"ghcr.io/a/x\"\n",
	} {
		writeFiles(t, origin, map[string]string{".github/workflows/ci.yaml": ci})
		git(t, origin, "commit", "-qam", name)
		if _, err := runTrustStep(t, clone, "pull_request", "refs/pull/1/merge"); err == nil {
			t.Errorf("%s: trust step succeeded", name)
		}
	}
}

// The scripts the trust step runs use /bin/bash and call their tools by
// absolute path, so an earlier step that writes GITHUB_PATH cannot shim them
// (#134); so do the scripts of the aval run action (#211). cosign comes
// from sigstore/cosign-installer, an allowed action.
func TestTrustScriptsCallToolsByAbsolutePath(t *testing.T) {
	bare := regexp.MustCompile(`(?m)^[^#\n]*(?:^|[^/\w-])(?:git|sed|sort|grep|curl|sha256sum|tar|mkdir|bash|jq|awk|rm)\s`)
	for _, f := range []string{trustScript, installScript, "integrations/github/run/guard.sh", "integrations/github/run/run.sh"} {
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

// A job that uses the policy waits for ci-gates, so when the gate fails its
// check is skipped instead of green (#134).
func TestPolicyJobsNeedGates(t *testing.T) {
	var wf workflow
	if err := yaml.Unmarshal(readCI(t), &wf); err != nil {
		t.Fatal(err)
	}
	for name, j := range wf.Jobs {
		if slices.ContainsFunc(j.Steps, func(s step) bool { return strings.TrimSpace(s.Run) == trustStep }) && !slices.Contains(j.Needs, "ci-gates") {
			t.Errorf("%s runs the trust step but does not need ci-gates", name)
		}
	}
}

// runnerEnv sets what a GitHub-hosted runner sets, so the test runs the
// trust script as a real runner would (#134).
func TestTrustStepEnvLooksLikeARunner(t *testing.T) {
	probe := `for v in GITHUB_ACTIONS CI RUNNER_OS RUNNER_ENVIRONMENT GITHUB_RUN_ID GITHUB_RUN_ATTEMPT GITHUB_WORKSPACE GITHUB_REPOSITORY GITHUB_SERVER_URL GITHUB_WORKFLOW GITHUB_SHA GITHUB_REF GITHUB_HEAD_REF GITHUB_ACTOR; do [ -n "${!v:-}" ] || { echo "missing $v"; exit 1; }; done`
	cmd := exec.Command("bash", "-c", probe)
	cmd.Env = runnerEnv("pull_request", t.TempDir(), "/ws")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%v: %s", err, out)
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
