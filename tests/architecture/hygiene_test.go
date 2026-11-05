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

const titleJob = "pr-title"

// titlePattern returns the regex the pr-title job matches the PR title
// against, so the Go cases below test the exact pattern CI runs.
func titlePattern(t *testing.T) *regexp.Regexp {
	t.Helper()
	var wf workflow
	if err := yaml.Unmarshal(readCI(t), &wf); err != nil {
		t.Fatal(err)
	}
	job, ok := wf.Jobs[titleJob]
	if !ok {
		t.Fatalf("ci.yaml has no %s job", titleJob)
	}
	m := regexp.MustCompile(`(?m)^pattern='([^']+)'$`)
	for _, s := range job.Steps {
		if p := m.FindStringSubmatch(s.Run); p != nil {
			return regexp.MustCompile(p[1])
		}
	}
	t.Fatalf("%s job has no `pattern='...'` line", titleJob)
	return nil
}

// The title gate must run on every pull request and on title edits, and sit
// behind the required aggregator (TestEveryCIJobIsAggregated) so it blocks
// merges.
func TestPRTitleGateRunsOnTitleEdits(t *testing.T) {
	var wf struct {
		On struct {
			PullRequest struct {
				Types []string `yaml:"types"`
			} `yaml:"pull_request"`
		} `yaml:"on"`
	}
	if err := yaml.Unmarshal(readCI(t), &wf); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"opened", "edited", "synchronize", "reopened"} {
		if !slices.Contains(wf.On.PullRequest.Types, typ) {
			t.Errorf("ci.yaml pull_request.types lacks %q", typ)
		}
	}
	titlePattern(t)
}

func TestPRTitlePattern(t *testing.T) {
	re := titlePattern(t)
	for _, title := range []string{
		"feat: add verify",
		"fix(fingerprint): hash symlink targets",
		"chore(deps): bump github.com/spf13/cobra from 1.10.1 to 1.10.2",
		"feat!: new predicate version",
		"ci: docs lint (markdownlint, vale)",
		"docs: x", "test: x", "refactor: x", "perf: x", "build: x", "style: x",
	} {
		if !re.MatchString(title) {
			t.Errorf("valid title rejected: %q", title)
		}
	}
	for _, title := range []string{
		"Add verify",
		"feat add verify",
		"feat:add verify",
		"feat: ",
		"Feat: add verify",
		"feature: add verify",
		"wip: add verify",
		"feat(): add verify",
		" feat: add verify",
	} {
		if re.MatchString(title) {
			t.Errorf("invalid title accepted: %q", title)
		}
	}
}

// Contribution files GitHub surfaces to contributors.
func TestContributionTemplatesExist(t *testing.T) {
	for _, f := range []string{
		"CODE_OF_CONDUCT.md",
		".github/pull_request_template.md",
		".github/ISSUE_TEMPLATE/bug_report.yml",
		".github/ISSUE_TEMPLATE/feature_request.yml",
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", f))
		if err != nil {
			t.Error(err)
			continue
		}
		if strings.TrimSpace(string(data)) == "" {
			t.Errorf("%s is empty", f)
		}
		if strings.HasSuffix(f, ".yml") {
			var form struct {
				Name string           `yaml:"name"`
				Body []map[string]any `yaml:"body"`
			}
			if err := yaml.Unmarshal(data, &form); err != nil || form.Name == "" || len(form.Body) == 0 {
				t.Errorf("%s is not an issue form (name and body required): %v", f, err)
			}
		}
	}
}
