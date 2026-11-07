package fingerprint

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/contract"
)

type entry struct {
	content string
	exec    bool
}

// decodeTree turns fuzz bytes into a file tree. Entries are separated by
// 0xff; per entry, byte 0 holds the exec bit (bit 0) and the depth (bits
// 1-2), the next depth bytes name directories, one byte names the file, the
// rest is content. Directory names are one of a-c and file names end in ".f",
// so a path is never both a file and a directory, and lowercase-only names
// avoid collisions on case-insensitive filesystems.
func decodeTree(data []byte) map[string]entry {
	tree := map[string]entry{}
	for _, e := range strings.Split(string(data), "\xff") {
		if e == "" {
			continue
		}
		depth := int(e[0]>>1) & 3
		if len(e) < 2+depth {
			continue
		}
		var segs []string
		for _, b := range []byte(e[1 : 1+depth]) {
			segs = append(segs, string(rune('a'+b%3)))
		}
		segs = append(segs, string(rune('a'+e[1+depth]%4))+".f")
		tree[strings.Join(segs, "/")] = entry{content: e[2+depth:], exec: e[0]&1 == 1}
	}
	return tree
}

func manifestOf(t *testing.T, tree map[string]entry) string {
	t.Helper()
	dir := t.TempDir()
	for p, e := range tree {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if e.exec {
			mode = 0o755
		}
		if err := os.WriteFile(full, []byte(e.content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(full, mode); err != nil { // WriteFile's mode is masked by umask
			t.Fatal(err)
		}
	}
	c := testContract()
	c.Scope = contract.Scope{Source: []string{"**"}}
	fp, err := Compute(context.Background(), dir, c, fixedEnv)
	if err != nil {
		t.Fatal(err)
	}
	return fp.Manifest
}

// FuzzManifestDistinguishesTrees: two different file trees (path, content or
// exec bit) never produce the same manifest, and equal trees always do.
func FuzzManifestDistinguishesTrees(f *testing.F) {
	f.Add([]byte("\x00ahello"), []byte("\x00ahello"))
	f.Add([]byte("\x00ahello"), []byte("\x01ahello"))
	f.Add([]byte("\x00ahello"), []byte("\x00bhello"))
	f.Add([]byte("\x02aax\xff\x00ay"), []byte("\x00ax\xff\x02aay"))
	f.Add([]byte("\x00aone\nsource 00 - b.f"), []byte("\x00aone\xff\x00b"))
	f.Add([]byte("\x00a\xff\x00b"), []byte("\x00a"))
	f.Fuzz(func(t *testing.T, a, b []byte) {
		ta, tb := decodeTree(a), decodeTree(b)
		if len(ta) == 0 || len(tb) == 0 {
			return // empty scope fails closed, covered by TestFalseHitCorpus
		}
		ma, mb := manifestOf(t, ta), manifestOf(t, tb)
		if same := maps.Equal(ta, tb); same != (ma == mb) {
			t.Fatalf("trees equal=%v but manifests equal=%v\n%v\n%v\n--- manifest a\n%s--- manifest b\n%s", same, ma == mb, ta, tb, ma, mb)
		}
	})
}
