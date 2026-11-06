package fingerprint

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// benchTree writes 100 scoped files under src/ and 2000 files under a
// node_modules/ sibling that no include glob can match.
func benchTree(b *testing.B) string {
	b.Helper()
	dir := b.TempDir()
	write := func(name string) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	for i := range 100 {
		write(fmt.Sprintf("src/p%d/f%d.go", i%10, i))
	}
	for i := range 2000 {
		write(fmt.Sprintf("node_modules/m%d/d/f%d.js", i%50, i))
	}
	write("go.mod")
	return dir
}

// BenchmarkWalk measures the scope-bounded walk alone over benchTree.
func BenchmarkWalk(b *testing.B) {
	dir := benchTree(b)
	include := splitGlobs([]string{"src/**/*.go", "go.mod"})
	b.ReportAllocs()
	for b.Loop() {
		if _, err := listFiles(dir, nil, include); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCompute measures a full fingerprint over benchTree. Run both with
// `go test -run '^$' -bench . ./internal/fingerprint`.
func BenchmarkCompute(b *testing.B) {
	dir := benchTree(b)
	c := testContract()
	c.Scope.Source = []string{"src/**/*.go"}
	c.Scope.Exclude = []string{"src/p0/**"}
	c.Scope.Dependencies = []string{"go.mod"}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Compute(dir, c, fixedEnv); err != nil {
			b.Fatal(err)
		}
	}
}
