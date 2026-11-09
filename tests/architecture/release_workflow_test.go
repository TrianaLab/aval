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

// releaseWorkflowProblems: release.yaml's auto-tag job tags and dispatches
// only after ci passed on a push to main, holds only contents and actions
// write, and runs no code from its checkout; version-pr and release keep
// their push-only and tag-only conditions, version-pr holds only
// versionPRPermissions, and version-pr-ci holds only actions write, needs
// version-pr and runs only versionPRDispatch, with no action.
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
	problems = append(problems, versionPRCIProblems(wf.Jobs["version-pr-ci"], needs.Jobs["version-pr-ci"].Needs)...)
	if c := wf.Jobs["release"].If; c != "startsWith(github.ref, 'refs/tags/v')" {
		problems = append(problems, "release runs on "+c)
	}
	return problems, nil
}

// versionPRCIProblems: version-pr-ci holds only actions write, needs
// version-pr only and runs only versionPRDispatch, with no action.
func versionPRCIProblems(ci permJob, needs []string) []string {
	var problems []string
	if !maps.Equal(ci.Permissions, map[string]string{"actions": "write"}) {
		problems = append(problems, "version-pr-ci: permissions must be exactly actions write")
	}
	if len(needs) != 1 || needs[0] != "version-pr" {
		problems = append(problems, "version-pr-ci must need version-pr only")
	}
	if len(ci.Steps) != 1 || ci.Steps[0].Uses != "" || strings.TrimSpace(ci.Steps[0].Run) != versionPRDispatch {
		problems = append(problems, "version-pr-ci: one step, no action, run exactly: "+versionPRDispatch)
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
