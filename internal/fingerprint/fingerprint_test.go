package fingerprint

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/contract"
)

func testContract() *contract.Contract {
	return &contract.Contract{
		APIVersion: contract.APIVersion,
		Kind:       contract.Kind,
		Metadata:   contract.Metadata{Name: "it"},
		Runner:     contract.Runner{Command: []string{"go", "test", "./..."}},
		Scope: contract.Scope{
			Source:       []string{"**/*.go", "compose.yaml"},
			Exclude:      []string{"vendor/**"},
			Dependencies: []string{"go.sum"},
		},
		Environment: contract.Environment{
			Toolchain: map[string]string{"go": "1.25", "docker": "27"},
			Env:       []string{"DB_IMAGE", "MODE"},
		},
		Cacheability: contract.ControlledEnvironment,
	}
}

var baseFiles = map[string]string{
	"main.go":       "package main\n",
	"pkg/a/a.go":    "package a\n",
	"compose.yaml":  "services: {}\n",
	"go.sum":        "example.com/x v1.0.0 h1:abc\n",
	"README.md":     "not in scope\n",
	"vendor/v/v.go": "package v\n",
}

var fixedEnv = Env{"DB_IMAGE": "postgres:16"}

func TestHexStripsAlgorithm(t *testing.T) {
	if got := (Fingerprint{Digest: "sha256:abc"}).Hex(); got != "abc" {
		t.Fatalf("Hex() = %q", got)
	}
}

func TestNilEnvUsesProcessEnvironment(t *testing.T) {
	f := newFixture(t)
	t.Setenv("DB_IMAGE", "postgres:16")
	t.Setenv("MODE", "") // restores MODE after the test
	if err := os.Unsetenv("MODE"); err != nil {
		t.Fatal(err)
	}
	got, err := Compute(context.Background(), f.dir, f.c, nil)
	want, err2 := f.compute()
	if err != nil || err2 != nil {
		t.Fatal(err, err2)
	}
	if got.Digest != want.Digest {
		t.Fatal("nil env must resolve variables from the process environment")
	}
}

func TestUnreadableInputsFailClosed(t *testing.T) {
	if _, err := Compute(context.Background(), filepath.Join(t.TempDir(), "missing"), testContract(), fixedEnv); err == nil {
		t.Error("missing contract root must fail")
	}
	if _, _, err := (&hasher{buf: make([]byte, 512)}).hashFile(os.DirFS(t.TempDir()), "gone.go"); err == nil {
		t.Error("file removed after listing must fail")
	}
	f := newFixture(t)
	f.chmod("main.go", 0)
	if _, err := f.compute(); err == nil {
		t.Error("unreadable file in scope must fail")
	}
	// An unreadable directory could hide scoped files: the walk must fail,
	// not skip it.
	f = newFixture(t)
	f.chmod("pkg", 0)
	t.Cleanup(func() { _ = os.Chmod(f.path("pkg"), 0o755) })
	if _, err := f.compute(); err == nil {
		t.Error("unreadable directory must fail")
	}
}

// Audit F3: the walk only enters directories an include glob can match, so
// an unreadable directory outside the scope (a root-owned volume, a cache)
// does not break the fingerprint, and does not change it.
func TestWalkSkipsDirectoriesOutsideScope(t *testing.T) {
	f := newFixture(t)
	f.c.Scope = contract.Scope{Source: []string{"pkg/**"}, Dependencies: []string{"go.sum"}}
	want, err := f.compute()
	if err != nil {
		t.Fatal(err)
	}
	f.write("pgdata/base/1", "row")
	f.chmod("pgdata", 0)
	t.Cleanup(func() { _ = os.Chmod(f.path("pgdata"), 0o755) })
	got, err := f.compute()
	if err != nil {
		t.Fatalf("unreadable directory outside the scope: %v", err)
	}
	if got.Manifest != want.Manifest {
		t.Errorf("manifest changed:\n%s\nwant\n%s", got.Manifest, want.Manifest)
	}
	// The same directory inside the scope still fails closed.
	f.c.Scope.Source = append(f.c.Scope.Source, "pgdata/**")
	if _, err := f.compute(); err == nil {
		t.Error("unreadable directory in scope must fail")
	}
}

// A contract directory that is itself a symlink fingerprints like its
// target: only symlinks inside the root are rejected (#111).
func TestSymlinkedRootFingerprintsLikeTarget(t *testing.T) {
	f := newFixture(t)
	want, err := f.compute()
	if err != nil {
		t.Fatal(err)
	}
	f.symlink(f.dir, "../"+filepath.Base(f.dir)+"-link")
	link := f.dir + "-link"
	t.Cleanup(func() { _ = os.Remove(link) })
	f.dir = link
	got, err := f.compute()
	if err != nil {
		t.Fatal(err)
	}
	if got.Manifest != want.Manifest {
		t.Errorf("manifest changed:\n%s\nwant\n%s", got.Manifest, want.Manifest)
	}
}

