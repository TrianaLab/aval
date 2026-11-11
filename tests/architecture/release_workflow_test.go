package architecture

import (
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// autoTagGuards: auto-tag runs on workflow_run of ci, which a fork PR from a
// branch named main also matches (workflow_run.branches filters head_branch),
// so every clause is needed before it holds a write token.
var autoTagGuards = []string{
	"github.event_name == 'workflow_run'",
	"github.event.workflow_run.conclusion == 'success'",
	"github.event.workflow_run.event == 'push'",
	"github.event.workflow_run.head_branch == 'main'",
	"github.event.workflow_run.head_repository.full_name == github.repository",
}

// versionPRPermissions: version-pr writes the version PR, nothing else. It
// runs npm code, so it never holds actions write: with contents write that
// could tag a commit and dispatch the release on it.
var versionPRPermissions = map[string]string{"contents": "write", "pull-requests": "write"}

// versionPRDispatch is the one step of version-pr-ci, the only job holding
// actions write next to version-pr: it dispatches ci.yaml on the version PR
// branch, with a bounded retry, and runs nothing else.
const versionPRDispatch = `for _ in 1 2 3; do
  gh workflow run ci.yaml --ref changeset-release/main && exit 0
  sleep 15
done
exit 1`

// pinPRPermissions: pin-actions pushes the pin branch and opens its PR,
// nothing else (#213).
var pinPRPermissions = map[string]string{"contents": "write", "pull-requests": "write"}

// pinPRDispatch is the one step of pin-actions-ci: it dispatches ci.yaml on
// the pin PR branch, like versionPRDispatch.
const pinPRDispatch = `for _ in 1 2 3; do
  gh workflow run ci.yaml --ref release-pins/main && exit 0
  sleep 15
done
exit 1`

// pinExamplesDispatch is the one step of pin-examples-ci: it dispatches
// ci.yaml on the examples' pin PR branch (#261), like versionPRDispatch.
const pinExamplesDispatch = `for _ in 1 2 3; do
  gh workflow run ci.yaml --ref release-pins/examples && exit 0
  sleep 15
done
exit 1`

// releaseWorkflowProblems: release.yaml's auto-tag job tags and dispatches
// only after ci passed on a push to main, holds only contents and actions
// write, and runs no code from its checkout; version-pr and release keep
// their push-only and tag-only conditions, version-pr holds only
// versionPRPermissions, and version-pr-ci holds only actions write, needs
// version-pr and runs only versionPRDispatch, with no action. pin-actions
// runs after release only, holds only pinPRPermissions, and pin-actions-ci
// is to it what version-pr-ci is to version-pr, with pinPRDispatch.
// pin-examples runs on push to main only, holds only pinPRPermissions, and
// pin-examples-ci dispatches pinExamplesDispatch after it.
func releaseWorkflowProblems(data []byte) ([]string, error) {
	var wf struct {
		Jobs map[string]permJob `yaml:"jobs"`
	}
	var needs workflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, &needs); err != nil {
		return nil, err
	}
	var problems []string
	job := wf.Jobs["auto-tag"]
	for _, g := range autoTagGuards {
		if !strings.Contains(job.If, g) {
			problems = append(problems, "auto-tag: if lacks "+g)
		}
	}
	if !maps.Equal(job.Permissions, map[string]string{"contents": "write", "actions": "write"}) {
		problems = append(problems, "auto-tag: permissions must be exactly contents and actions write")
	}
	for _, s := range job.Steps {
		if s.Uses != "" && (!strings.HasPrefix(s.Uses, "actions/checkout@") || s.With["ref"] != "${{ github.event.workflow_run.head_sha }}") {
			problems = append(problems, "auto-tag: uses "+s.Uses)
		}
		for _, c := range []string{"make", "go ", "./", "bin/", "npm", "source ", "bash "} {
			if strings.Contains(s.Run, c) {
				problems = append(problems, "auto-tag: runs code from the checkout: "+c)
			}
		}
	}
	if c := wf.Jobs["version-pr"].If; c != "github.event_name == 'push' && github.ref == 'refs/heads/main'" {
		problems = append(problems, "version-pr runs on "+c)
	}
	if !maps.Equal(wf.Jobs["version-pr"].Permissions, versionPRPermissions) {
		problems = append(problems, "version-pr: permissions must be exactly contents and pull-requests write")
	}
	problems = append(problems, dispatchJobProblems("version-pr-ci", wf.Jobs["version-pr-ci"], needs.Jobs["version-pr-ci"].Needs, "version-pr", versionPRDispatch)...)
	if c := wf.Jobs["release"].If; c != "startsWith(github.ref, 'refs/tags/v')" {
		problems = append(problems, "release runs on "+c)
	}
	problems = append(problems, pinActionsProblems(wf.Jobs["pin-actions"], needs.Jobs["pin-actions"].Needs)...)
	problems = append(problems, dispatchJobProblems("pin-actions-ci", wf.Jobs["pin-actions-ci"], needs.Jobs["pin-actions-ci"].Needs, "pin-actions", pinPRDispatch)...)
	problems = append(problems, pinExamplesProblems(wf.Jobs["pin-examples"])...)
	problems = append(problems, dispatchJobProblems("pin-examples-ci", wf.Jobs["pin-examples-ci"], needs.Jobs["pin-examples-ci"].Needs, "pin-examples", pinExamplesDispatch)...)
	return problems, nil
}

