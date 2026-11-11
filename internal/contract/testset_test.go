package contract

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// set is a TestSet with defaults, two fragments, a plain test and a matrix
// test. The Go fingerprint tests use the same shape on disk.
const set = `
apiVersion: aval.trianalab.io/v1
kind: TestSet
defaults:
  scope:
    source: ["go.mod"]
    exclude: ["**/testdata/**"]
    dependencies: ["go.sum"]
  environment:
    toolchain: {go: "1.25"}
    paths: ["~/.netrc"]
  cacheability: hermetic
fragments:
  db:
    scope: {source: ["db/**"]}
    environment:
      toolchain: {postgres: "16"}
      probe: {postgres: [postgres, --version]}
      env: [DB_URL]
      paths: ["~/.pgpass", "~/.netrc"]
  web:
    scope: {source: ["web/**", "go.mod"]}
tests:
  - metadata: {name: unit}
    runner: {command: [go, test, ./...]}
    scope: {source: ["**/*.go"]}
  - metadata: {name: "shard-{index}-of-{total}"}
    use: [web, db]
    runner: {command: [go, test, "-shard={index}/{total}", "{literal x}"]}
    matrix: {index: ["1", "2"], total: ["2"]}
    cacheability: controlled-environment
`

func TestParseAllTestSet(t *testing.T) {
	cs, err := ParseAll([]byte(set))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range cs {
		names = append(names, c.Metadata.Name)
	}
	if want := []string{"unit", "shard-1-of-2", "shard-2-of-2"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names %q, want %q", names, want)
	}
	unit := Contract{
		APIVersion: APIVersion, Kind: Kind, Metadata: Metadata{Name: "unit"},
		Runner:       Runner{Command: []string{"go", "test", "./..."}},
		Scope:        Scope{Source: []string{"go.mod", "**/*.go"}, Exclude: []string{"**/testdata/**"}, Dependencies: []string{"go.sum"}},
		Environment:  Environment{Toolchain: map[string]string{"go": "1.25"}, Paths: []string{"~/.netrc"}},
		Cacheability: Hermetic,
	}
	if !reflect.DeepEqual(*cs[0], unit) {
		t.Fatalf("unit:\n got %+v\nwant %+v", *cs[0], unit)
	}
	s := cs[2]
	if want := []string{"go", "test", "-shard=2/2", "{literal x}"}; !reflect.DeepEqual(s.Runner.Command, want) {
		t.Errorf("command %q, want %q", s.Runner.Command, want)
	}
	// Fragments in sorted order (db, web) after defaults; go.mod once.
	if want := []string{"go.mod", "db/**", "web/**"}; !reflect.DeepEqual(s.Scope.Source, want) {
		t.Errorf("source %q, want %q", s.Scope.Source, want)
	}
	if want := []string{"~/.netrc", "~/.pgpass"}; !reflect.DeepEqual(s.Environment.Paths, want) {
		t.Errorf("paths %q, want %q", s.Environment.Paths, want)
	}
	if s.Cacheability != ControlledEnvironment || s.Environment.Toolchain["postgres"] != "16" || s.Environment.Env[0] != "DB_URL" || len(s.Environment.Probe["postgres"]) != 2 {
		t.Errorf("merged %+v", s)
	}
	// A kind: Test parses to itself.
	cs, err = ParseAll([]byte(valid))
	if err != nil || len(cs) != 1 || cs[0].Metadata.Name != "orders-integration" {
		t.Fatalf("kind: Test: %v %v", cs, err)
	}
}

