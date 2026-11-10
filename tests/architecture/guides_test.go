package architecture

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The task guides (#75): short, visual pages whose code cannot drift.
var guides = []string{
	"github-actions", "signing", "write-a-contract", "trust-policy",
	"go-consumer", "measure-reuse", "troubleshooting", "depend-on-a-contract",
	"shards-and-matrices", "integrate-in-5-files",
}

// guideMaxLines is the maintainer's "under ~100 lines", with room for the
// troubleshooting tables; details go in collapsible blocks.
const guideMaxLines = 120

var (
	fenceRe   = regexp.MustCompile("(?ms)^( *)```(\\w*)[^\\n]*\\n(.*?)^ *```")
	yamlDocRe = regexp.MustCompile(`(?m)^\s*(apiVersion|kind|identities|minTrustLevel|scope|runner|cacheability):`)
	// msgRe finds the reasons a user reads for the common cases: contract
	// and policy validation, misses (skipped) and verification failures
	// (Reason, and the reasons evidence.Verify returns).
	msgRe     = regexp.MustCompile(`(?:\badd\(|skipped\(|Reason: |return [^,\n]+, )(?:fmt\.Sprintf\()?("(?:[^"\\]|\\.)*")( \+)?`)
	valRe     = regexp.MustCompile(`(?:errors\.New|fmt\.Errorf)\(("(?:[^"\\]|\\.)*")`)
	verbRe    = regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z]`)
	avalCmdRe = regexp.MustCompile(`(?m)^\s*aval ([a-z]+)\b(.*)$`)
	flagRe    = regexp.MustCompile(`(?:^|\s)(--[a-z][a-z-]*)`)
	cliSecRe  = regexp.MustCompile(`(?m)^## aval ([a-z]+)$`)
	rowRe     = regexp.MustCompile("(?m)^ *\\| (`[^|]*`) \\|")
	spanRe    = regexp.MustCompile("`([^`]+)`")
	holeRe    = regexp.MustCompile(`<[^>]*>`)
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
		body := strings.TrimSpace(f[3])
		lang := fenceLang(f[2], body)
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

// fenceLang normalises a fence's language so aliases and untagged blocks
// get the same checks: yml is yaml, shell aliases are sh, and an untagged
// block is yaml if it looks like aval.yaml or a policy, sh if it runs aval.
func fenceLang(lang, body string) string {
	switch lang {
	case "yml":
		return "yaml"
	case "bash", "shell", "zsh":
		return "sh"
	case "":
		if yamlDocRe.MatchString(body) {
			return "yaml"
		}
		if avalCmdRe.MatchString(body) {
			return "sh"
		}
	}
	return lang
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

// exitCodesInclude pulls in the one generated exit-code table.
const exitCodesInclude = `--8<-- "docs/cli-reference.md:exit-codes"`

// troubleshootingProblems checks that the page includes the exit-code table
// and that every message quoted in a table's first column is in the Go source, so a
// reworded message breaks the page here instead of silently. <...> stands
// for the parts that change per run.
func troubleshootingProblems(md, src string) []string {
	var probs []string
	if !strings.Contains(md, exitCodesInclude) {
		probs = append(probs, "troubleshooting: must include "+exitCodesInclude)
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

// messageFormats returns the format strings re captures in src. A literal
// followed by + (as in "signature: " + err.Error()) ends in a hole.
func messageFormats(src string, re *regexp.Regexp) []string {
	var formats []string
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		f, err := strconv.Unquote(m[1])
		if err != nil {
			continue
		}
		if len(m) > 2 && m[2] != "" {
			f += "%v"
		}
		formats = append(formats, f)
	}
	return formats
}

// uncoveredMessages lists the formats that no message in the first column
// of a troubleshooting table quotes: a span matches when, with its <...>
// holes and the format's verbs both read as one hole, it is a part of the
// format of at least 10 characters. Formats with almost no fixed text (such
// as "%s: %w") say nothing on their own and are skipped.
func uncoveredMessages(md string, formats []string) []string {
	var spans []string
	for _, row := range rowRe.FindAllStringSubmatch(md, -1) {
		for _, s := range spanRe.FindAllStringSubmatch(row[1], -1) {
			if n := holeRe.ReplaceAllString(s[1], "%"); len(n) >= 10 {
				spans = append(spans, n)
			}
		}
	}
	var missing []string
	for _, f := range formats {
		n := verbRe.ReplaceAllString(f, "%")
		if len(strings.Trim(strings.ReplaceAll(n, "%", ""), " :;,.[]()")) < 6 {
			continue
		}
		if !slices.ContainsFunc(spans, func(s string) bool { return strings.Contains(n, s) }) {
			missing = append(missing, fmt.Sprintf("troubleshooting: no row for %q", f))
		}
	}
	return missing
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
	ts := readFile(t, "docs/guides/troubleshooting.md")
	for _, p := range troubleshootingProblems(ts, src.String()) {
		t.Error(p)
	}
	var formats []string
	for _, f := range trackedFiles(t, "cmd/aval/*.go", "internal/registry/*.go", "pkg/evidence/evidence.go", "internal/contract/*.go", "internal/policy/*.go", ":!*_test.go") {
		formats = append(formats, messageFormats(readFile(t, f), msgRe)...)
		if strings.HasPrefix(f, "internal/contract/") || strings.HasPrefix(f, "internal/policy/") {
			formats = append(formats, messageFormats(readFile(t, f), valRe)...)
		}
	}
	for _, p := range uncoveredMessages(ts, formats) {
		t.Error(p)
	}
}

func TestGuideChecksCatchDrift(t *testing.T) {
	cliRef := "## aval run\n      --push   push\n## aval verify\n      --policy x\n"
	bad := "# T\n\nNo goal.\n\n```yaml\nkind: Test\n```\n\n```sh\naval run --push --pull\naval stats\naval verify --policy p\n```\n" +
		// aliases and untagged blocks get the same checks
		"```yml title=\"aval.yaml\"\nkind: Test\n```\n```\nidentities: []\n```\n```bash\naval stats\n```\n"
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
	if len(got) != 9 {
		t.Errorf("got %d problems, want 9: %q", len(got), got)
	}
	ok := "# T\n\nGoal: x.\n\n```mermaid\nflowchart LR\n```\n\n```yaml\n--8<-- \"a.yaml\"\n```\n\n## Read next\n"
	if p := guideProblems("g", ok+strings.Repeat("\n", guideMaxLines), cliRef); len(p) != 1 {
		t.Errorf("a long guide: got %q, want only the line limit", p)
	}

	md := "\n    | `bad flag <x> here`, `--push requires` | fix |\n"
	src := `errors.New("--push requires --registry"); fmt.Errorf("bad flag %s there")`
	got = troubleshootingProblems(md, src)
	want := []string{"troubleshooting: must include " + exitCodesInclude, `troubleshooting: "bad flag <x> here" is not in the Go source`}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := troubleshootingProblems(exitCodesInclude+md, src); len(got) != 1 {
		t.Errorf("with the include: got %q, want only the unknown message", got)
	}

	src = `add("kind: got %q, want %q", k); return nil, "signature: " + err.Error(); add("%s: %w", f, err)`
	formats := messageFormats(src, msgRe)
	if want := []string{"kind: got %q, want %q", "signature: %v", "%s: %w"}; !slices.Equal(formats, want) {
		t.Errorf("formats %q, want %q", formats, want)
	}
	got = uncoveredMessages("| Message | Fix |\n| `kind: got <x>, want <y>` | fix |\n", formats)
	if want := []string{`troubleshooting: no row for "signature: %v"`}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
