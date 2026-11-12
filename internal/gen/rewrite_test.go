package gen

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/contract"
)

// handWritten is a TestSet with comments, blank lines and mixed styles,
// a stale generated fragment and test between hand-written ones, and a
// hand-written e2e test that uses a generated fragment.
const handWritten = `# Shared inputs, written by hand.
apiVersion: aval.trianalab.io/v1
kind: TestSet

defaults:
  scope:
    dependencies: ["go.mod", "go.sum"]   # lockfiles
  cacheability: hermetic

fragments:
  # the database
  db:
    scope: {source: ["db/**"]}

  # stale, owned by aval gen go
  go.old:
    scope: {source: ["old/*.go"]}
  web:
    scope:
      source:
      - web/**

tests:
  - metadata: {name: e2e}   # cross-language
    use: [db, web, go.a]
    runner: {command: [make, e2e]}
  # stale
  - metadata: {name: go.old}
    runner: {command: [go, test, ./old]}
    use: [go.old]

  - metadata: {name: lint}
    runner: {command: [make, lint]}
    scope: {source: ["**/*.go"]}
`

const generatedWant = `# Shared inputs, written by hand.
apiVersion: aval.trianalab.io/v1
kind: TestSet

defaults:
  scope:
    dependencies: ["go.mod", "go.sum"]   # lockfiles
  cacheability: hermetic

fragments:
  # the database
  db:
    scope: {source: ["db/**"]}

  # stale, owned by aval gen go
  go.a:
    scope:
      source:
        - a/*.go
  go.b:
    scope:
      source:
        - b/*.go
  web:
    scope:
      source:
      - web/**

tests:
  - metadata: {name: e2e}   # cross-language
    use: [db, web, go.a]
    runner: {command: [make, e2e]}
  # stale
  - metadata: {name: go.a}
    runner: {command: [go, test, ./a]}
    use:
      - go.a
      - go.b
  - metadata: {name: go.b}
    runner: {command: [go, test, ./b]}
    use:
      - go.b

  - metadata: {name: lint}
    runner: {command: [make, lint]}
    scope: {source: ["**/*.go"]}
`

func rewrite(t *testing.T, data, lang string, units ...Unit) string {
	t.Helper()
	out, err := Rewrite([]byte(data), lang, units, nil)
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	return string(out)
}

func TestRewriteKeepsHandWritten(t *testing.T) {
	units, err := fake{units: []Unit{unit("b"), unit("a", "b")}}.Units(".")
	if err != nil {
		t.Fatal(err)
	}
	got := rewrite(t, handWritten, "go", units...)
	if got != generatedWant {
		t.Fatalf("got:\n%s\nwant:\n%s\n%s", got, generatedWant, Diff("got", "want", []byte(got), []byte(generatedWant)))
	}
	// Idempotent: a second run, and a run with the units in another order,
	// change nothing (what --check relies on).
	if again := rewrite(t, got, "go", unit("a", "b"), unit("b")); again != got {
		t.Errorf("not idempotent:\n%s", Diff("first", "second", []byte(got), []byte(again)))
	}
}

// Line numbers follow yaml.v3, which also breaks lines on a lone \r: a
// file with \r\n or old Mac \r endings is rewritten in its own endings and
// round-trips byte for byte.
func TestRewriteLineEndings(t *testing.T) {
	for _, eol := range []string{"\r\n", "\r"} {
		in, want := strings.ReplaceAll(handWritten, "\n", eol), strings.ReplaceAll(generatedWant, "\n", eol)
		if got := rewrite(t, in, "go", unit("b"), unit("a", "b")); got != want {
			t.Errorf("%q: got %q", eol, got)
		}
		if got := rewrite(t, want, "go", unit("a", "b"), unit("b")); got != want {
			t.Errorf("%q: up to date file changed: %q", eol, got)
		}
	}
	// A quoted U+2028 or U+0085 is a line break to yaml.v3 too.
	for _, br := range []string{"\u2028", "\u0085", "\r"} {
		in := "kind: TestSet\napiVersion: aval.trianalab.io/v1\ndefaults: {cacheability: hermetic}\ntests:\n- metadata: {name: e2e}\n  runner: {command: [\"x" + br + "y\"]}\n  scope: {source: [x]}\n- metadata: {name: go.old}\n  runner: {command: [x]}\n  scope: {source: [x]}\n"
		got := rewrite(t, in, "go", unit("a"))
		if strings.Contains(got, "go.old") || !strings.Contains(got, "x"+br+"y") || !strings.Contains(got, "- metadata: {name: go.a}") {
			t.Errorf("%q: got %q", br, got)
		}
	}
}

