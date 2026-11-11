// Package integration holds level 2 tests: real components wired together,
// nothing over a reachable network.
package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/fingerprint"
)

var noEnv = fingerprint.Env{}

// copyTree copies the regular files under src into dst.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func loadAndCompute(t *testing.T, dir string) string {
	t.Helper()
	c, err := contract.Load(filepath.Join(dir, "aval.yaml"), "")
	if err != nil {
		t.Fatal(err)
	}
	fp, err := fingerprint.Compute(context.Background(), dir, c, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	return fp.Digest
}

// The contract loader and the fingerprint engine, run over the real
// go-compose example: the digest depends on content, not on location, and a
// change to a declared input is never a hit.
func TestExampleFingerprintFromLoadedContract(t *testing.T) {
	src := filepath.Join("..", "..", "examples", "go-compose")
	a, b := t.TempDir(), t.TempDir()
	copyTree(t, src, a)
	copyTree(t, src, b)

	if loadAndCompute(t, src) != loadAndCompute(t, a) || loadAndCompute(t, a) != loadAndCompute(t, b) {
		t.Fatal("same example tree in two places produced different fingerprints")
	}

	before := loadAndCompute(t, b)
	f := filepath.Join(b, "counter", "counter.go")
	data, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, append(data, "\n// edited\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	if loadAndCompute(t, b) == before {
		t.Fatal("editing a declared source file kept the fingerprint: false hit")
	}
}
