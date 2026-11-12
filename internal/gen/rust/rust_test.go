package rust

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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
		Packages []map[string]any `json:"packages"`
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
