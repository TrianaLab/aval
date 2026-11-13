package gen

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/fingerprint"
)

const base = "apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults:\n  scope: {dependencies: [go.mod]}\n  cacheability: hermetic\n"

// graph is a -> b -> c, and d alone.
var graph = []Unit{unit("a", "b"), unit("b", "c"), unit("c"), unit("d")}

// digest fingerprints the generated test name of set over files.
func digest(t *testing.T, set string, files map[string]string, name string) string {
	t.Helper()
	cs, err := contract.ParseAll([]byte(set))
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{}
	for p, data := range files {
		fsys[p] = &fstest.MapFile{Data: []byte(data)}
	}
	for _, c := range cs {
		if c.Metadata.Name == name {
			fp, err := fingerprint.ComputeFS(fsys, c, fingerprint.Env{})
			if err != nil {
				t.Fatal(err)
			}
			return fp.Digest
		}
	}
	t.Fatalf("no test %s", name)
	return ""
}

func TestFalseHitTransitiveDependency(t *testing.T) {
	set := rewrite(t, base, "go", graph...)
	files := map[string]string{"go.mod": "module x", "a/a.go": "a", "b/b.go": "b", "c/c.go": "c", "d/d.go": "d"}
	before := digest(t, set, files, "go.a")

	changed := map[string]string{}
	for k, v := range files {
		changed[k] = v
	}
	changed["c/c.go"] = "c2" // a transitive dependency of a
	if digest(t, set, changed, "go.a") == before {
		t.Error("go.a reused after its transitive dependency c changed")
	}
	changed["c/c.go"], changed["d/d.go"] = "c", "d2" // an unrelated unit
	if digest(t, set, changed, "go.a") != before {
		t.Error("go.a changed after the unrelated unit d changed")
	}
	// Adding an unrelated unit neither renames nor rescopes go.a.
	more := rewrite(t, set, "go", append(slices.Clone(graph), unit("e"))...)
	changed["d/d.go"], changed["e/e.go"] = "d", "e"
	if digest(t, more, changed, "go.a") != before {
		t.Error("go.a changed after an unrelated unit e was added")
	}
}

// A dependency dropped from the graph is drift: the generated file differs,
// and Diff names the fragment that left the test's use.
func TestFalseHitDroppedDependencyIsDrift(t *testing.T) {
	set := rewrite(t, base, "go", graph...)
	now := rewrite(t, set, "go", unit("a"), unit("b", "c"), unit("c"), unit("d"))
	if now == set {
		t.Fatal("dropping a -> b left the file unchanged")
	}
	if d := Diff("aval.yaml", "generated", []byte(set), []byte(now)); !strings.Contains(d, "-      - go.b") {
		t.Errorf("diff does not show go.b leaving go.a:\n%s", d)
	}
}

// Hand-written entries never change, whatever the graph.
func TestFalseHitHandWrittenUnchanged(t *testing.T) {
	hand := base + "fragments:\n  shared:   # by hand\n    scope: {source: [VERSION]}\ntests:\n  - metadata: {name: e2e}\n    use: [shared, go.a]\n    runner: {command: [make, e2e]}\n"
	for _, units := range [][]Unit{graph, {unit("a")}, {unit("a", "z"), unit("z")}} {
		out := rewrite(t, hand, "go", units...)
		for l := range strings.SplitAfterSeq(hand, "\n") {
			if !strings.Contains(out, l) {
				t.Errorf("line %q lost", l)
			}
		}
	}
}

// reach is the transitive closure by Floyd-Warshall, a different algorithm
// from plan's walk.
func reach(n int, edge [][]bool) [][]bool {
	r := make([][]bool, n)
	for i := range r {
		r[i] = slices.Clone(edge[i])
		r[i][i] = true
	}
	for k := range n {
		for i := range n {
			for j := range n {
				r[i][j] = r[i][j] || r[i][k] && r[k][j]
			}
		}
	}
	return r
}

