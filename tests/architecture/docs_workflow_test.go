package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// docs.yaml: only `deploy` holds write scopes, never on pull_request, and a
// manual run (workflow_dispatch) gets them only on main, so a dispatch on
// another branch never deploys that branch's code.
func TestDocsDeployWriteScopesOnlyOnMain(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "docs.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Permissions map[string]string  `yaml:"permissions"`
		Jobs        map[string]permJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &wf); err != nil {
		t.Fatal(err)
	}
	hasWrite := func(p map[string]string) bool {
		for _, v := range p {
			if v == "write" {
				return true
			}
		}
		return false
	}
	if hasWrite(wf.Permissions) {
		t.Error("workflow-level write scopes")
	}
	for name, j := range wf.Jobs {
		if name != "deploy" && hasWrite(j.Permissions) {
			t.Errorf("%s: write scopes outside deploy", name)
		}
	}
	cond := wf.Jobs["deploy"].If
	if strings.Contains(cond, "pull_request") {
		t.Errorf("deploy runs on pull_request: %s", cond)
	}
	const dispatchOnMain = "(github.event_name == 'workflow_dispatch' && github.ref == 'refs/heads/main')"
	if strings.Count(cond, "workflow_dispatch") != strings.Count(cond, dispatchOnMain) {
		t.Errorf("deploy: a manual run must be limited to main: %s", cond)
	}
}