func TestRewriteInsertsWhenNothingOwned(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"append to the collections": {
			"apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults: {cacheability: hermetic}\nfragments:\n  db:\n    scope: {source: [db]}\n# trailing\n\ntests:\n- metadata: {name: e2e}\n  use: [db]\n  runner: {command: [x]}\n# end\n",
			"apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults: {cacheability: hermetic}\nfragments:\n  db:\n    scope: {source: [db]}\n  go.a:\n    scope:\n      source:\n        - a/*.go\n# trailing\n\ntests:\n- metadata: {name: e2e}\n  use: [db]\n  runner: {command: [x]}\n- metadata: {name: go.a}\n  runner: {command: [go, test, ./a]}\n  use:\n    - go.a\n# end\n",
		},
		"missing keys, no final newline": {
			"apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults: {cacheability: hermetic}",
			"apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults: {cacheability: hermetic}\nfragments:\n  go.a:\n    scope:\n      source:\n        - a/*.go\ntests:\n  - metadata: {name: go.a}\n    runner: {command: [go, test, ./a]}\n    use:\n      - go.a\n",
		},
		"dash on its own line": {
			"apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults: {cacheability: hermetic}\ntests:\n  -\n    metadata: {name: e2e}\n    runner: {command: [x]}\n    scope: {source: [x]}\n",
			"apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults: {cacheability: hermetic}\ntests:\n  -\n    metadata: {name: e2e}\n    runner: {command: [x]}\n    scope: {source: [x]}\n  - metadata: {name: go.a}\n    runner: {command: [go, test, ./a]}\n    use:\n      - go.a\nfragments:\n  go.a:\n    scope:\n      source:\n        - a/*.go\n",
		},
		"empty keys": {
			"apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults: {cacheability: hermetic}\nfragments:\ntests:\n",
			"apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults: {cacheability: hermetic}\nfragments:\n  go.a:\n    scope:\n      source:\n        - a/*.go\ntests:\n  - metadata: {name: go.a}\n    runner: {command: [go, test, ./a]}\n    use:\n      - go.a\n",
		},
	} {
		if got := rewrite(t, tc.in, "go", unit("a")); got != tc.want {
			t.Errorf("%s:\n%s", name, Diff("got", "want", []byte(got), []byte(tc.want)))
		}
	}
}

// Two generators own disjoint parts of one file: each rewrites only its own.
func TestRewriteTwoGenerators(t *testing.T) {
	base := "apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults: {cacheability: hermetic}\n"
	goOnly := rewrite(t, base, "go", unit("a"))
	both := rewrite(t, goOnly, "js", Unit{ID: "@acme/web", Source: []string{"web/**"}, Command: []string{"npm", "test"}})
	if !strings.Contains(both, "js.acme.web:") || !strings.Contains(both, "go.a:") {
		t.Fatalf("missing entries:\n%s", both)
	}
	if again := rewrite(t, both, "go", unit("a")); again != both {
		t.Errorf("go rewrote js entries:\n%s", Diff("before", "after", []byte(both), []byte(again)))
	}
	if again := rewrite(t, both, "js", Unit{ID: "@acme/web", Source: []string{"web/**"}, Command: []string{"npm", "test"}}); again != both {
		t.Errorf("js rewrote go entries:\n%s", Diff("before", "after", []byte(both), []byte(again)))
	}
}

