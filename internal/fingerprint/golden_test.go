package fingerprint

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/contract"
)

var update = flag.Bool("update", false, "write the golden manifest of the current fingerprint.Version if it has none (never overwrites)")

// goldenFile is the committed golden of testdata/golden/tree under one
// manifest version: the manifests of goldenContracts, concatenated. Goldens
// of older versions stay as a record.
func goldenFile(version string) string {
	return filepath.Join("testdata", "golden", strings.ReplaceAll(version, "/", "-")+".manifest")
}

// goldenContracts are fingerprinted over the same tree. With the tree,
// goldenModes and goldenEnv they reach every branch of the manifest encoding:
//   - contract.yaml: unsorted and repeated lists, a toolchain map, argv
//     order, JSON escaping, an exclude, a dependency, and env vars that are
//     set, set to "" and unset (one of them repeated);
//   - minimal.yaml: every optional field omitted, so no dependency,
//     toolchain or env lines;
//   - empty.yaml: every optional field present but empty; it must encode
//     exactly like minimal.yaml.
//
// The tree adds a nested path, a path with a space, an empty file under a
// dot-directory (included), a .git directory made by goldenTree (skipped),
// an excluded vendor file and a file outside the scope.
var goldenContracts = []string{"contract.yaml", "minimal.yaml", "empty.yaml"}

// goldenModes fixes the mode of tree files (default 0644), so the mode column
// is the same on every OS, umask and git core.fileMode setting. hook.sh has
// only the group exec bit: any exec bit means "x".
var goldenModes = map[string]fs.FileMode{"run.sh": 0o755, "hook.sh": 0o654}

func goldenEnv(k string) (string, bool) {
	switch k {
	case "DB_IMAGE":
		return "postgres:16", true
	case "EMPTY":
		return "", true
	}
	return "", false
}

// goldenProblem reports why fps, computed under version, do not match the
// golden committed for that version, or "" if they do.
func goldenProblem(version string, fps []Fingerprint, golden []byte) string {
	for _, fp := range fps {
		if !strings.HasPrefix(fp.Manifest, version+"\n") {
			return "manifest does not start with the " + version + " header"
		}
	}
	if got := concat(fps); got != string(golden) {
		return "manifest bytes changed without a version bump: the same fingerprint would now mean different inputs " +
			"(a false hit against every existing attestation). Bump fingerprint.Version, then run " +
			"`go test ./internal/fingerprint -run TestManifestGolden -update` (see the aval-format-change skill)." +
			"\n--- golden " + version + "\n" + string(golden) + "--- got\n" + got
	}
	for _, fp := range fps {
		if fp.Digest != "sha256:"+sum([]byte(fp.Manifest)) {
			return "digest is no longer sha256 over the manifest bytes: bump fingerprint.Version"
		}
	}
	return ""
}

func concat(fps []Fingerprint) string {
	var b strings.Builder
	for _, fp := range fps {
		b.WriteString(fp.Manifest)
	}
	return b.String()
}

// goldenTree returns the fingerprints of goldenContracts, in order.
func goldenTree(t *testing.T) []Fingerprint {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "golden", "tree"))); err != nil {
		t.Fatal(err)
	}
	// git cannot commit a .git directory, so it is made here. g.go matches
	// **/*.go and must not appear in any manifest.
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "g.go"), []byte("package g\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		mode, ok := goldenModes[d.Name()]
		if !ok {
			mode = 0o644
		}
		return os.Chmod(p, mode)
	}); err != nil {
		t.Fatal(err)
	}
	var fps []Fingerprint
	for _, name := range goldenContracts {
		c, err := contract.Load(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		fp, err := Compute(dir, c, goldenEnv)
		if err != nil {
			t.Fatal(err)
		}
		fps = append(fps, fp)
	}
	return fps
}

