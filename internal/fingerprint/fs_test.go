package fingerprint

import (
	"io/fs"
	"os"
	"path/filepath"
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
		c, err := contract.Load(filepath.Join(dir, name))
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