// FuzzGraph: any graph, cycles included, gives the same file whatever the
// order of units and edges, and each test uses exactly the fragments of
// the units its unit reaches.
func FuzzGraph(f *testing.F) {
	f.Add([]byte{3, 0x01, 0x12, 0x20}) // a cycle 0 -> 1 -> 2 -> 0
	f.Add([]byte{5, 0x01, 0x02, 0x13, 0x34})
	f.Add([]byte{1, 0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		n := int(data[0])%12 + 1
		edge := make([][]bool, n)
		for i := range edge {
			edge[i] = make([]bool, n)
		}
		for _, b := range data[1:] {
			edge[int(b>>4)%n][int(b&15)%n] = true
		}
		units := make([]Unit, n)
		for i := range units {
			units[i] = unit(fmt.Sprintf("u%d", i))
			for j := range n {
				if edge[i][j] {
					units[i].Deps = append(units[i].Deps, fmt.Sprintf("u%d", j))
				}
			}
		}
		first := rewrite(t, base, "go", units...)

		rng := rand.New(rand.NewPCG(uint64(len(data)), uint64(data[0])))
		shuffled := slices.Clone(units)
		rng.Shuffle(n, func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		for i := range shuffled {
			shuffled[i].Deps = slices.Clone(shuffled[i].Deps)
			slices.Reverse(shuffled[i].Deps)
		}
		if again := rewrite(t, base, "go", shuffled...); again != first {
			t.Fatalf("order changed the output:\n%s", Diff("first", "again", []byte(first), []byte(again)))
		}

		p, err := plan("go", units)
		if err != nil {
			t.Fatal(err)
		}
		r := reach(n, edge)
		for _, u := range p {
			i, _ := strconv.Atoi(strings.TrimPrefix(u.name, "go.u"))
			var want []string
			for j := range n {
				if r[i][j] {
					want = append(want, fmt.Sprintf("go.u%d", j))
				}
			}
			slices.Sort(want)
			if !slices.Equal(u.use, want) {
				t.Fatalf("%s uses %v, want %v", u.name, u.use, want)
			}
		}
	})
}

// FuzzRewrite: any file either fails or gives a valid TestSet that a second
// run leaves unchanged; nothing panics.
func FuzzRewrite(f *testing.F) {
	f.Add([]byte(handWritten))
	f.Add([]byte(base))
	f.Add([]byte("# only a comment\n"))
	f.Add([]byte(strings.ReplaceAll(handWritten, "\n", "\r\n")))
	f.Add([]byte(strings.ReplaceAll(base, "\n", "\r") + "tests:\r- metadata: {name: \"go.x\u2028\"}\n"))
	f.Add([]byte(base + "tests:\n  -\n    metadata: {name: go.x}\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		out, err := Rewrite(data, "go", []Unit{unit("a", "b"), unit("b")}, nil)
		if err != nil {
			return
		}
		if _, err := contract.ParseAll(out); err != nil {
			t.Fatalf("invalid output: %v", err)
		}
		again, err := Rewrite(out, "go", []Unit{unit("b"), unit("a", "b")}, nil)
		if err != nil || string(again) != string(out) {
			t.Fatalf("second run: %v\n%s", err, Diff("first", "second", out, again))
		}
	})
}

// A unit whose directory or file name holds a glob metacharacter still has
// its own files in scope: changing one changes its test's fingerprint, and
// a sibling the raw name would match as a glob stays out of it.
func TestFalseHitGlobMetacharacters(t *testing.T) {
	for _, c := range []string{"[", "]", "*", "?", "{", "}", "[x]", "{a,b}", "**"} {
		t.Run(c, func(t *testing.T) {
			dir, file := "pkg/a"+c+"b", "lib/f"+c+".go"
			own, err := Under(dir, "**")
			if err != nil {
				t.Fatal(err)
			}
			lit, err := Literal(file)
			if err != nil {
				t.Fatal(err)
			}
			u := Unit{ID: "a", Source: []string{own, lit}, Command: []string{"test"}}
			set := rewrite(t, base, "go", u)
			files := map[string]string{"go.mod": "m", dir + "/x.go": "x", file: "f", "pkg/axb/x.go": "s", "lib/fx.go": "s"}
			before := digest(t, set, files, "go.a")
			for _, p := range []string{dir + "/x.go", file} {
				changed := maps.Clone(files)
				changed[p] = "changed"
				if digest(t, set, changed, "go.a") == before {
					t.Errorf("go.a reused after %s changed", p)
				}
			}
			for _, p := range []string{"pkg/axb/x.go", "lib/fx.go"} {
				changed := maps.Clone(files)
				changed[p] = "changed"
				if digest(t, set, changed, "go.a") != before {
					t.Errorf("go.a changed after the sibling %s changed", p)
				}
			}
		})
	}
}

// A path no contract glob can express fails closed, naming the path.
func TestFalseHitInexpressiblePath(t *testing.T) {
	for _, p := range []string{`pkg/a\b`, `a\`, "a//b", "a/./b", "../a", "/abs", ""} {
		if g, err := Literal(p); err == nil || !strings.Contains(err.Error(), strconv.Quote(p)) {
			t.Errorf("Literal(%q) = %q, %v; want an error naming the path", p, g, err)
		}
		if g, err := Under(p, "**"); err == nil {
			t.Errorf("Under(%q) = %q, want an error", p, g)
		}
	}
	if g, err := Under(".", "**"); err != nil || g != "**" {
		t.Errorf(`Under(".") = %q, %v; want "**"`, g, err)
	}
}

// A unit without a test keeps its fragment: a change to its files still
// re-runs the tests of the units that depend on it.
func TestFalseHitNoTestKeepsScope(t *testing.T) {
	out, err := Rewrite([]byte(base), "go", graph, nil, "b")
	if err != nil {
		t.Fatal(err)
	}
	set := string(out)
	if strings.Contains(set, "{name: go.b}") {
		t.Fatal("go.b still has a test")
	}
	files := map[string]string{"go.mod": "module x", "a/a.go": "a", "b/b.go": "b", "c/c.go": "c", "d/d.go": "d"}
	before := digest(t, set, files, "go.a")
	files["b/b.go"] = "b2"
	if digest(t, set, files, "go.a") == before {
		t.Error("go.a reused after the files of go.b, which has no test, changed")
	}
	files["b/b.go"], files["c/c.go"] = "b", "c2"
	if digest(t, set, files, "go.a") == before {
		t.Error("go.a reused after c, reached through go.b, changed")
	}
}

// A change to a file of the shared fragment re-runs every generated test.
func TestFalseHitShared(t *testing.T) {
	set := string(must(Rewrite([]byte(base), "go", graph, testShared)))
	files := map[string]string{"go.mod": "module x", "go.sum": "x v1 h1:a=", "a/a.go": "a", "b/b.go": "b", "c/c.go": "c", "d/d.go": "d"}
	for _, f := range []string{"go.mod", "go.sum"} {
		for _, name := range []string{"go.a", "go.b", "go.c", "go.d"} {
			before := digest(t, set, files, name)
			changed := maps.Clone(files)
			changed[f] += "\n"
			if digest(t, set, changed, name) == before {
				t.Errorf("%s reused after %s changed", name, f)
			}
		}
	}
}

// A file only a unit's test reads (TestSource) re-runs that unit's test and
// not its dependents', which never run it; a file of its Source re-runs
// both. Its fragment is written only while the unit has a test.
func TestFalseHitTestSource(t *testing.T) {
	units := []Unit{unit("a", "b"), unit("b"), unit("c")}
	units[1].Source = append(units[1].Source, "lib/rates.json")
	units[1].TestSource = []string{"fixtures/b.json"}
	set := string(must(Rewrite([]byte(base), "go", units, nil)))
	files := map[string]string{"go.mod": "module x", "a/a.go": "a", "b/b.go": "b", "c/c.go": "c", "fixtures/b.json": "{}", "lib/rates.json": "{}"}
	for _, tc := range []struct {
		file, test string
		changes    bool
	}{
		{"fixtures/b.json", "go.b", true},
		{"fixtures/b.json", "go.a", false}, // a dependent never runs b's tests
		{"fixtures/b.json", "go.c", false},
		{"lib/rates.json", "go.b", true},
		{"lib/rates.json", "go.a", true}, // a dependent runs b's code
	} {
		before := digest(t, set, files, tc.test)
		changed := maps.Clone(files)
		changed[tc.file] += "\n"
		if got := digest(t, set, changed, tc.test) != before; got != tc.changes {
			t.Errorf("%s changed: fingerprint of %s changed = %v, want %v", tc.file, tc.test, got, tc.changes)
		}
	}
	if !strings.Contains(set, "go.b.test:") {
		t.Errorf("no go.b.test fragment:\n%s", set)
	}
	if set := string(must(Rewrite([]byte(base), "go", units, nil, "b"))); strings.Contains(set, "go.b.test") {
		t.Errorf("go.b has no test but go.b.test is written:\n%s", set)
	}
}

// A profile directory excluded in defaults is accepted as an output next to
// a ** unit, and then a profile never enters any test's fingerprint: an
// output cannot make a test reused or re-run. Without the exclude, gen
// refuses it, since the ** test would hash it.
func TestFalseHitExcludedOutput(t *testing.T) {
	all := Unit{ID: "r", Source: []string{"**"}, Command: []string{"go", "test", "./..."}}
	units := []Unit{all, withOutputs(unit("a"), "cover/go.a.out")}
	excl := strings.Replace(base, "dependencies: [go.mod]}", "dependencies: [go.mod], exclude: [cover/**]}", 1)
	set := rewrite(t, excl, "go", units...)
	files := map[string]string{"go.mod": "module x", "a/a.go": "a", "r/r.go": "r"}
	for _, name := range []string{"go.a", "go.r"} {
		before := digest(t, set, files, name)
		files["cover/go.a.out"] = "mode: set\n" + name
		if digest(t, set, files, name) != before {
			t.Errorf("%s: the excluded profile changed the fingerprint", name)
		}
		delete(files, "cover/go.a.out")
	}
	if _, err := Rewrite([]byte(base), "go", units, nil); err == nil {
		t.Error("a profile in the ** scope was accepted")
	}
}