func TestRewriteErrors(t *testing.T) {
	set := "apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults: {cacheability: hermetic}\n"
	for name, tc := range map[string]struct {
		in    string
		units []Unit
		want  string
	}{
		"bad units":        {set, nil, "no units found"},
		"empty file":       {"", []Unit{unit("a")}, "parse"},
		"bad yaml":         {"a: [", []Unit{unit("a")}, "parse"},
		"two documents":    {set + "---\na: 1\n", []Unit{unit("a")}, "one YAML document"},
		"not a mapping":    {"- a\n", []Unit{unit("a")}, "block mapping"},
		"flow root":        {"{kind: TestSet}\n", []Unit{unit("a")}, "block mapping"},
		"kind Test":        {"kind: Test\n", []Unit{unit("a")}, "kind: TestSet"},
		"flow fragments":   {set + "fragments: {}\n", []Unit{unit("a")}, "fragments: aval gen needs a block mapping"},
		"null tests":       {set + "tests: ~\n", []Unit{unit("a")}, "tests: aval gen needs a block sequence"},
		"scalar test":      {set + "tests:\n  - x\n", []Unit{unit("a")}, "not a valid TestSet"},
		"mapping as tests": {set + "tests:\n  a: b\n", []Unit{unit("a")}, "tests: aval gen needs a block sequence"},
		// A hand-written test uses the fragment of a unit that is gone.
		"removed unit used by hand": {set + "tests:\n  - metadata: {name: e2e}\n    use: [go.gone]\n    runner: {command: [x]}\n    scope: {source: [x]}\n", []Unit{unit("a")}, `unknown fragment "go.gone"`},
		// The line edit would drop the last line of a block scalar it took
		// for a comment: the node check refuses to write.
		"block scalar": {set + "tests:\n  - metadata: {name: go.a}\n    runner: {command: [x]}\n    scope: {source: [x]}\n  - metadata: {name: h}\n    scope: {source: [x]}\n    runner:\n      command:\n        - sh\n        - |\n          echo\n          # kept\n  - metadata: {name: go.b}\n    runner: {command: [x]}\n    scope: {source: [x]}\n", []Unit{unit("a")}, "without changing other lines"},
	} {
		_, err := Rewrite([]byte(tc.in), "go", tc.units, nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
		}
	}
}

func TestDiff(t *testing.T) {
	got := Diff("a", "b", []byte("x\ny\nz\n"), []byte("x\nY\nz\n"))
	want := "--- a\n+++ b\n@@ -2,1 +2,1 @@\n-y\n+Y\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRewriteNoTest(t *testing.T) {
	units := []Unit{unit("a", "cmd/gen"), unit("cmd/gen"), unit("cmd/genx"), unit("tests/e2e")}
	out, err := Rewrite([]byte(base), "go", units, nil, "cmd/gen*", "tests/**")
	if err != nil {
		t.Fatal(err)
	}
	cs, err := contract.ParseAll(out)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range cs {
		names = append(names, c.Metadata.Name)
	}
	if !slices.Equal(names, []string{"go.a"}) {
		t.Errorf("tests %v, want only go.a", names)
	}
	// go.a uses go.cmd.gen; no test uses the other two, so they go.
	for f, want := range map[string]bool{"go.cmd.gen:": true, "go.cmd.genx:": false, "go.tests.e2e:": false} {
		if strings.Contains(string(out), "  "+f+"\n") != want {
			t.Errorf("fragment %s kept: %v, want %v:\n%s", f, !want, want, out)
		}
	}
	if !strings.Contains(string(out), "use:\n      - go.a\n      - go.cmd.gen\n") {
		t.Errorf("go.a no longer uses go.cmd.gen:\n%s", out)
	}
	// A hand-written test that uses an excluded unit keeps its fragment.
	hand := base + "tests:\n  - metadata: {name: e2e}\n    use: [go.tests.e2e]\n    runner: {command: [x]}\n"
	if kept := string(must(Rewrite([]byte(hand), "go", units, nil, "cmd/gen*", "tests/**"))); !strings.Contains(kept, "  go.tests.e2e:\n") || strings.Contains(kept, "{name: go.tests.e2e}") {
		t.Errorf("hand-written use:\n%s", kept)
	}
	// Without patterns every unit gets its test back.
	if all := rewrite(t, string(out), "go", units...); strings.Count(all, "- metadata:") != 4 {
		t.Errorf("without --no-test:\n%s", all)
	}
}

