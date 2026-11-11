package fingerprint

import (
	"context"
	"errors"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/contract"
)

// testsetGolden pins the manifests of testdata/testset/aval.yaml for the
// current manifest version; goldens of older versions stay as a record.
var testsetGolden = filepath.Join(filepath.Dir(goldenFile(Version)), "testset-"+filepath.Base(goldenFile(Version)))

// setFingerprints expands the TestSet at file, a path inside dir, and
// fingerprints each test, keyed by name.
func setFingerprints(t *testing.T, dir, file string) ([]string, map[string]Fingerprint) {
	t.Helper()
	cs, err := contract.LoadAll(filepath.Join(dir, file))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	fps := map[string]Fingerprint{}
	for _, c := range cs {
		fp, err := Compute(context.Background(), filepath.Dir(filepath.Join(dir, file)), c, noEnv)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, c.Metadata.Name)
		fps[c.Metadata.Name] = fp
	}
	return names, fps
}

// TestTestSetGolden: each test of a TestSet fingerprints byte for byte like
// the same test written as a standalone kind: Test, and the manifests are
// pinned for the current version.
func TestTestSetGolden(t *testing.T) {
	dir := filepath.Join("testdata", "testset")
	names, fps := setFingerprints(t, dir, "aval.yaml")
	var all []Fingerprint
	for _, name := range names {
		standalone, err := computeFile(dir, "standalone-"+name+".yaml")
		if err != nil {
			t.Fatal(err)
		}
		if fps[name].Manifest != standalone.Manifest {
			t.Errorf("%s: the TestSet test and its standalone contract differ:\n--- set\n%s--- standalone\n%s", name, fps[name].Manifest, standalone.Manifest)
		}
		all = append(all, fps[name])
	}
	golden, err := os.ReadFile(testsetGolden)
	if errors.Is(err, fs.ErrNotExist) && *update {
		if err := os.WriteFile(testsetGolden, []byte(concat(all)), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if p := goldenProblem(Version, all, golden); err != nil || p != "" {
		t.Fatalf("%v %s", err, p)
	}
}

// permSet is a TestSet with one test that uses every fragment in use.
func permSet(use []string, frags string) string {
	return "apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults: {scope: {source: [VERSION]}, cacheability: hermetic}\n" +
		"fragments:\n" + frags +
		"tests:\n  - metadata: {name: t}\n    runner: {command: [x]}\n    use: [" + strings.Join(use, ", ") + "]\n"
}

const permFrags = `  db: {scope: {source: ["db/**"]}, environment: {toolchain: {postgres: "16"}, env: [DB_URL]}}
  web: {scope: {source: ["web/**"], dependencies: [go.sum]}}
  src: {scope: {source: ["src/**", "db/**"], exclude: []}, environment: {env: [CI]}}
  go: {environment: {toolchain: {go: "1.25"}, probe: {go: [go, version]}}}
`

// setFingerprint fingerprints the one test of the TestSet doc over the
// fixture tree.
func setFingerprint(t *testing.T, doc string) Fingerprint {
	t.Helper()
	dir := writeTree(t, map[string]string{
		"aval.yaml": doc, "VERSION": "1\n", "go.sum": "s\n", "db/a.sql": "a\n", "web/i.html": "i\n", "src/a.go": "package a\n",
	})
	_, fps := setFingerprints(t, dir, "aval.yaml")
	if len(fps) != 1 {
		t.Fatalf("want one test, got %d", len(fps))
	}
	for _, fp := range fps {
		return fp
	}
	return Fingerprint{}
}

// Property: permuting use never changes the fingerprint.
func TestTestSetUseOrderProperty(t *testing.T) {
	use := []string{"db", "web", "src", "go"}
	want := setFingerprint(t, permSet(use, permFrags)).Digest
	r := rand.New(rand.NewPCG(206, 14))
	for range 20 {
		r.Shuffle(len(use), func(i, j int) { use[i], use[j] = use[j], use[i] })
		if got := setFingerprint(t, permSet(use, permFrags)).Digest; got != want {
			t.Fatalf("use %v changed the fingerprint", use)
		}
	}
}

// False hits: each change to what a test inherits changes its fingerprint.
func TestTestSetFalseHits(t *testing.T) {
	all := []string{"db", "web", "src", "go"}
	base := setFingerprint(t, permSet(all, permFrags))
	for name, doc := range map[string]string{
		"a test drops a fragment": permSet([]string{"db", "web", "src"}, permFrags[:strings.Index(permFrags, "  go:")]),
		"a fragment changes":      permSet(all, strings.Replace(permFrags, `postgres: "16"`, `postgres: "17"`, 1)),
		"a fragment grows":        permSet(all, strings.Replace(permFrags, `[DB_URL]`, `[DB_URL, DB_USER]`, 1)),
	} {
		cs, err := contract.ParseAll([]byte(doc))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		dir := writeTree(t, map[string]string{"VERSION": "1\n", "go.sum": "s\n", "db/a.sql": "a\n", "web/i.html": "i\n", "src/a.go": "package a\n"})
		fp, err := Compute(context.Background(), dir, cs[0], noEnv)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if fp.Digest == base.Digest {
			t.Errorf("%s: fingerprint unchanged (false hit)", name)
		}
	}
}

// False hits: tests a TestSet expands to never share a fingerprint, even
// with the same scope; and a test cannot shrink what defaults declare.
func TestTestSetDistinctTests(t *testing.T) {
	doc := `apiVersion: aval.trianalab.io/v1
kind: TestSet
defaults:
  scope: {source: [VERSION, "src/**"], dependencies: [go.sum]}
  environment: {env: [CI]}
  cacheability: hermetic
tests:
  - {metadata: {name: "shard-{i}"}, runner: {command: [x, "{i}"]}, matrix: {i: ["1", "2"]}}
  - {metadata: {name: same-a}, runner: {command: [x]}}
  - {metadata: {name: same-b}, runner: {command: [x]}}
  - {metadata: {name: narrow}, runner: {command: [x]}, scope: {source: [VERSION], dependencies: []}, environment: {env: []}}
`
	dir := writeTree(t, map[string]string{"aval.yaml": doc, "VERSION": "1\n", "go.sum": "s\n", "src/a.go": "package a\n"})
	_, fps := setFingerprints(t, dir, "aval.yaml")
	seen := map[string]string{}
	for name, fp := range fps {
		if other, dup := seen[fp.Digest]; dup {
			t.Errorf("%s and %s share a fingerprint (false hit)", name, other)
		}
		seen[fp.Digest] = name
	}
	// narrow still hashes src/a.go, go.sum and CI from defaults.
	for _, line := range []string{"source ", "src/a.go", "dependency ", "go.sum", "env CI unset"} {
		if !strings.Contains(fps["narrow"].Manifest, line) {
			t.Errorf("narrow dropped an inherited input (%q):\n%s", line, fps["narrow"].Manifest)
		}
	}
}

// platformSet is the per-platform matrix of #255, as the shards guide
// shows it.
var platformSet = filepath.Join("testdata", "shards", "platforms.yaml")

// False hit: each matrix leg hashes its own toolchain value, so a result
// recorded for unit-darwin is a miss for unit-linux (#255).
func TestTestSetMatrixToolchain(t *testing.T) {
	dir := writeTree(t, map[string]string{"a.go": "package a\n", "go.mod": "module a\n"})
	cs, err := contract.LoadAll(platformSet)
	if err != nil {
		t.Fatal(err)
	}
	fps := map[string]Fingerprint{}
	for _, c := range cs {
		if fps[c.Metadata.Name], err = Compute(context.Background(), dir, c, noEnv); err != nil {
			t.Fatal(err)
		}
	}
	linux, darwin := fps["unit-linux"], fps["unit-darwin"]
	if linux.Digest == darwin.Digest {
		t.Fatal("unit-linux and unit-darwin share a fingerprint (false hit)")
	}
	for fp, line := range map[*Fingerprint]string{&linux: "toolchain platform=linux\n", &darwin: "toolchain platform=darwin\n"} {
		if !strings.Contains(fp.Manifest, line) {
			t.Errorf("manifest lacks %q:\n%s", line, fp.Manifest)
		}
	}
}

// Paths and scope.root resolve from the TestSet file's directory, as for a
// standalone contract next to it.
func TestTestSetRoot(t *testing.T) {
	set := "apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults: {scope: {root: .., source: [\"src/**\"]}, cacheability: hermetic}\n" +
		"tests:\n  - {metadata: {name: unit}, runner: {command: [x]}}\n"
	root := writeTree(t, map[string]string{
		".git/HEAD": "ref: refs/heads/main\n", "src/a.go": "package a\n", "aval/set.yaml": set,
		"aval/unit.yaml": strings.Replace(avalYAML("unit", "src/**", nil, "root: .."), "[make, test]", "[x]", 1),
	})
	_, fps := setFingerprints(t, root, "aval/set.yaml")
	standalone, err := computeFile(root, "aval/unit.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if fps["unit"].Manifest != standalone.Manifest {
		t.Fatalf("set and standalone differ:\n%s\n%s", fps["unit"].Manifest, standalone.Manifest)
	}
}

// scope.contracts takes a kind: Test only: a TestSet is an invalid contract
// (exit 2), never a silent miss.
func TestContractsRejectTestSet(t *testing.T) {
	files := monorepoFiles()
	files["backend/aval.yaml"] = "apiVersion: aval.trianalab.io/v1\nkind: TestSet\ntests: []\n"
	_, err := computeFile(writeTree(t, files), "frontend/aval.yaml")
	if !errors.Is(err, ErrContract) || !strings.Contains(err.Error(), "backend/aval.yaml is a kind: TestSet; scope.contracts takes a kind: Test") {
		t.Fatalf("got %v", err)
	}
}

// shardSet is the sharding TestSet of #259 with the matrix line m and the
// shard argument arg.
func shardSet(m, arg string) string {
	return "apiVersion: aval.trianalab.io/v1\nkind: TestSet\ntests:\n" +
		"  - metadata: {name: \"browser-{shard}\"}\n" +
		"    runner: {command: [make, test-browser, \"PW_SHARD=" + arg + "\"]}\n" +
		"    matrix: " + m + "\n    scope: {source: [VERSION]}\n    cacheability: hermetic\n"
}

func shardFingerprints(t *testing.T, doc string) map[string]Fingerprint {
	t.Helper()
	_, fps := setFingerprints(t, writeTree(t, map[string]string{"aval.yaml": doc, "VERSION": "1\n"}), "aval.yaml")
	return fps
}

// {count: 3} with {shard}/{shard_count} fingerprints byte for byte like the
// list ["1","2","3"] with {shard}/3, which is what existing results were
// recorded with (#259).
func TestTestSetMatrixCountSameFingerprints(t *testing.T) {
	count := shardFingerprints(t, shardSet("{shard: {count: 3}}", "{shard}/{shard_count}"))
	list := shardFingerprints(t, shardSet(`{shard: ["1", "2", "3"]}`, "{shard}/3"))
	if len(count) != 3 || len(list) != 3 {
		t.Fatalf("got %d and %d tests", len(count), len(list))
	}
	for name, fp := range list {
		if count[name].Manifest != fp.Manifest {
			t.Errorf("%s: count form differs from the list form:\n%s\n%s", name, count[name].Manifest, fp.Manifest)
		}
	}
}

// False hit: changing N changes the argv, so the contract hash and the
// fingerprint, of every test that references {shard_count}; a result of
// shard 1 of 2 is a miss for shard 1 of 3.
func TestTestSetMatrixCountFalseHit(t *testing.T) {
	two := shardFingerprints(t, shardSet("{shard: {count: 2}}", "{shard}/{shard_count}"))
	three := shardFingerprints(t, shardSet("{shard: {count: 3}}", "{shard}/{shard_count}"))
	for _, name := range []string{"browser-1", "browser-2"} {
		if two[name].Digest == three[name].Digest {
			t.Errorf("%s: N=2 and N=3 share a fingerprint (false hit)", name)
		}
	}
}