// A matrix substitutes {var} into toolchain values, inherited ones too, in
// one pass: a value that holds "{x}" is kept as is. Probe argv is not
// substituted (#255); env names cannot hold braces.
func TestParseAllMatrixToolchain(t *testing.T) {
	doc := `apiVersion: aval.trianalab.io/v1
kind: TestSet
defaults:
  scope: {source: [a.txt]}
  environment: {toolchain: {arch: "{arch}"}}
  cacheability: hermetic
tests:
  - metadata: {name: "unit-{os}"}
    runner: {command: [echo, "{os}"]}
    matrix: {os: [linux, darwin], arch: ["{os}"]}
    environment:
      toolchain: {platform: "{os}", go: "1.25", both: "{os}/{arch}"}
      probe: {platform: [go, env, "{os}"]}
`
	cs, err := ParseAll([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	for i, goos := range []string{"linux", "darwin"} {
		c := cs[i]
		want := map[string]string{"platform": goos, "go": "1.25", "arch": "{os}", "both": goos + "/{os}"}
		if c.Metadata.Name != "unit-"+goos || !reflect.DeepEqual(c.Environment.Toolchain, want) {
			t.Errorf("leg %s: %s %v, want %v", goos, c.Metadata.Name, c.Environment.Toolchain, want)
		}
		if c.Environment.Probe["platform"][2] != "{os}" {
			t.Errorf("leg %s: probe substituted: %v", goos, c.Environment.Probe)
		}
	}
	if cs[0].Environment.Toolchain["platform"] == cs[1].Environment.Toolchain["platform"] {
		t.Fatal("legs share one toolchain map")
	}
}

// The order of use never changes the expanded test.
func TestParseAllUseOrder(t *testing.T) {
	a, err := ParseAll([]byte(set))
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseAll([]byte(strings.Replace(set, "use: [web, db]", "use: [db, web, db]", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("use order changed the tests:\n%+v\n%+v", a[1], b[1])
	}
}

// A test cannot drop a list item it inherits: lists only grow.
func TestParseAllInheritedListsStay(t *testing.T) {
	cs, err := ParseAll([]byte(strings.Replace(set, `scope: {source: ["**/*.go"]}`, `scope: {source: ["**/*.go"], dependencies: []}
    environment: {env: []}`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if got := cs[0].Scope; !reflect.DeepEqual(got.Dependencies, []string{"go.sum"}) || !reflect.DeepEqual(got.Source, []string{"go.mod", "**/*.go"}) {
		t.Fatalf("inherited items removed: %+v", got)
	}
}

func TestParseAllTestSetInvalid(t *testing.T) {
	cases := map[string]struct{ from, to, want string }{
		"unknown fragment":    {"use: [web, db]", "use: [web, db, cache]", `tests[1] (shard-{index}-of-{total}): use: unknown fragment "cache"`},
		"unused fragment":     {"use: [web, db]", "use: [db]", "fragments.web: not used by any test"},
		"fragment uses":       {"web:\n    scope:", "web:\n    use: [db]\n    scope:", "fragments.web: use is not allowed here: a fragment cannot use another fragment"},
		"fragment exclude":    {`scope: {source: ["db/**"]}`, `scope: {source: ["db/**"], exclude: [x]}`, "fragments.db: scope.exclude is not allowed here"},
		"fragment runner":     {"web:\n", "web:\n    runner: {command: [x]}\n", "fragments.web: runner is not allowed here"},
		"fragment metadata":   {"web:\n", "web:\n    metadata: {name: x}\n", "fragments.web: metadata is not allowed here"},
		"fragment cache":      {"web:\n", "web:\n    cacheability: hermetic\n", "fragments.web: cacheability is not allowed here"},
		"fragment outputs":    {"web:\n", "web:\n    outputs: [x]\n", "fragments.web: outputs is not allowed here"},
		"fragment matrix":     {"web:\n", "web:\n    matrix: {a: [b]}\n", "fragments.web: matrix is not allowed here"},
		"fragment name":       {"web:", "Web:", "fragments.Web: the name must be"},
		"defaults runner":     {"  cacheability: hermetic\nfragments", "  runner: {command: [x]}\n  cacheability: hermetic\nfragments", "defaults: runner is not allowed here"},
		"defaults use":        {"  cacheability: hermetic\nfragments", "  use: [db]\n  cacheability: hermetic\nfragments", "defaults: use is not allowed here: it belongs to a test only"},
		"toolchain conflict":  {`toolchain: {postgres: "16"}`, `toolchain: {postgres: "16", go: "1.24"}`, `environment.toolchain.go: "1.25" from defaults conflicts with "1.24" from fragment db`},
		"probe conflict":      {"[web, db]\n", "[web, db]\n    environment: {probe: {postgres: [pg, -V]}}\n", `environment.probe.postgres: ["postgres" "--version"] from fragment db conflicts with ["pg" "-V"] from the test`},
		"unknown var":         {`"{literal x}"`, `"{totl}"`, `unknown matrix variable {totl} in "{totl}" (the matrix has index, total)`},
		"duplicate expanded":  {`index: ["1", "2"]`, `index: ["1", "1"]`, `metadata.name "shard-1-of-2" is also the name of tests[1]`},
		"duplicate name":      {"name: unit}", "name: shard-1-of-2}", `metadata.name "shard-1-of-2" is also the name of tests[0]`},
		"empty matrix":        {`matrix: {index: ["1", "2"], total: ["2"]}`, `matrix: {}`, "matrix: at least one variable is required"},
		"empty values":        {`total: ["2"]`, `total: []`, "matrix.total: at least one value is required"},
		"bad var":             {`total: ["2"]}`, `total: ["2"], 1x: [a]}`, `matrix: invalid variable name "1x"`},
		"toolchain var":       {"    cacheability: controlled", "    environment: {toolchain: {os: \"{oss}\"}}\n    cacheability: controlled", `tests[1] (shard-{index}-of-{total}): unknown matrix variable {oss} in environment.toolchain.os "{oss}" (the matrix has index, total)`},
		"no-matrix var":       {"scope: {source: [\"**/*.go\"]}", "scope: {source: [\"**/*.go\"]}\n    environment: {toolchain: {os: \"x-{os}\"}}", "tests[0] (unit): environment.toolchain.os: {os} is a matrix reference but the test has no matrix"},
		"inherited no-matrix": {`toolchain: {go: "1.25"}`, `toolchain: {go: "{go}"}`, "tests[0] (unit): environment.toolchain.go: {go} is a matrix reference but the test has no matrix"},
		"invalid test":        {"name: unit}", "name: Unit}", `tests[0] (Unit): metadata.name: "Unit"`},
		"apiVersion":          {"aval.trianalab.io/v1", "aval.trianalab.io/v2", "apiVersion"},
		"typo":                {"fragments:", "fragmens:", "did you mean fragments?"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			doc := strings.Replace(set, tc.from, tc.to, 1)
			if doc == set {
				t.Fatalf("replacement %q not applied", tc.from)
			}
			_, err := ParseAll([]byte(doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParseAllRootConflict(t *testing.T) {
	doc := strings.Replace(set, `exclude: ["**/testdata/**"]`, `exclude: ["**/testdata/**"]
    root: ..`, 1)
	doc = strings.Replace(doc, `scope: {source: ["db/**"]}`, `scope: {source: ["db/**"], root: ../..}`, 1)
	if _, err := ParseAll([]byte(doc)); err == nil || !strings.Contains(err.Error(), `scope.root: ".." from defaults conflicts with "../.." from fragment db`) {
		t.Fatalf("got %v", err)
	}
}

func TestParseAllTestSetEmpty(t *testing.T) {
	_, err := ParseAll([]byte("apiVersion: aval.trianalab.io/v1\nkind: TestSet\n"))
	if err == nil || !strings.Contains(err.Error(), "tests: at least one test is required") {
		t.Fatalf("got %v", err)
	}
	// One decoding error, before any expansion.
	if _, err := ParseAll([]byte("kind: TestSet\ntests: [1]\n")); err == nil {
		t.Fatal("accepted a test that is not a mapping")
	}
}

func TestParseAllMatrixLimit(t *testing.T) {
	vals := strings.Repeat(`"x", `, 33)
	doc := strings.Replace(set, `matrix: {index: ["1", "2"], total: ["2"]}`, "matrix: {a: ["+vals+`"y"], b: [`+vals+`"y"]}`, 1)
	if _, err := ParseAll([]byte(doc)); err == nil || !strings.Contains(err.Error(), "more than 1024 tests") {
		t.Fatalf("got %v", err)
	}
}

// maxTests bounds plain tests too, not only matrix expansion (#207): 1024
// plain tests expand, 1025 do not, and nothing past the limit is expanded.
func TestParseAllPlainTestLimit(t *testing.T) {
	doc := func(n int) []byte {
		var b strings.Builder
		b.WriteString("apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults: {scope: {source: [a]}, cacheability: hermetic}\ntests:\n")
		for i := range n {
			fmt.Fprintf(&b, "- {metadata: {name: t%d}, runner: {command: [x]}}\n", i)
		}
		return []byte(b.String())
	}
	if cs, err := ParseAll(doc(maxTests)); err != nil || len(cs) != maxTests {
		t.Fatalf("%d tests: %d, %v", maxTests, len(cs), err)
	}
	if _, err := ParseAll(doc(maxTests + 1)); err == nil || !strings.Contains(err.Error(), "tests[1024] (t1024): the set expands to more than 1024 tests") {
		t.Fatalf("got %v", err)
	}
}

// Every expanded test marshals back to a standalone kind: Test that parses
// to the same contract: what 'aval explain --expand' prints (#207).
func TestExpandedTestRoundTrips(t *testing.T) {
	cs, err := ParseAll([]byte(set))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		c.Scope.Resolve = []Reference{{Kind: "npm", Ref: "react@^18"}}
		c.Scope.AllowMutable = []AllowMutable{{Ref: "react@^18", Reason: "pinned by the lockfile"}}
		raw, err := yaml.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		back, err := Parse(raw)
		if err != nil || !reflect.DeepEqual(back, c) {
			t.Fatalf("%s:\n%s\ngot %+v, %v\nwant %+v", c.Metadata.Name, raw, back, err, c)
		}
	}
}

// Parse takes one test: a TestSet is ErrTestSet, which scope.contracts
// reports.
func TestParseRejectsTestSet(t *testing.T) {
	if _, err := Parse([]byte(set)); err != ErrTestSet {
		t.Fatalf("got %v, want ErrTestSet", err)
	}
}

// Load selects a test by name (#207). Without a name a file of one test
// loads it and a file of more fails listing the names; a name selects that
// test of a TestSet and must equal metadata.name on a kind: Test.
func TestLoadTestSet(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	many, test := write("many.yaml", set), write("test.yaml", valid)
	one := write("one.yaml", "apiVersion: aval.trianalab.io/v1\nkind: TestSet\ntests:\n- {metadata: {name: unit}, runner: {command: [x]}, scope: {source: [a]}, cacheability: hermetic}\n")
	for _, tc := range []struct{ file, test, name, err string }{
		{many, "", "", "the TestSet holds 3 tests (unit, shard-1-of-2, shard-2-of-2); select one with --test"},
		{many, "shard-2-of-2", "shard-2-of-2", ""},
		{many, "shard-3-of-2", "", `many.yaml: no test named "shard-3-of-2"; its tests: unit, shard-1-of-2, shard-2-of-2`},
		// False hits: a prefix or another case never selects a test.
		{many, "shard-1", "", `no test named "shard-1"`},
		{many, "UNIT", "", `no test named "UNIT"`},
		{one, "", "unit", ""},
		{one, "unit", "unit", ""},
		{test, "", "orders-integration", ""},
		{test, "orders-integration", "orders-integration", ""},
		{test, "orders", "", `no test named "orders"; its tests: orders-integration`},
		{write("bad.yaml", "kind: TestSet\n"), "x", "", "bad.yaml: "},
	} {
		c, err := Load(tc.file, tc.test)
		switch {
		case tc.err == "" && (err != nil || c.Metadata.Name != tc.name):
			t.Errorf("%s --test %q: %v, %v; want %s", tc.file, tc.test, c, err, tc.name)
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%s --test %q: %v; want %q", tc.file, tc.test, err, tc.err)
		}
	}
}

// scope.optionalSource unions across layers like scope.source, and a glob
// that one layer makes required and another optional is rejected.
func TestParseAllOptionalSource(t *testing.T) {
	opt := strings.Replace(set, `scope: {source: ["db/**"]}`, `scope: {source: ["db/**"], optionalSource: ["db/seed/*.sql"]}`, 1)
	opt = strings.Replace(opt, "    use: [web, db]\n", "    use: [web, db]\n    scope: {optionalSource: [\"**/*testdata*.yaml\", \"db/seed/*.sql\"]}\n", 1)
	cs, err := ParseAll([]byte(opt))
	if err != nil {
		t.Fatal(err)
	}
	if cs[0].Scope.OptionalSource != nil {
		t.Errorf("unit optionalSource %q, want nil", cs[0].Scope.OptionalSource)
	}
	if want := []string{"db/seed/*.sql", "**/*testdata*.yaml"}; !reflect.DeepEqual(cs[1].Scope.OptionalSource, want) {
		t.Errorf("optionalSource %q, want %q", cs[1].Scope.OptionalSource, want)
	}
	both := strings.Replace(set, `scope: {source: ["**/*.go"]}`, `scope: {source: ["**/*.go"], optionalSource: ["go.mod"]}`, 1)
	if _, err := ParseAll([]byte(both)); err == nil || !strings.Contains(err.Error(), `"go.mod" is also in scope.source`) {
		t.Fatalf("want a both-fields error, got %v", err)
	}
}