// Every unit under --no-test: only the hand-written entries are left.
func TestRewriteNoTestAll(t *testing.T) {
	hand := base + "tests:\n  - metadata: {name: e2e}\n    runner: {command: [x]}\n    scope: {source: [x]}\n"
	if out := string(must(Rewrite([]byte(hand), "go", []Unit{unit("a")}, nil, "a"))); out != hand {
		t.Errorf("got:\n%s", out)
	}
	if out := string(must(Rewrite([]byte(hand+"fragments:\n"), "go", []Unit{unit("a")}, nil, "a"))); out != hand+"fragments:\n" {
		t.Errorf("empty fragments:\n%s", out)
	}
	gend := rewrite(t, hand, "go", unit("a"))
	if out := string(must(Rewrite([]byte(gend), "go", []Unit{unit("a")}, nil, "*"))); !strings.HasSuffix(out, "fragments:\n") || strings.Contains(out, "go.a") {
		t.Errorf("got:\n%s", out)
	}
	if _, err := Rewrite([]byte(base), "go", []Unit{unit("a")}, nil, "a"); err == nil || !strings.Contains(err.Error(), "at least one test") {
		t.Errorf("no test left: %v", err)
	}
}

func must(out []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return out
}

func TestRewriteNoTestErrors(t *testing.T) {
	units := []Unit{unit("a"), unit("b")}
	for pattern, want := range map[string]string{
		"c":     "--no-test c: matches no unit",
		"a/**x": "'**' must be a whole path segment",
		"/a":    "relative",
	} {
		_, err := Rewrite([]byte(base), "go", units, nil, "a", pattern)
		if err == nil || !strings.Contains(err.Error(), want) || errors.Is(err, ErrUnits) {
			t.Errorf("%q: got %v, want %q (not ErrUnits)", pattern, err, want)
		}
	}
}

var testShared = &Shared{
	Toolchain:    map[string]string{"go": "1.22"},
	Probe:        map[string][]string{"go": {"go", "env", "GOVERSION"}},
	Dependencies: []string{"go.sum", "go.mod"},
	Env:          []string{"GOOS", "CGO_ENABLED"},
}

// The shared fragment is written once, in name order, and every generated
// test uses it; hand-written tests are left alone.
func TestRewriteShared(t *testing.T) {
	hand := base + "tests:\n  - metadata: {name: e2e}\n    runner: {command: [x]}\n    scope: {source: [x]}\n"
	out := string(must(Rewrite([]byte(hand), "go", []Unit{unit("a", "b"), unit("b")}, testShared)))
	frag := "  go.module:\n    scope:\n      dependencies:\n        - go.mod\n        - go.sum\n    environment:\n" +
		"      toolchain: {go: \"1.22\"}\n      probe:\n        go: [go, env, GOVERSION]\n      env:\n        - CGO_ENABLED\n        - GOOS\n"
	if !strings.Contains(out, frag) {
		t.Fatalf("no go.module fragment:\n%s", out)
	}
	cs, err := contract.ParseAll([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		gen := strings.HasPrefix(c.Metadata.Name, "go.")
		if got := c.Environment.Toolchain["go"] == "1.22" && slices.Contains(c.Environment.Env, "GOOS"); got != gen {
			t.Errorf("%s: has go.module = %v, want %v", c.Metadata.Name, got, gen)
		}
	}
	// Idempotent, and dropped when no test uses it.
	if again := string(must(Rewrite([]byte(out), "go", []Unit{unit("a", "b"), unit("b")}, testShared))); again != out {
		t.Errorf("second run differs:\n%s", Diff("first", "second", []byte(out), []byte(again)))
	}
	if none := string(must(Rewrite([]byte(out), "go", []Unit{unit("a")}, testShared, "a"))); strings.Contains(none, "go.module") {
		t.Errorf("go.module kept without a test that uses it:\n%s", none)
	}
}

func TestRewriteSharedErrors(t *testing.T) {
	if _, err := Rewrite([]byte(base), "go", []Unit{unit("module")}, testShared); !errors.Is(err, ErrUnits) || !strings.Contains(err.Error(), "go.module") {
		t.Errorf("a unit named module: %v", err)
	}
	// A hand-written toolchain that disagrees is a conflict, not an override.
	clash := strings.Replace(base, "  cacheability:", "  environment: {toolchain: {go: \"1.21\"}}\n  cacheability:", 1)
	if _, err := Rewrite([]byte(clash), "go", []Unit{unit("a")}, testShared); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Errorf("conflicting toolchain: %v", err)
	}
}
