package fingerprint

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/TrianaLab/aval/internal/contract"
)

// Story 12.2, shards over examples/uv-monorepo. Pattern A, one contract per
// shard: a file only shard 2 reads changes shard 2 and never shard 1.
// Pattern B, one contract with shard variables: SHARD_INDEX and SHARD_TOTAL
// each change the fingerprint, so two shards never share a result.
func TestShardFingerprints(t *testing.T) {
	example := filepath.Join("..", "..", "examples", "uv-monorepo")
	dir := uvTree(t, example)
	fp := func(file string, env map[string]string) string {
		t.Helper()
		c, err := contract.Load(file, "")
		if err != nil {
			t.Fatal(err)
		}
		got, err := Compute(context.Background(), dir, c, env)
		if err != nil {
			t.Fatal(err)
		}
		return got.Digest
	}

	shard1, shard2 := filepath.Join(example, "aval.shard-1.yaml"), filepath.Join(example, "aval.shard-2.yaml")
	a1, a2 := fp(shard1, nil), fp(shard2, nil)
	app := filepath.Join(dir, "packages", "app", "src", "app", "__init__.py")
	b, err := os.ReadFile(app)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app, append(b, "# read by shard 2 only\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	if fp(shard1, nil) != a1 {
		t.Error("pattern A: a file only shard 2 reads changed shard 1's fingerprint (needless miss)")
	}
	if fp(shard2, nil) == a2 {
		t.Error("pattern A: a change to shard 2's input left its fingerprint unchanged (false hit)")
	}

	patternB := filepath.Join("testdata", "shards", "pattern-b.yaml")
	base := map[string]string{"SHARD_INDEX": "0", "SHARD_TOTAL": "2"}
	want := fp(patternB, base)
	if fp(patternB, map[string]string{"SHARD_INDEX": "0", "SHARD_TOTAL": "2"}) != want {
		t.Error("pattern B: same shard variables gave a different fingerprint (needless miss)")
	}
	for name, env := range map[string]map[string]string{
		"SHARD_INDEX":       {"SHARD_INDEX": "1", "SHARD_TOTAL": "2"},
		"SHARD_TOTAL":       {"SHARD_INDEX": "0", "SHARD_TOTAL": "3"},
		"SHARD_INDEX unset": {"SHARD_TOTAL": "2"},
		"SHARD_TOTAL unset": {"SHARD_INDEX": "0"},
	} {
		if fp(patternB, env) == want {
			t.Errorf("pattern B: %s left the fingerprint unchanged (false hit)", name)
		}
	}
}

// uvTree copies the inputs of examples/uv-monorepo to a temporary directory.
func uvTree(t *testing.T, example string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(filepath.Join(dir, "packages"), os.DirFS(filepath.Join(example, "packages"))); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"pyproject.toml", "uv.lock", ".python-version"} {
		b, err := os.ReadFile(filepath.Join(example, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Pattern C, a TestSet matrix: the shard is in the argv, so each shard has
// its own fingerprint without any variable.
func TestShardMatrixFingerprints(t *testing.T) {
	dir := uvTree(t, filepath.Join("..", "..", "examples", "uv-monorepo"))
	cs, err := contract.LoadAll(filepath.Join("testdata", "shards", "pattern-c.yaml"))
	if err != nil || len(cs) != 2 {
		t.Fatalf("pattern C: %d tests, %v", len(cs), err)
	}
	var c [2]string
	for i := range cs {
		got, err := Compute(context.Background(), dir, cs[i], noEnv)
		if err != nil {
			t.Fatal(err)
		}
		c[i] = got.Digest
	}
	if c[0] == c[1] {
		t.Error("pattern C: two matrix shards share a fingerprint (false hit)")
	}
}
