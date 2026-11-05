package architecture

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const aggregator = "required"

type workflow struct {
	Jobs map[string]struct {
		If    string    `yaml:"if"`
		Needs yamlNeeds `yaml:"needs"`
		Steps []step    `yaml:"steps"`
	} `yaml:"jobs"`
}

type step struct {
	Run string `yaml:"run"`
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
			return strings.TrimSpace(s.Run) == "make "+leg
		}) {
			t.Errorf("CI job %s must run `make %s`", leg, leg)
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
