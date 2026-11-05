package architecture

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const root = "../.."

// ponytail: this file names the forbidden words it searches for, so it skips
// itself in the text scans below.
const self = "tests/architecture/invariants_test.go"

// evidenceAllowedModules lists the non-standard-library modules pkg/evidence
// may depend on, directly or transitively (AGENTS.md invariant 11). Adding one
// is a public API decision.
var evidenceAllowedModules = []string{}

// trackedFiles returns the git-tracked files matching the pathspecs.
func trackedFiles(t *testing.T, pathspecs ...string) []string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"ls-files", "--"}, pathspecs...)...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	return strings.Fields(string(out))
}

func readFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// evidenceDepProblems reports packages pkg/evidence must not depend on, given
// `go list -deps` lines of "<import path> <standard>".
func evidenceDepProblems(lines []string) []string {
	var problems []string
	for _, l := range lines {
		path, std, _ := strings.Cut(l, " ")
		switch {
		case std == "true", strings.HasPrefix(path, "github.com/TrianaLab/aval/pkg/evidence"):
		case strings.HasPrefix(path, "github.com/TrianaLab/aval/"):
			problems = append(problems, path+": pkg/evidence must not import other Aval packages (internal/ included)")
		case !allowedModule(path):
			problems = append(problems, path+": not in evidenceAllowedModules")
		}
	}
	return problems
}

func allowedModule(path string) bool {
	for _, m := range evidenceAllowedModules {
		if path == m || strings.HasPrefix(path, m+"/") {
			return true
		}
	}
	return false
}

func TestEvidenceDependsOnlyOnAllowlistedModules(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}} {{.Standard}}", "./pkg/evidence/...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, p := range evidenceDepProblems(strings.Split(strings.TrimSpace(string(out)), "\n")) {
		t.Error(p)
	}
}

func TestEvidenceDepCheckCatchesForbiddenImports(t *testing.T) {
	bad := []string{
		"github.com/TrianaLab/aval/internal/contract false",
		"github.com/TrianaLab/aval/cmd/aval false",
		"gopkg.in/yaml.v3 false",
	}
	for _, l := range bad {
		if len(evidenceDepProblems([]string{l})) != 1 {
			t.Errorf("%q not reported", l)
		}
	}
	ok := []string{"fmt true", "github.com/TrianaLab/aval/pkg/evidence false"}
	if p := evidenceDepProblems(ok); len(p) != 0 {
		t.Errorf("valid deps reported %v", p)
	}
}

// Invariant 9: Aval never imports or models a consumer.
var consumerRe = regexp.MustCompile(`(?i)pacto`)

func TestNoGoCodeNamesPacto(t *testing.T) {
	for _, f := range trackedFiles(t, "*.go", "*go.mod") {
		if f == self {
			continue
		}
		if consumerRe.MatchString(readFile(t, f)) {
			t.Errorf("%s names Pacto (invariant 9: no consumer coupling)", f)
		}
	}
}

// AGENTS.md "Work tracking": every TODO names a roadmap milestone. Markdown is
// prose about the convention and is not scanned; .claude/skills holds symlinks.
var todoRe = regexp.MustCompile(`TODO(\(milestone-[0-9]+\))?`)

func untaggedTODOs(text string) int {
	n := 0
	for _, m := range todoRe.FindAllStringSubmatch(text, -1) {
		if m[1] == "" {
			n++
		}
	}
	return n
}

func TestEveryTODONamesAMilestone(t *testing.T) {
	for _, f := range trackedFiles(t, ".", ":!*.md", ":!.claude/skills") {
		if f == self {
			continue
		}
		if n := untaggedTODOs(readFile(t, f)); n > 0 {
			t.Errorf("%s: %d TODO(s) without (milestone-N)", f, n)
		}
	}
}

func TestTODOCheckCatchesUntagged(t *testing.T) {
	for text, want := range map[string]int{
		"// TODO: later":                       1,
		"// TODO(milestone-2): x":              0,
		"// TODO(milestone-N): x":              1,
		"// TODO(alice) and TODO(milestone-3)": 1,
	} {
		if got := untaggedTODOs(text); got != want {
			t.Errorf("%q: got %d untagged, want %d", text, got, want)
		}
	}
}

var semver = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// A floating linter version lets the same tree pass one day and fail the next,
// and a local version that differs from CI's makes `make ci` lie.
func TestGolangciLintVersionIsPinnedOnce(t *testing.T) {
	m := regexp.MustCompile(`(?m)^GOLANGCI_LINT_VERSION := (\S+)$`).FindStringSubmatch(readFile(t, "Makefile"))
	if m == nil {
		t.Fatal("Makefile has no `GOLANGCI_LINT_VERSION := vX.Y.Z`")
	}
	want := m[1]
	if !semver.MatchString(want) {
		t.Errorf("Makefile pins golangci-lint %q, want a full vX.Y.Z", want)
	}
	var wf workflow
	if err := yaml.Unmarshal(readCI(t), &wf); err != nil {
		t.Fatal(err)
	}
	installers := 0
	for name, job := range wf.Jobs {
		for _, s := range job.Steps {
			if !strings.HasPrefix(s.Uses, "golangci/golangci-lint-action@") {
				continue
			}
			installers++
			if s.With["version"] != want {
				t.Errorf("CI job %s installs golangci-lint %q, Makefile pins %q", name, s.With["version"], want)
			}
		}
	}
	if installers == 0 {
		t.Error("no CI step installs golangci-lint; this gate watches nothing")
	}
}

// Cyclomatic complexity stays at 15 or below, enforced by golangci-lint.
func TestCyclomaticComplexityIsCapped(t *testing.T) {
	var cfg struct {
		Linters struct {
			Enable   []string `yaml:"enable"`
			Settings struct {
				Gocyclo struct {
					MinComplexity int `yaml:"min-complexity"`
				} `yaml:"gocyclo"`
			} `yaml:"settings"`
		} `yaml:"linters"`
	}
	if err := yaml.Unmarshal([]byte(readFile(t, ".golangci.yml")), &cfg); err != nil {
		t.Fatal(err)
	}
	enabled := false
	for _, l := range cfg.Linters.Enable {
		enabled = enabled || l == "gocyclo"
	}
	if !enabled {
		t.Error(".golangci.yml must enable gocyclo")
	}
	// gocyclo reports functions whose complexity is above min-complexity.
	if c := cfg.Linters.Settings.Gocyclo.MinComplexity; c < 1 || c > 15 {
		t.Errorf("gocyclo min-complexity is %d, want 1..15", c)
	}
}