// TestManifestGolden is the version gate (#15): the manifests of a fixed tree
// are pinned per fingerprint.Version, so a change to how any of them is built
// fails CI unless Version changes too.
func TestManifestGolden(t *testing.T) {
	got := goldenTree(t)
	path := goldenFile(Version)
	golden, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && *update {
		if err := os.WriteFile(path, []byte(concat(got)), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
		return
	}
	if err != nil {
		t.Fatalf("no golden manifest for %s (%v): run `go test ./internal/fingerprint -run TestManifestGolden -update`", Version, err)
	}
	if p := goldenProblem(Version, got, golden); p != "" {
		t.Fatal(p)
	}
}

// TestGoldenOmittedEqualsEmpty: an optional field that is omitted and one
// that is present but empty declare the same inputs.
func TestGoldenOmittedEqualsEmpty(t *testing.T) {
	fps := goldenTree(t)
	if fps[1] != fps[2] {
		t.Fatalf("minimal.yaml and empty.yaml encode differently:\n%s\n%s", fps[1].Manifest, fps[2].Manifest)
	}
}

func TestGoldenCheckCatchesManifestChanges(t *testing.T) {
	got := goldenTree(t)
	golden := []byte(concat(got))
	if p := goldenProblem(Version, got, golden); p != "" {
		t.Fatalf("matching manifest reported: %s", p)
	}
	with := func(i int, manifest, digest string) []Fingerprint {
		fps := slices.Clone(got)
		fps[i] = Fingerprint{Manifest: manifest, Digest: digest}
		return fps
	}
	m, d := got[0].Manifest, got[0].Digest
	lines := strings.SplitAfter(m, "\n")
	changed := map[string][]Fingerprint{
		// A layout change under the same header, e.g. a new mode encoding.
		"line changed":  with(0, strings.Replace(m, " x run.sh", " 755 run.sh", 1), d),
		"line dropped":  with(0, strings.Join(append(lines[:2:2], lines[3:]...), ""), d),
		"line added":    with(0, m+"network none\n", d),
		"digest algo":   with(0, m, "sha512:"+got[0].Hex()),
		"wrong header":  with(0, strings.Replace(m, Version, "aval-fingerprint/v0", 1), d),
		"later changed": with(2, got[2].Manifest+"env X unset\n", got[2].Digest),
		"one dropped":   got[:2],
	}
	for name, fps := range changed {
		if goldenProblem(Version, fps, golden) == "" {
			t.Errorf("%s: not reported", name)
		}
	}
}

func git(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// goldenEdits lists the goldens (*.manifest under dir) that differ between
// commit base and the working tree other than by being added: modified,
// deleted, renamed (seen as a delete plus an add, since rename detection is
// off) or changed in type.
func goldenEdits(dir, base string) ([]string, error) {
	out, err := git(dir, "diff", "--no-renames", "--name-status", "--diff-filter=DMT", base, "--", "*.manifest")
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// TestGoldensAppendOnly makes the version gate binding: without it a manifest
// change could pass TestManifestGolden by rewriting the golden of the current
// version. A committed golden may be added (a new version) but never modified,
// renamed or deleted. The base is the merge base of HEAD and AVAL_GOLDEN_BASE,
// which CI sets and which must then resolve; unset, it is origin/main, and the
// test is skipped if that ref is missing (no clone, no remote).
func TestGoldensAppendOnly(t *testing.T) {
	ref, set := os.LookupEnv("AVAL_GOLDEN_BASE")
	if !set {
		ref = "origin/main"
	}
	base, err := git(".", "merge-base", "HEAD", "--end-of-options", ref)
	if err != nil {
		if set {
			t.Fatalf("AVAL_GOLDEN_BASE=%q: %v", ref, err)
		}
		t.Skipf("append-only check not run: no %s to compare against (%v)", ref, err)
	}
	edits, err := goldenEdits(filepath.Join("testdata", "golden"), base)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) > 0 {
		t.Fatalf("committed goldens are append-only, but these changed since %s:\n%s\n"+
			"A manifest change needs a fingerprint.Version bump and a new golden; the old one stays.",
			base, strings.Join(edits, "\n"))
	}
}

// TestGoldenEditsCatchesEdits is the negative test of TestGoldensAppendOnly,
// in a throwaway repository.
func TestGoldenEditsCatchesEdits(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	run := func(dir string, args ...string) string {
		t.Helper()
		out, err := git(dir, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	write := func(dir, name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const v1 = "aval-fingerprint-v1.manifest"
	cases := map[string]struct {
		change func(dir string)
		want   bool
	}{
		"untouched":   {func(string) {}, false},
		"new version": {func(d string) { write(d, "aval-fingerprint-v2.manifest", "v2\n") }, false},
		"tree edited": {func(d string) { write(d, "notes.txt", "other\n") }, false},
		"modified":    {func(d string) { write(d, v1, "v1 changed\n") }, true},
		"staged":      {func(d string) { write(d, v1, "v1 changed\n"); run(d, "add", "-A") }, true},
		"committed":   {func(d string) { write(d, v1, "v1 changed\n"); run(d, "commit", "-qam", "rewrite") }, true},
		"deleted":     {func(d string) { run(d, "rm", "-q", v1) }, true},
		"renamed":     {func(d string) { run(d, "mv", v1, "aval-fingerprint-v2.manifest") }, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			run(dir, "init", "-q")
			write(dir, v1, "v1\n")
			write(dir, "notes.txt", "notes\n")
			run(dir, "add", "-A")
			run(dir, "commit", "-qm", "base")
			base := run(dir, "rev-parse", "HEAD")
			tc.change(dir)
			edits, err := goldenEdits(dir, base)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(edits) > 0; got != tc.want {
				t.Errorf("edits %q, want reported=%v", edits, tc.want)
			}
		})
	}
}