// pinActionsProblems: pin-actions runs on a tag after release only and
// holds only pinPRPermissions.
func pinActionsProblems(job permJob, needs []string) []string {
	var problems []string
	if job.If != "startsWith(github.ref, 'refs/tags/v')" {
		problems = append(problems, "pin-actions runs on "+job.If)
	}
	if len(needs) != 1 || needs[0] != "release" {
		problems = append(problems, "pin-actions must need release only")
	}
	if !maps.Equal(job.Permissions, pinPRPermissions) {
		problems = append(problems, "pin-actions: permissions must be exactly contents and pull-requests write")
	}
	return problems
}

// pinExamplesProblems: pin-examples runs on push to main only and holds
// only pinPRPermissions.
func pinExamplesProblems(job permJob) []string {
	var problems []string
	if job.If != "github.event_name == 'push' && github.ref == 'refs/heads/main'" {
		problems = append(problems, "pin-examples runs on "+job.If)
	}
	if !maps.Equal(job.Permissions, pinPRPermissions) {
		problems = append(problems, "pin-examples: permissions must be exactly contents and pull-requests write")
	}
	return problems
}

// dispatchJobProblems: a job that dispatches ci.yaml holds only actions
// write, needs one job only and runs only the dispatch script, with no action.
func dispatchJobProblems(name string, ci permJob, needs []string, after, script string) []string {
	var problems []string
	if !maps.Equal(ci.Permissions, map[string]string{"actions": "write"}) {
		problems = append(problems, name+": permissions must be exactly actions write")
	}
	if len(needs) != 1 || needs[0] != after {
		problems = append(problems, name+" must need "+after+" only")
	}
	if len(ci.Steps) != 1 || ci.Steps[0].Uses != "" || strings.TrimSpace(ci.Steps[0].Run) != script {
		problems = append(problems, name+": one step, no action, run exactly: "+script)
	}
	return problems
}

