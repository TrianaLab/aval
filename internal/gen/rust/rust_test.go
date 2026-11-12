package rust

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/fingerprint"
	"github.com/TrianaLab/aval/internal/gen"
)

// The test binary doubles as a fake cargo: with AVAL_FAKE_CARGO set to a
// recorded cargo metadata file, it prints that file with @ROOT@ replaced by
// its working directory, as cargo would; with "fail" it fails as cargo does.
func TestMain(m *testing.M) {
	if src := os.Getenv("AVAL_FAKE_CARGO"); src != "" {
		if src == "fail" {
			fmt.Fprintln(os.Stderr, "error: could not find `Cargo.toml`")
			os.Exit(101)
		}
		data, _ := os.ReadFile(src)
		wd, _ := os.Getwd()
		wd, _ = filepath.EvalSymlinks(wd) // cargo prints canonical paths
		root, _ := json.Marshal(wd)
		fmt.Print(strings.ReplaceAll(string(data), "@ROOT@", string(root[1:len(root)-1])))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const fixture = "testdata/workspace"

// fake makes Units run the test binary with metadata as cargo's output.
func fake(t *testing.T, metadata string) {
	t.Helper()
	if metadata != "fail" {
		abs, err := filepath.Abs(metadata)
		if err != nil {
			t.Fatal(err)
		}
		metadata = abs
	}
	t.Setenv("AVAL_FAKE_CARGO", metadata)
	old := cargo
	cargo = os.Args[0]
	t.Cleanup(func() { cargo = old })
}

// generate runs the adapter on root and rewrites root/aval.yaml.
func generate(t *testing.T, root string) string {
	t.Helper()
	units, err := Adapter{}.Units(root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "aval.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := gen.Rewrite(data, "rust", units)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func golden(t *testing.T, got string) {
	t.Helper()
	want, err := os.ReadFile("testdata/aval.golden.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("output differs from testdata/aval.golden.yaml:\n%s", gen.Diff("golden", "got", want, []byte(got)))
	}
}

func TestGolden(t *testing.T) {
	fake(t, "testdata/metadata.json")
	golden(t, generate(t, fixture))
}

// TestGoldenCargo runs the real cargo on the fixture when it is installed.
func TestGoldenCargo(t *testing.T) {
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo not installed")
	}
	golden(t, generate(t, fixture))
}

// TestSymlinkRoot reaches the fixture through a symlinked working directory,
// as a clone under macOS /tmp does: cargo prints canonical paths, which must
// still be inside the root. A member really outside it still fails.
func TestSymlinkRoot(t *testing.T) {
	recorded, err := os.ReadFile("testdata/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(strings.Replace(string(recorded), `"manifest_path": "@ROOT@/apps/cli/Cargo.toml"`, `"manifest_path": "@ROOT@/../cli/Cargo.toml"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	pkg, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(pkg, link); err != nil {
		t.Fatal(err)
	}
	fake(t, "testdata/metadata.json")
	t.Chdir(link)
	for _, root := range []string{fixture, filepath.Join(link, fixture)} {
		golden(t, generate(t, root))
	}
	fake(t, outside)
	if units, err := (Adapter{}).Units(fixture); err == nil || !strings.Contains(err.Error(), "member cli: ") {
		t.Errorf("Units: %v, %v; want member cli outside the root", units, err)
	}
}

func TestErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	recorded, err := os.ReadFile("testdata/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	outside := write("outside.json", strings.Replace(string(recorded), `"manifest_path": "@ROOT@/apps/cli/Cargo.toml"`, `"manifest_path": "@ROOT@/../cli/Cargo.toml"`, 1))
	backslash := write("backslash.json", strings.Replace(string(recorded), `"manifest_path": "@ROOT@/apps/cli/Cargo.toml"`, `"manifest_path": "@ROOT@/apps/c\\li/Cargo.toml"`, 1))
	nonMember := write("nonmember.json", strings.Replace(string(recorded), `"path": "@ROOT@/crates/domain"`, `"path": "@ROOT@/vendor/domain"`, 1))
	for _, tc := range []struct {
		name, metadata, root, want string
	}{
		{"missing tool", "", fixture, "cargo metadata: exec:"},
		{"cargo fails", "fail", fixture, "could not find `Cargo.toml`"},
		{"bad json", write("bad.json", "{"), fixture, "cargo metadata: unexpected end of JSON"},
		{"no root", "", filepath.Join(dir, "none"), "no such file"},
		{"member outside root", outside, fixture, "member cli: "},
		{"path dependency not a member", nonMember, fixture, "path dependency domain at "},
		{"inexpressible member path", backslash, fixture, `member cli: "apps/c\\li": a contract glob cannot express`},
		{"no workspace root", write("nows.json", `{"packages": []}`), fixture, "no workspace_root"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.metadata == "" {
				old := cargo
				cargo = "aval-no-such-cargo"
				t.Cleanup(func() { cargo = old })
			} else {
				fake(t, tc.metadata)
			}
			units, err := Adapter{}.Units(tc.root)
			if err == nil || !strings.Contains(err.Error(), tc.want) || units != nil {
				t.Errorf("Units: %v, %v; want an error with %q", units, err, tc.want)
			}
		})
	}
}

// digest fingerprints test name of set over the fixture files, with edits.
func digest(t *testing.T, set, name string, edits map[string]string) string {
	t.Helper()
	fsys := fstest.MapFS{}
	err := filepath.WalkDir(fixture, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(fixture, p)
		fsys[filepath.ToSlash(rel)] = &fstest.MapFile{Data: data}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for p, data := range edits {
		fsys[p] = &fstest.MapFile{Data: []byte(data)}
	}
	cs, err := contract.ParseAll([]byte(set))
	if err != nil {
		t.Fatal(err)
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

// cli -> http -> store -> domain: a change to domain reaches the cli test;
// a change to cli does not reach store.
func TestFalseHitTransitiveDependency(t *testing.T) {
	fake(t, "testdata/metadata.json")
	set := generate(t, fixture)
	for _, tc := range []struct {
		test, file string
		changes    bool
	}{
		{"rust.cli", "crates/domain/src/lib.rs", true},
		{"rust.cli", "crates/domain/Cargo.toml", true},
		{"rust.http", "crates/testkit/src/lib.rs", true}, // a dev-dependency
		{"rust.store", "apps/cli/src/main.rs", false},
		{"rust.domain", "crates/testkit/src/lib.rs", false},
		{"rust.store", "crates/http/tests/handle.rs", false},
	} {
		before := digest(t, set, tc.test, nil)
		after := digest(t, set, tc.test, map[string]string{tc.file: "// changed\n"})
		if (before != after) != tc.changes {
			t.Errorf("%s after changing %s: fingerprint changed = %v, want %v", tc.test, tc.file, before != after, tc.changes)
		}
	}
}

// A new path dependency between members is drift: the generated file
// differs, and the diff shows the new fragment in the test's use.
func TestFalseHitNewPathDependencyIsDrift(t *testing.T) {
	fake(t, "testdata/metadata.json")
	set := generate(t, fixture)

	recorded, err := os.ReadFile("testdata/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	// domain now depends on testkit.
	var m struct {
		WorkspaceRoot string           `json:"workspace_root"`
		Packages      []map[string]any `json:"packages"`
	}
	if err := json.Unmarshal(recorded, &m); err != nil {
		t.Fatal(err)
	}
	for _, pkg := range m.Packages {
		if pkg["name"] == "domain" {
			pkg["dependencies"] = []any{map[string]any{"name": "testkit", "path": "@ROOT@/crates/testkit"}}
		}
	}
	added, _ := json.Marshal(m)
	p := filepath.Join(t.TempDir(), "metadata.json")
	if err := os.WriteFile(p, added, 0o600); err != nil {
		t.Fatal(err)
	}
	fake(t, p)
	now := generate(t, fixture)
	if d := gen.Diff("aval.yaml", "generated", []byte(set), []byte(now)); !strings.Contains(d, "+      - rust.testkit") {
		t.Errorf("diff does not show rust.testkit joining the tests that reach domain:\n%s", d)
	}
	if digest(t, now, "rust.domain", nil) == digest(t, now, "rust.domain", map[string]string{"crates/testkit/src/lib.rs": "// changed\n"}) {
		t.Error("rust.domain reused after its new dependency testkit changed")
	}
}

// overrides runs Units on a scratch workspace with the recorded metadata
// and files, a map from a path under the root to its content.
func overrides(t *testing.T, files map[string]string) ([]gen.Unit, error) {
	t.Helper()
	fake(t, "testdata/metadata.json")
	root := t.TempDir()
	for name, data := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if strings.HasSuffix(name, "/") { // a directory where a file should be
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Adapter{}.Units(root)
}

// cargo metadata --no-deps does not show path overrides: one outside every
// member fails closed, naming its file; one it cannot read fails closed.
func TestFalseHitPathOverrideOutsideMembers(t *testing.T) {
	for _, tc := range []struct {
		name, file, data, want string
	}{
		{"patch in Cargo.toml", "Cargo.toml", "[workspace]\n[patch.crates-io]\nserde = { path = \"vendor/serde\" }\n", "Cargo.toml: path override vendor/serde is not inside a workspace member"},
		{"quoted patch table", "Cargo.toml", "[ patch . \"https://github.com/a/b\" ]\nb.path = '../b'\n", "path override ../b"},
		{"dotted patch key", "Cargo.toml", "patch.crates-io.serde = { git = \"x\", path = \"/opt/serde\" }\n", "path override /opt/serde"},
		{"patch subtable", "Cargo.toml", "[patch.crates-io.serde]\n\"path\" = \"vendor/serde\"\n", "path override vendor/serde"},
		{"patch in config", ".cargo/config.toml", "[patch.crates-io]\nserde = { path = \"vendor/serde\" }\n", "config.toml: path override vendor/serde"},
		{"legacy config", ".cargo/config", "paths = [\"vendor/serde\"]\n", ".cargo/config: path override vendor/serde"},
		{"multi-line paths", ".cargo/config.toml", "paths = [\n  \"crates/domain\", # a member\n  'vendor/x',\n]\n", "path override vendor/x"},
		{"a sibling is not inside", "Cargo.toml", "[patch.crates-io]\nd = { path = \"crates/domain-fork\" }\n", "path override crates/domain-fork"},
		{"non-string path", "Cargo.toml", "[patch.crates-io]\nserde = { path = 7 }\n", "Cargo.toml:2: cannot read the path override"},
		{"multi-line string", "Cargo.toml", "[patch.crates-io]\nserde = { path = \"\"\"\nvendor\"\"\" }\n", "is not inside a workspace member"},
		{"bad escape", "Cargo.toml", "[patch.crates-io]\nserde = { path = \"\\q\" }\n", "cannot read the path override"},
		{"paths not an array", ".cargo/config.toml", "paths = \"vendor\"\n", "config.toml:1: cannot read"},
		{"bad escape in paths", ".cargo/config.toml", "paths = [\"\\q\"]\n", "cannot read"},
		{"unreadable", ".cargo/config.toml/", "", "config.toml: is a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			units, err := overrides(t, map[string]string{tc.file: tc.data})
			if err == nil || !strings.Contains(err.Error(), tc.want) || units != nil {
				t.Errorf("Units: %v, %v; want an error with %q", units, err, tc.want)
			}
		})
	}
}

// An override inside a member makes every member depend on it, so a change
// there re-runs every test. Comments, strings with # and tables other than
// [patch] are not overrides.
func TestFalseHitPathOverrideInsideMember(t *testing.T) {
	units, err := overrides(t, map[string]string{
		"Cargo.toml": "[workspace] # [patch.crates-io]\n# serde = { path = \"vendor\" }\n[dependencies]\nx = { path = \"vendor/x\" }\n" +
			"[patch.crates-io]\nserde = { path = \"crates/domain/vendor/a#\\\"b\" } # path = 1\n",
		".cargo/config.toml": "paths = ['apps/cli']\n[build]\njobs = 1\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range units {
		if !slices.Contains(u.Deps, "domain") || !slices.Contains(u.Deps, "cli") {
			t.Errorf("unit %s deps %v: want domain and cli", u.ID, u.Deps)
		}
	}
}

// A member directory named with glob metacharacters gets an escaped glob.
func TestMetacharacterMember(t *testing.T) {
	recorded, err := os.ReadFile("testdata/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "metadata.json")
	data := strings.Replace(string(recorded), `"manifest_path": "@ROOT@/apps/cli/Cargo.toml"`, `"manifest_path": "@ROOT@/apps/c[*?]li/Cargo.toml"`, 1)
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	fake(t, p)
	units, err := Adapter{}.Units(fixture)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(units, func(u gen.Unit) bool { return u.ID == "cli" })
	if i < 0 || !slices.Equal(units[i].Source, []string{"apps/c[[][*][?]]li/**"}) {
		t.Errorf("units %+v: want cli with source apps/c[[][*][?]]li/**", units)
	}
}
