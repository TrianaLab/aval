package fingerprint

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/TrianaLab/aval/internal/contract"
)

// mapFS copies the tree at dir, file modes included, into memory.
func mapFS(t *testing.T, dir string) fstest.MapFS {
	t.Helper()
	m := fstest.MapFS{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		m[filepath.ToSlash(rel)] = &fstest.MapFile{Data: data, Mode: fi.Mode()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// ComputeFS over an in-memory copy of the golden tree writes the committed
// golden manifests byte for byte: the browser demo fingerprints exactly like
// the CLI.
func TestComputeFSMatchesGolden(t *testing.T) {
	dir := goldenDir(t)
	fsys := mapFS(t, dir)
	var fps []Fingerprint
	for _, name := range goldenContracts {
		c, err := contract.Load(filepath.Join(dir, name), "")
		if err != nil {
			t.Fatal(err)
		}
		fp, err := ComputeFS(fsys, c, goldenEnv)
		if err != nil {
			t.Fatal(err)
		}
		fps = append(fps, fp)
	}
	golden, err := os.ReadFile(goldenFile(Version))
	if err != nil {
		t.Fatal(err)
	}
	if p := goldenProblem(Version, fps, golden); p != "" {
		t.Fatal(p)
	}
}

// skip leaves an Aval output out of the manifest, like an output path does
// in Compute; a symlink in scope fails closed in memory too.
func TestComputeFSSkipAndSymlink(t *testing.T) {
	c := &contract.Contract{Scope: contract.Scope{Source: []string{"*.txt"}}}
	fsys := fstest.MapFS{"a.txt": {Data: []byte("a")}, "out.txt": {Data: []byte("{}")}}
	all, err := ComputeFS(fsys, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	skipped, err := ComputeFS(fsys, c, nil, "out.txt")
	if err != nil || skipped.Digest == all.Digest {
		t.Fatalf("skip had no effect: %v", err)
	}
	fsys["link.txt"] = &fstest.MapFile{Data: []byte("a.txt"), Mode: fs.ModeSymlink}
	if _, err := ComputeFS(fsys, c, nil); err == nil {
		t.Fatal("a symlink in scope must fail closed")
	}
}

// A symlink whose name no glob matches, but under which a glob could match
// files, fails closed in memory too, naming the path.
func TestComputeFSSymlinkedDirectory(t *testing.T) {
	c := &contract.Contract{Scope: contract.Scope{Source: []string{"**/*.txt"}}}
	fsys := fstest.MapFS{"a.txt": {Data: []byte("a")}, "sub": {Data: []byte("/elsewhere"), Mode: fs.ModeSymlink}}
	if _, err := ComputeFS(fsys, c, nil); err == nil || !strings.Contains(err.Error(), `"sub"`) {
		t.Fatalf("want an error naming sub, got %v", err)
	}
}

// The npm ci repro from #198: pkg/** with node_modules excluded fingerprints,
// and nothing under node_modules reaches the manifest.
func TestExcludedSymlinkLeavesNoManifestLine(t *testing.T) {
	f := newFixture(t)
	nodeModulesTool(f)
	fp, err := f.compute()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fp.Manifest, "node_modules") {
		t.Fatalf("node_modules in the manifest:\n%s", fp.Manifest)
	}
}

// Each condition of excludedSymlink on its own.
func TestExcludedSymlink(t *testing.T) {
	for _, tc := range []struct {
		name          string
		toFile        bool
		deps, exclude []string
		want          bool
	}{
		{"name and contents excluded by one glob", false, nil, []string{"pkg/**"}, true},
		{"name and contents excluded by two globs", false, nil, []string{"pkg/bin", "pkg/bin/**"}, true},
		{"nothing excluded", false, nil, nil, false},
		{"name excluded, contents not", false, nil, []string{"pkg/bin"}, false},
		{"contents excluded only by the zero-segment form", false, nil, []string{"pkg/bin/**"}, false},
		{"contents glob does not match the parent", false, nil, []string{"pkg/bin", "pkg/*/x/**"}, false},
		{"dependency glob matches the name", false, []string{"pkg/bin"}, []string{"pkg/bin", "pkg/bin/**"}, false},
		{"dependency glob reaches under it", false, []string{"pkg/**/*.so"}, []string{"pkg/bin", "pkg/bin/**"}, false},
		{"dependency glob elsewhere", false, []string{"go.sum"}, []string{"pkg/bin", "pkg/bin/**"}, true},
		{"file target, name excluded", true, nil, []string{"pkg/bin"}, true},
		{"file target, nothing excluded", true, nil, nil, false},
		{"file target, contents excluded only", true, nil, []string{"pkg/bin/**"}, false},
		{"file target, dependency glob matches the name", true, []string{"pkg/bin"}, []string{"pkg/bin"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := excludedSymlink([]string{"pkg", "bin"}, tc.toFile, splitGlobs(tc.deps), splitGlobs(tc.exclude))
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// The repro from #258: a symlink to a file excluded by name only
// fingerprints like the contract that also excludes <link>/**, except for
// the contract line.
func TestFileSymlinkExcludedByName(t *testing.T) {
	f := newFixture(t)
	fileLink(f)
	byName, err := f.compute()
	if err != nil {
		t.Fatal(err)
	}
	f.c.Scope.Exclude = append(f.c.Scope.Exclude, "tests/fx/link.yaml/**")
	both, err := f.compute()
	if err != nil {
		t.Fatal(err)
	}
	if d := changedSections(byName.Manifest, both.Manifest); len(d) != 2 || d[0] != "contract" || d[1] != "contract" {
		t.Fatalf("want only the contract line to differ, got %v\n--- name\n%s--- both\n%s", d, byName.Manifest, both.Manifest)
	}
	if strings.Contains(byName.Manifest, "link.yaml") || !strings.Contains(byName.Manifest, "tests/fx/real.yaml") {
		t.Fatalf("want real.yaml hashed and link.yaml not:\n%s", byName.Manifest)
	}
}

// A name-excluded link to a file outside the root is only Stat-ed: no byte
// of the target reaches the manifest, whatever it holds.
func TestFileSymlinkOutsideRootNotRead(t *testing.T) {
	f := newFixture(t)
	fileLink(f)
	out := filepath.Join(f.t.TempDir(), "secret.yaml")
	f.do(os.WriteFile(out, []byte("secret\n"), 0o644))
	f.remove("tests/fx/link.yaml")
	f.symlink(out, "tests/fx/link.yaml")
	fp, err := f.compute()
	if err != nil {
		t.Fatal(err)
	}
	f.do(os.WriteFile(out, []byte("other\n"), 0o644))
	again, err := f.compute()
	if err != nil {
		t.Fatal(err)
	}
	if fp.Manifest != again.Manifest || strings.Contains(fp.Manifest, sum([]byte("secret\n"))) || strings.Contains(fp.Manifest, "secret") {
		t.Fatalf("the target reached the manifest:\n%s", fp.Manifest)
	}
}

// The error names the exclude that would fix it: the name alone for a file
// target, the name and its contents for anything else (fail closed).
func TestSymlinkErrorHint(t *testing.T) {
	byName := `exclude it by name ("tests/fx/link.yaml")`
	both := `exclude "tests/fx/link.yaml" and "tests/fx/link.yaml/**"`
	for _, tc := range []struct {
		name   string
		change func(*fixture)
		want   string
	}{
		{"file target not excluded", func(f *fixture) { f.c.Scope.Exclude = nil }, byName},
		{"file target a dependency glob reaches", func(f *fixture) {
			f.c.Scope.Dependencies = append(f.c.Scope.Dependencies, "tests/**")
		}, byName},
		{"directory target", func(f *fixture) {
			f.remove("tests/fx/link.yaml")
			symlinkDir(f, "tests/fx/link.yaml")
		}, both},
		{"dangling", func(f *fixture) { f.remove("tests/fx/real.yaml") }, both},
		{"loop", func(f *fixture) {
			f.remove("tests/fx/link.yaml")
			f.symlink("link.yaml", "tests/fx/link.yaml")
		}, both},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			fileLink(f)
			tc.change(f)
			_, err := f.compute()
			if err == nil || !strings.Contains(err.Error(), `"tests/fx/link.yaml" is a symlink a glob could match files under`) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want the symlink error with %s, got %v", tc.want, err)
			}
		})
	}
}