func TestReleaseAutoTagOnlyAfterCIOnMain(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := releaseWorkflowProblems(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p {
		t.Error(s)
	}
}

func TestReleaseWorkflowCheckCatchesLooseAutoTag(t *testing.T) {
	ok := "jobs:\n" +
		"  version-pr:\n    if: github.event_name == 'push' && github.ref == 'refs/heads/main'\n" +
		"    permissions: {contents: write, pull-requests: write}\n" +
		"  version-pr-ci:\n    needs: version-pr\n    permissions: {actions: write}\n" +
		"    steps:\n      - run: " + strconv.Quote(versionPRDispatch) + "\n" +
		"  release:\n    if: startsWith(github.ref, 'refs/tags/v')\n" +
		"  pin-actions:\n    needs: release\n    if: startsWith(github.ref, 'refs/tags/v')\n" +
		"    permissions: {contents: write, pull-requests: write}\n" +
		"  pin-actions-ci:\n    needs: pin-actions\n    permissions: {actions: write}\n" +
		"    steps:\n      - run: " + strconv.Quote(pinPRDispatch) + "\n" +
		"  pin-examples:\n    if: github.event_name == 'push' && github.ref == 'refs/heads/main'\n" +
		"    permissions: {contents: write, pull-requests: write}\n" +
		"  pin-examples-ci:\n    needs: pin-examples\n    permissions: {actions: write}\n" +
		"    steps:\n      - run: " + strconv.Quote(pinExamplesDispatch) + "\n" +
		"  auto-tag:\n    if: >-\n      " + strings.Join(autoTagGuards, " &&\n      ") + "\n" +
		"    permissions: {contents: write, actions: write}\n" +
		"    steps:\n" +
		"      - uses: actions/checkout@x\n        with: {ref: '${{ github.event.workflow_run.head_sha }}'}\n" +
		"      - run: gh workflow run release.yaml\n"
	if p, err := releaseWorkflowProblems([]byte(ok)); err != nil || len(p) != 0 {
		t.Fatalf("valid workflow reported %v (%v)", p, err)
	}
	cases := map[string][2]string{
		"no push guard":      {"github.event.workflow_run.event == 'push' &&", ""},
		"no fork guard":      {"&&\n      github.event.workflow_run.head_repository.full_name == github.repository", ""},
		"extra scope":        {"actions: write}", "actions: write, id-token: write}"},
		"runs make":          {"gh workflow run release.yaml", "make build"},
		"third-party action": {"actions/checkout@x", "evil/action@x"},
		"release on push":    {"startsWith(github.ref, 'refs/tags/v')", "github.event_name == 'push'"},
		"version-pr signs":   {"pull-requests: write}", "pull-requests: write, id-token: write}"},
		"version-pr actions": {"pull-requests: write}", "pull-requests: write, actions: write}"},
		"ci job contents":    {"permissions: {actions: write}\n    steps", "permissions: {actions: write, contents: write}\n    steps"},
		"ci job other ref":   {"--ref changeset-release/main", "--ref main"},
		"ci job release":     {"run ci.yaml", "run release.yaml"},
		"ci job extra step":  {"    steps:\n      - run: \"for", "    steps:\n      - uses: actions/checkout@x\n      - run: \"for"},
		"ci job no needs":    {"    needs: version-pr\n", ""},
		"pin on push":        {"needs: release\n    if: startsWith(github.ref, 'refs/tags/v')", "needs: release\n    if: github.event_name == 'push'"},
		"pin before release": {"needs: release\n", "needs: version-pr\n"},
		"pin signs":          {"contents: write, pull-requests: write}\n  pin", "contents: write, pull-requests: write, id-token: write}\n  pin"},
		"pin ci other ref":   {"--ref release-pins/main", "--ref main"},
		"pin ci contents":    {"needs: pin-actions\n    permissions: {actions: write}", "needs: pin-actions\n    permissions: {actions: write, contents: write}"},
		"examples on tag":    {"if: github.event_name == 'push' && github.ref == 'refs/heads/main'\n    permissions: {contents: write, pull-requests: write}\n  pin-examples-ci", "if: startsWith(github.ref, 'refs/tags/v')\n    permissions: {contents: write, pull-requests: write}\n  pin-examples-ci"},
		"examples signs":     {"pull-requests: write}\n  pin-examples-ci", "pull-requests: write, id-token: write}\n  pin-examples-ci"},
		"examples ci ref":    {"--ref release-pins/examples", "--ref main"},
		"examples ci needs":  {"needs: pin-examples\n", "needs: pin-actions\n"},
	}
	for name, c := range cases {
		wf := strings.Replace(ok, c[0], c[1], 1)
		if wf == ok {
			t.Fatalf("%s: case changes nothing", name)
		}
		if p, err := releaseWorkflowProblems([]byte(wf)); err != nil || len(p) == 0 {
			t.Errorf("%s: no problem reported (%v)", name, err)
		}
	}
}
