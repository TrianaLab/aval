package gen

import (
	"strings"
	"testing"
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
	out, err := Rewrite([]byte(data), lang, units)
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
		_, err := Rewrite([]byte(tc.in), "go", tc.units)
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
