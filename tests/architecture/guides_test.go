package architecture

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The task guides (#75): short, visual pages whose code cannot drift.
var guides = []string{
	"github-actions", "signing", "write-a-contract", "trust-policy",
	"go-consumer", "measure-reuse", "troubleshooting",
}

// guideMaxLines is the maintainer's "under ~100 lines", with room for the
// troubleshooting tables; details go in collapsible blocks.
const guideMaxLines = 120

var (
	fenceRe   = regexp.MustCompile("(?ms)^( *)```(\\w*)\\n(.*?)^ *```")
	avalCmdRe = regexp.MustCompile(`(?m)^\s*aval ([a-z]+)\b(.*)$`)
	flagRe    = regexp.MustCompile(`(?:^|\s)(--[a-z][a-z-]*)`)
	cliSecRe  = regexp.MustCompile(`(?m)^## aval ([a-z]+)$`)
	rowRe     = regexp.MustCompile("(?m)^ *\\| (`[^|]*`) \\|")
	spanRe    = regexp.MustCompile("`([^`]+)`")
	holeRe    = regexp.MustCompile(`<[^>]*>`)
	exitRe    = regexp.MustCompile(`(?m)^\s+Exit\w+\s+= (\d+)`)
)

// guideProblems checks one guide: a one-sentence goal, a diagram, a "Read
// next" section, YAML, Go and JSON only through snippets of tested files,
// and only aval subcommands and flags that the CLI reference lists.
func guideProblems(name, md, cliRef string) []string {
	var probs []string
	add := func(f string, a ...any) { probs = append(probs, name+": "+fmt.Sprintf(f, a...)) }
	if n := strings.Count(md, "\n"); n > guideMaxLines {
		add("%d lines, the limit is %d", n, guideMaxLines)
	}
	if lines := strings.SplitN(md, "\n", 4); len(lines) < 3 || !strings.HasPrefix(lines[0], "# ") || !strings.HasPrefix(lines[2], "Goal: ") {
		add(`must open with the title, then a "Goal: " sentence`)
	}
	if !strings.Contains(md, "```mermaid\n") {
		add("needs a diagram")
	}
	if !strings.Contains(md, "\n## Read next\n") {
		add(`must end with "## Read next"`)
	}
	sections := cliSections(cliRef)
	for _, f := range fenceRe.FindAllStringSubmatch(md, -1) {
		lang, body := f[2], strings.TrimSpace(f[3])
		switch lang {
		case "yaml", "go", "json":
			if strings.Contains(body, "\n") || !strings.HasPrefix(body, "--8<-- ") {
				add("a %s block must be one --8<-- include of a tested file", lang)
			}
		case "sh", "console":
			for _, p := range commandProblems(body, sections) {
				add("%s", p)
			}
		}
	}
	return probs
}

// cliSections maps each subcommand to its section of the CLI reference.
func cliSections(cliRef string) map[string]string {
	sections := map[string]string{}
	idx := cliSecRe.FindAllStringSubmatchIndex(cliRef, -1)
	for i, m := range idx {
		end := len(cliRef)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		sections[cliRef[m[2]:m[3]]] = cliRef[m[1]:end]
	}
	return sections
}

// commandProblems lists the aval subcommands and flags in a shell block that
// the CLI reference does not have.
func commandProblems(body string, sections map[string]string) []string {
	var probs []string
	for _, c := range avalCmdRe.FindAllStringSubmatch(body, -1) {
		sec, ok := sections[c[1]]
		if !ok {
			probs = append(probs, fmt.Sprintf("aval %s: no such command in docs/cli-reference.md", c[1]))
			continue
		}
		for _, fl := range flagRe.FindAllStringSubmatch(c[2], -1) {
			if !strings.Contains(sec, fl[1]+" ") {
				probs = append(probs, fmt.Sprintf("aval %s %s: no such flag in docs/cli-reference.md", c[1], fl[1]))
			}
		}
	}
	return probs
}

// troubleshootingProblems checks that every exit code has a row and that
// every message quoted in a table's first column is in the Go source, so a
// reworded message breaks the page here instead of silently. <...> stands
// for the parts that change per run.
func troubleshootingProblems(md, src string, codes []string) []string {
	var probs []string
	for _, c := range codes {
		if !strings.Contains(md, "\n| "+c+" | ") {
			probs = append(probs, "troubleshooting: no row for exit code "+c)
		}
	}
	for _, row := range rowRe.FindAllStringSubmatch(md, -1) {
		for _, s := range spanRe.FindAllStringSubmatch(row[1], -1) {
			for _, piece := range holeRe.Split(s[1], -1) {
				if strings.TrimSpace(piece) != "" && !strings.Contains(src, piece) {
					probs = append(probs, fmt.Sprintf("troubleshooting: %q is not in the Go source", s[1]))
					break
				}
			}
		}
	}
	return probs
}

func TestGuides(t *testing.T) {
	cliRef := readFile(t, "docs/cli-reference.md")
	for _, g := range guides {
		for _, p := range guideProblems(g, readFile(t, "docs/guides/"+g+".md"), cliRef) {
			t.Error(p)
		}
	}
	var src strings.Builder
	for _, f := range trackedFiles(t, "cmd/*.go", "internal/*.go", "pkg/*.go", ":!*_test.go") {
		src.WriteString(readFile(t, f))
	}
	var codes []string
	for _, m := range exitRe.FindAllStringSubmatch(readFile(t, "cmd/aval/main.go"), -1) {
		codes = append(codes, m[1])
	}
	if len(codes) < 7 {
		t.Fatalf("found exit codes %v in cmd/aval/main.go, want 0 to 6", codes)
	}
	for _, p := range troubleshootingProblems(readFile(t, "docs/guides/troubleshooting.md"), src.String(), codes) {
		t.Error(p)
	}
}

func TestGuideChecksCatchDrift(t *testing.T) {
	cliRef := "## aval run\n      --push   push\n## aval verify\n      --policy x\n"
	bad := "# T\n\nNo goal.\n\n```yaml\nkind: Test\n```\n\n```sh\naval run --push --pull\naval stats\naval verify --policy p\n```\n"
	got := guideProblems("g", bad, cliRef)
	for _, want := range []string{
		`g: must open with the title, then a "Goal: " sentence`,
		"g: needs a diagram",
		`g: must end with "## Read next"`,
		"g: a yaml block must be one --8<-- include of a tested file",
		"g: aval run --pull: no such flag in docs/cli-reference.md",
		"g: aval stats: no such command in docs/cli-reference.md",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if len(got) != 6 {
		t.Errorf("got %d problems, want 6: %q", len(got), got)
	}
	ok := "# T\n\nGoal: x.\n\n```mermaid\nflowchart LR\n```\n\n```yaml\n--8<-- \"a.yaml\"\n```\n\n## Read next\n"
	if p := guideProblems("g", ok+strings.Repeat("\n", guideMaxLines), cliRef); len(p) != 1 {
		t.Errorf("a long guide: got %q, want only the line limit", p)
	}

	md := "\n| 0 | ok | - |\n    | `bad flag <x> here`, `--push requires` | fix |\n"
	got = troubleshootingProblems(md, `errors.New("--push requires --registry"); fmt.Errorf("bad flag %s there")`, []string{"0", "4"})
	want := []string{"troubleshooting: no row for exit code 4", `troubleshooting: "bad flag <x> here" is not in the Go source`}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