// canonicalContract discards the json.Marshal error; guard against it ever
// collapsing to empty or identical bytes for distinct contracts.
func TestCanonicalContractNonEmptyAndDistinct(t *testing.T) {
	a, b := testContract(), testContract()
	b.Metadata.Name = a.Metadata.Name + "-other"
	ca, cb := canonicalContract(a), canonicalContract(b)
	if len(ca) == 0 {
		t.Fatal("canonical contract bytes must not be empty")
	}
	if bytes.Equal(ca, cb) {
		t.Error("contracts differing only in name must give different canonical bytes")
	}
}

// An optional source glob may match nothing; the digest then depends only on
// whether a matching file exists, so adding one misses and removing it
// returns to the digest computed before it existed (#257).
func TestOptionalSource(t *testing.T) {
	f := newFixture(t)
	f.c.Scope.OptionalSource = []string{"**/*testdata*.yaml", "docs/**"}
	f.write("docs/a.md", "a\n")
	base, err := f.compute()
	if err != nil {
		t.Fatalf("optional glob that matches nothing: %v", err)
	}
	if want := []string{"**/*testdata*.yaml"}; !slices.Equal(base.UnmatchedOptional, want) {
		t.Fatalf("UnmatchedOptional = %q, want %q", base.UnmatchedOptional, want)
	}
	f.write("pkg/a/a_testdata.yaml", "k: v\n")
	added, err := f.compute()
	if err != nil {
		t.Fatal(err)
	}
	if added.Digest == base.Digest || !strings.Contains(added.Manifest, " - pkg/a/a_testdata.yaml\n") || added.UnmatchedOptional != nil {
		t.Fatalf("added file not hashed as a source:\n%s", added.Manifest)
	}
	f.remove("pkg/a/a_testdata.yaml")
	if back, err := f.compute(); err != nil || back.Digest != base.Digest {
		t.Fatalf("after removing the file: %v, digest %s, want %s", err, back.Digest, base.Digest)
	}
}

// optionalSource is exempt from the matched-no-files rule only: a required
// glob that matches nothing, or a scope whose source and optionalSource
// together hash no file, still fail.
func TestOptionalSourceFailsClosed(t *testing.T) {
	for name, scope := range map[string]contract.Scope{
		"required glob unmatched":        {Source: []string{"main.go", "missing/**"}, OptionalSource: []string{"pkg/**"}},
		"union hashes no file":           {Source: []string{"vendor/**"}, OptionalSource: []string{"none/**"}, Exclude: []string{"vendor/**"}},
		"optional matches excluded only": {Source: []string{"vendor/**"}, OptionalSource: []string{"vendor/v/*.go"}, Exclude: []string{"vendor/**"}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.c.Scope = scope
			if fp, err := f.compute(); err == nil {
				t.Fatalf("want an error, got %s", fp.Digest)
			}
		})
	}
}

// TestEnvLines: a prefix pattern hashes every set variable it matches,
// one line each, merged with exact names and sorted (#253).
func TestEnvLines(t *testing.T) {
	h := func(v string) string { return sum([]byte(v)) }
	env := Env{"APP_A": "1", "APP_B": "2", "OTHER": "3", "APPX": "4", "app_c": "5", contract.RunStackEnv: "/outer/aval.yaml"}
	want := []string{"env APP_A " + h("1"), "env APP_B " + h("2")}
	for _, entries := range [][]string{{"APP_*"}, {"APP_*", "APP_A"}, {"APP_A", "APP_*", "APP_*"}} {
		if got := envLines(entries, env); !slices.Equal(got, want) {
			t.Errorf("envLines(%q) = %q, want %q", entries, got, want)
		}
	}
	if got := envLines([]string{"NOPE_*"}, env); got != nil {
		t.Errorf("pattern matching nothing: %q", got)
	}
	if got := envLines([]string{"NOPE"}, env); !slices.Equal(got, []string{"env NOPE unset"}) {
		t.Errorf("unset exact name: %q", got)
	}
	// No pattern contract validation accepts can name the run stack, and
	// none other matches it.
	for _, p := range []string{"APP_*", "OTHER*", "P*"} {
		for _, l := range envLines([]string{p}, env) {
			if strings.Contains(l, contract.RunStackEnv) {
				t.Errorf("%s hashed %s: %q", p, contract.RunStackEnv, l)
			}
		}
	}
}

// TestEnvLinesCase: names compare case-sensitively on POSIX and
// case-insensitively, upper-cased, on Windows, where an exact name keeps
// the spelling of the contract.
func TestEnvLinesCase(t *testing.T) {
	if foldEnv != (runtime.GOOS == "windows") {
		t.Fatalf("foldEnv = %v on %s", foldEnv, runtime.GOOS)
	}
	h := func(v string) string { return sum([]byte(v)) }
	env := Env{"app_a": "1", "App_B": "2", "db_image": "3"}
	for fold, want := range map[bool][]string{
		false: {"env DB_IMAGE unset"},
		true:  {"env APP_A " + h("1"), "env APP_B " + h("2"), "env DB_IMAGE " + h("3")},
	} {
		t.Run(map[bool]string{false: "posix", true: "windows"}[fold], func(t *testing.T) {
			defer func(old bool) { foldEnv = old }(foldEnv)
			foldEnv = fold
			if got := envLines([]string{"APP_*", "DB_IMAGE"}, env); !slices.Equal(got, want) {
				t.Errorf("envLines = %q, want %q", got, want)
			}
		})
	}
}
