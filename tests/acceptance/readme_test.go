package acceptance

import (
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	ggcr "github.com/google/go-containerregistry/pkg/registry"
)

// quickstartStep is one ```sh block between the quickstart markers and, when
// a ```text block follows it, the output the page promises.
type quickstartStep struct {
	script string
	want   string
	hasOut bool
}

var fenceRE = regexp.MustCompile("(?s)```(sh|text)\n(.*?)```")

// quickstartSteps reads the blocks between the quickstart markers of a
// Markdown file under the repository root. `docker` lines (registry:2 and its
// cleanup before and after) are dropped: the test serves an in-process registry instead.
func quickstartSteps(t *testing.T, doc string) []quickstartStep {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", doc))
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(raw), "<!-- quickstart:start")
	section, _, ok2 := strings.Cut(rest, "<!-- quickstart:end")
	if !ok || !ok2 {
		t.Fatalf("%s: quickstart markers not found", doc)
	}
	var steps []quickstartStep
	var dropped []string
	for _, m := range fenceRE.FindAllStringSubmatch(section, -1) {
		if m[1] == "text" {
			if len(steps) == 0 || steps[len(steps)-1].hasOut {
				t.Fatalf("%s: text block without an sh block before it:\n%s", doc, m[2])
			}
			steps[len(steps)-1].want, steps[len(steps)-1].hasOut = m[2], true
			continue
		}
		var kept []string
		for _, line := range strings.Split(m[2], "\n") {
			if strings.HasPrefix(line, "docker ") {
				dropped = append(dropped, line)
				continue
			}
			kept = append(kept, line)
		}
		steps = append(steps, quickstartStep{script: strings.Join(kept, "\n")})
	}
	if !slices.ContainsFunc(dropped, func(l string) bool { return strings.Contains(l, "registry:2") }) {
		t.Fatalf("%s: quickstart must start a local registry:2, dropped %q", doc, dropped)
	}
	return steps
}

// runQuickstart runs the steps in order in one shell, against an in-process
// registry that stands in for localhost:5001, and returns each step's
// combined output. A failing step does not stop the shell; each step's exit
// code is in codes.
func runQuickstart(t *testing.T, steps []quickstartStep) (outs []string, codes []int, host string) {
	t.Helper()
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	host = strings.TrimPrefix(srv.URL, "http://")
	var body strings.Builder
	for i, s := range steps {
		fmt.Fprintf(&body, "echo '@@step %d@@'\n%s\necho \"@@exit $?@@\"\n", i, strings.ReplaceAll(s.script, "localhost:5001", host))
	}
	cmd := exec.Command("sh", "-c", body.String())
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"PATH="+filepath.Dir(aval)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DOCKER_CONFIG="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("quickstart failed: %v\n%s", err, out)
	}
	parts := regexp.MustCompile(`(?m)^@@step \d+@@\n`).Split(string(out), -1)
	if len(parts) != len(steps)+1 {
		t.Fatalf("quickstart output has %d steps, want %d\n%s", len(parts)-1, len(steps), out)
	}
	exitRE := regexp.MustCompile(`(?m)^@@exit (\d+)@@\n\z`)
	shownRE := regexp.MustCompile(`(?m)^exit code: (\d+)\n\z`)
	for _, p := range parts[1:] {
		m := exitRE.FindStringSubmatchIndex(p)
		if m == nil {
			t.Fatalf("step output lacks its exit marker:\n%s", p)
		}
		code := p[m[2]:m[3]]
		p = p[:m[0]]
		// A step that ends with `echo "exit code: $?"` shows the code of the
		// command before it; that is the step's code.
		if s := shownRE.FindStringSubmatch(p); s != nil {
			code = s[1]
		}
		n, _ := strconv.Atoi(code)
		outs, codes = append(outs, p), append(codes, n)
	}
	return outs, codes, host
}

// outputMatches compares output with a page's text block line by line; "..."
// in the block matches any run of characters.
func outputMatches(want, got string) bool {
	wl, gl := strings.Split(strings.TrimRight(want, "\n"), "\n"), strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(wl) != len(gl) {
		return false
	}
	for i, w := range wl {
		re := "^" + strings.ReplaceAll(regexp.QuoteMeta(w), regexp.QuoteMeta("..."), ".*") + "$"
		if !regexp.MustCompile(re).MatchString(gl[i]) {
			return false
		}
	}
	return true
}

func TestOutputMatches(t *testing.T) {
	for _, c := range []struct {
		want, got string
		ok        bool
	}{
		{"pushed r@sha256:...\n", "pushed r@sha256:abc\n", true},
		{"exit code: 0\n", "exit code: 5\n", false},
		{"a\nb\n", "a\n", false},
		{"a.b\n", "axb\n", false},
	} {
		if outputMatches(c.want, c.got) != c.ok {
			t.Errorf("outputMatches(%q, %q) = %v", c.want, c.got, !c.ok)
		}
	}
}

// docs/quickstart.md, run as written: every command block, and every output
// block it shows (miss, verified, reuse, changed input, exit 5) must be what
// the commands print.
func TestDocsQuickstart(t *testing.T) {
	steps := quickstartSteps(t, "docs/quickstart.md")
	outs, codes, host := runQuickstart(t, steps)
	checked := 0
	for i, s := range steps {
		// Only the last step (an untrusted signer) exits non-zero, with 5.
		if want := map[bool]int{true: 5}[i == len(steps)-1]; codes[i] != want {
			t.Errorf("step %d exited %d, want %d\n%s", i+1, codes[i], want, outs[i])
		}
		if !s.hasOut {
			if outs[i] != "" {
				t.Errorf("step %d prints output the page does not show:\n%s", i+1, outs[i])
			}
			continue
		}
		checked++
		if want := strings.ReplaceAll(s.want, "localhost:5001", host); !outputMatches(want, outs[i]) {
			t.Errorf("step %d output differs from the page\nwant:\n%s\ngot:\n%s", i+1, want, outs[i])
		}
	}
	if checked < 5 {
		t.Errorf("docs/quickstart.md shows %d outputs, want at least 5", checked)
	}
}
