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
		deps, exclude []string
		want          bool
	}{
		{"name and contents excluded by one glob", nil, []string{"pkg/**"}, true},
		{"name and contents excluded by two globs", nil, []string{"pkg/bin", "pkg/bin/**"}, true},
		{"nothing excluded", nil, nil, false},
		{"name excluded, contents not", nil, []string{"pkg/bin"}, false},
		{"contents excluded only by the zero-segment form", nil, []string{"pkg/bin/**"}, false},
		{"contents glob does not match the parent", nil, []string{"pkg/bin", "pkg/*/x/**"}, false},
		{"dependency glob matches the name", []string{"pkg/bin"}, []string{"pkg/bin", "pkg/bin/**"}, false},
		{"dependency glob reaches under it", []string{"pkg/**/*.so"}, []string{"pkg/bin", "pkg/bin/**"}, false},
		{"dependency glob elsewhere", []string{"go.sum"}, []string{"pkg/bin", "pkg/bin/**"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := excludedSymlink([]string{"pkg", "bin"}, splitGlobs(tc.deps), splitGlobs(tc.exclude))
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
