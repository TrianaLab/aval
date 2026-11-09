package architecture

import (
	"maps"
	"os"
	"path/filepath"
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

// versionPRPermissions: version-pr writes the version PR and dispatches
// ci.yaml on it, nothing else (no packages or id-token write).
var versionPRPermissions = map[string]string{"contents": "write", "pull-requests": "write", "actions": "write"}

// releaseWorkflowProblems: release.yaml's auto-tag job tags and dispatches
// only after ci passed on a push to main, holds only contents and actions
// write, and runs no code from its checkout; version-pr and release keep
// their push-only and tag-only conditions, and version-pr holds only
// versionPRPermissions.
func releaseWorkflowProblems(data []byte) ([]string, error) {
	var wf struct {
		Jobs map[string]permJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &wf); err != nil {
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
		problems = append(problems, "version-pr: permissions must be exactly contents, pull-requests and actions write")
	}
	if c := wf.Jobs["release"].If; c != "startsWith(github.ref, 'refs/tags/v')" {
		problems = append(problems, "release runs on "+c)
	}
	return problems, nil
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
		"    permissions: {contents: write, pull-requests: write, actions: write}\n" +
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
		"version-pr signs":   {"pull-requests: write, actions: write}", "pull-requests: write, actions: write, id-token: write}"},
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
