package architecture

import (
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/contract"
	"gopkg.in/yaml.v3"
)

// exampleDir is the monorepo example of #217; baselineDir holds the
// files the integration edits as they were before it.
const (
	exampleDir  = "examples/github-monorepo/"
	baselineDir = "tests/architecture/testdata/github-monorepo/"
)

// integrationAdded and integrationEdited are the only files integrating Aval
// may add or edit (docs/guides/integrate-in-5-files.md); the edits to
// ci.yml and .gitignore are required, CONTRIBUTING.md is optional.
var (
	integrationAdded  = []string{"aval.yaml", ".github/aval/policy.yaml"}
	integrationEdited = map[string]bool{".github/workflows/ci.yml": true, ".gitignore": true, "CONTRIBUTING.md": false}
	mentionsAval      = regexp.MustCompile(`(?i)aval`)
)

// integrationProblems checks the example (path relative to the example
// root to content) against the baseline of the edited files: at most 5
// files added or edited, exactly the allowed ones, and no other file that
// mentions Aval, so the Makefile and scripts are as before, with no wrapper
// script under .github/aval and no Aval wiring test.
func integrationProblems(files, baseline map[string]string) []string {
	var problems []string
	changed := 0
	for _, f := range integrationAdded {
		if _, ok := files[f]; !ok {
			problems = append(problems, f+": the integration adds it, but it is missing")
		}
		changed++
	}
	for f, required := range integrationEdited {
		old, ok := baseline[f]
		switch {
		case !ok:
			problems = append(problems, f+": no baseline copy under "+baselineDir)
		case files[f] != old:
			changed++
		case required:
			problems = append(problems, f+": the integration must edit it")
		}
	}
	if changed > 5 {
		problems = append(problems, "the integration touches more than 5 files")
	}
	for _, f := range slices.Sorted(maps.Keys(files)) {
		if slices.Contains(integrationAdded, f) {
			continue
		}
		if _, ok := integrationEdited[f]; ok {
			continue
		}
		if mentionsAval.MatchString(f + "\n" + files[f]) {
			problems = append(problems, f+": mentions Aval, but only the integration files may")
		}
	}
	return problems
}

// matrixTests expands ${{ matrix.<var> }} in a run action's test input over
// the job's matrix (one variable).
func matrixTests(test string, matrix map[string][]string) []string {
	for v, values := range matrix {
		expr := "${{ matrix." + v + " }}"
		if strings.Contains(test, expr) {
			var out []string
			for _, x := range values {
				out = append(out, strings.ReplaceAll(test, expr, x))
			}
			return out
		}
	}
	return []string{test}
}

// workflowTestProblems checks that the workflow runs every test of the
// contract through the run action exactly once (one artifact per test), and
// that the external job is a plain make job.
func workflowTestProblems(wf []byte, tests []string) ([]string, error) {
	var w struct {
		Jobs map[string]struct {
			Strategy struct {
				Matrix map[string][]string `yaml:"matrix"`
			} `yaml:"strategy"`
			Steps []step `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(wf, &w); err != nil {
		return nil, err
	}
	var problems, got []string
	for name, j := range w.Jobs {
		for _, s := range j.Steps {
			if strings.HasPrefix(s.Uses, runActionRef) {
				got = append(got, matrixTests(s.With["test"], j.Strategy.Matrix)...)
			}
			if name == "external" && strings.Contains(s.Uses, "TrianaLab/aval") {
				problems = append(problems, "external: runs through Aval; it is an external-dependencies suite")
			}
		}
	}
	slices.Sort(got)
	tests = slices.Sorted(slices.Values(tests))
	if !slices.Equal(got, tests) {
		problems = append(problems, "the run action runs "+strings.Join(got, ", ")+"; the contract has "+strings.Join(tests, ", "))
	}
	return problems, nil
}

// The example of #217 integrates Aval in at most 5 files: 2 added, ci.yml
// and .gitignore edited, CONTRIBUTING.md optionally; nothing else.
func TestGitHubMonorepoIntegrationFiles(t *testing.T) {
	files, baseline := map[string]string{}, map[string]string{}
	for _, f := range trackedFiles(t, exampleDir) {
		files[strings.TrimPrefix(f, exampleDir)] = readFile(t, f)
	}
	for _, f := range trackedFiles(t, baselineDir) {
		baseline[strings.TrimPrefix(f, baselineDir)] = readFile(t, f)
	}
	if len(files) == 0 {
		t.Fatal("no tracked files under " + exampleDir)
	}
	for _, p := range integrationProblems(files, baseline) {
		t.Error(p)
	}
	if !slices.Contains(strings.Split(files[".gitignore"], "\n"), ".aval/") {
		t.Error(".gitignore: must ignore .aval/")
	}

	wf := []byte(files[".github/workflows/ci.yml"])
	p, err := actionExampleProblems(wf, []byte(readFile(t, "integrations/github/run/action.yml")))
	if err != nil {
		t.Fatal(err)
	}
	q, err := publishExampleProblems(wf, []byte(readFile(t, "integrations/github/publish/action.yml")))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := contract.LoadAll(filepath.Join(root, exampleDir, "aval.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range cs {
		names = append(names, c.Metadata.Name)
	}
	r, err := workflowTestProblems(wf, names)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range slices.Concat(p, q, r) {
		t.Error("ci.yml: " + s)
	}
}

func TestIntegrationCheckCatchesDrift(t *testing.T) {
	base := map[string]string{".github/workflows/ci.yml": "a", ".gitignore": "b", "CONTRIBUTING.md": "c"}
	files := map[string]string{
		"aval.yaml": "x", ".github/workflows/ci.yml": "a", ".gitignore": "b2", "CONTRIBUTING.md": "c",
		"Makefile": "test:\n\taval run --test unit\n", ".github/aval/run.sh": "#!/bin/sh\n",
	}
	got := integrationProblems(files, base)
	want := []string{
		".github/aval/policy.yaml: the integration adds it, but it is missing",
		".github/workflows/ci.yml: the integration must edit it",
		".github/aval/run.sh: mentions Aval, but only the integration files may",
		"Makefile: mentions Aval, but only the integration files may",
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("missing %q in %q", w, got)
		}
	}

	wf := "jobs:\n  e2e:\n    strategy: {matrix: {shard: [1, 2]}}\n    steps:\n      - uses: " + runActionRef + "x\n        with: {test: \"e2e-${{ matrix.shard }}\"}\n" +
		"  external:\n    steps:\n      - uses: " + runActionRef + "x\n        with: {test: ext}\n"
	got, err := workflowTestProblems([]byte(wf), []string{"e2e-1", "e2e-2", "e2e-3"})
	if err != nil {
		t.Fatal(err)
	}
	want = []string{
		"external: runs through Aval; it is an external-dependencies suite",
		"the run action runs e2e-1, e2e-2, ext; the contract has e2e-1, e2e-2, e2e-3",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
