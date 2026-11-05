package fingerprint

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/contract"
)

var update = flag.Bool("update", false, "write the golden manifest of the current fingerprint.Version if it has none (never overwrites)")

// goldenFile is the committed manifest of testdata/golden/tree under one
// manifest version. Goldens of older versions stay as a record.
func goldenFile(version string) string {
	return filepath.Join("testdata", "golden", strings.ReplaceAll(version, "/", "-")+".manifest")
}

// goldenProblem reports why got, computed under version, does not match the
// golden manifest committed for that version, or "" if it does.
func goldenProblem(version string, got Fingerprint, golden []byte) string {
	switch {
	case !strings.HasPrefix(got.Manifest, version+"\n"):
		return "manifest does not start with the " + version + " header"
	case got.Manifest != string(golden):
		return "manifest bytes changed without a version bump: the same fingerprint would now mean different inputs " +
			"(a false hit against every existing attestation). Bump fingerprint.Version, then run " +
			"`go test ./internal/fingerprint -run TestManifestGolden -update` (see the aval-format-change skill)." +
			"\n--- golden " + version + "\n" + string(golden) + "--- got\n" + got.Manifest
	case got.Digest != "sha256:"+sum(golden):
		return "digest is no longer sha256 over the manifest bytes: bump fingerprint.Version"
	}
	return ""
}

func goldenTree(t *testing.T) Fingerprint {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "golden", "tree"))); err != nil {
		t.Fatal(err)
	}
	// Modes are set here, not taken from the checkout, so the mode column is
	// the same on every OS, umask and git core.fileMode setting.
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		mode := fs.FileMode(0o644)
		if d.Name() == "run.sh" {
			mode = 0o755
		}
		return os.Chmod(p, mode)
	}); err != nil {
		t.Fatal(err)
	}
	c, err := contract.Load(filepath.Join(dir, "contract.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	fp, err := Compute(dir, c, fixedEnv)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

// TestManifestGolden is the version gate (#15): the manifest of a fixed tree
// is pinned per fingerprint.Version, so any change to which bytes are hashed
// or how they are laid out fails CI unless Version changes too.
func TestManifestGolden(t *testing.T) {
	got := goldenTree(t)
	path := goldenFile(Version)
	golden, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && *update {
		if err := os.WriteFile(path, []byte(got.Manifest), 0o644); err != nil {
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

func TestGoldenCheckCatchesManifestChanges(t *testing.T) {
	got := goldenTree(t)
	golden := []byte(got.Manifest)
	if p := goldenProblem(Version, got, golden); p != "" {
		t.Fatalf("matching manifest reported: %s", p)
	}
	lines := strings.SplitAfter(got.Manifest, "\n")
	changed := map[string]Fingerprint{
		// A layout change under the same header, e.g. a new mode encoding.
		"line changed": {Manifest: strings.Replace(got.Manifest, " x run.sh", " 755 run.sh", 1), Digest: got.Digest},
		"line dropped": {Manifest: strings.Join(append(lines[:2:2], lines[3:]...), ""), Digest: got.Digest},
		"line added":   {Manifest: got.Manifest + "network none\n", Digest: got.Digest},
		"digest algo":  {Manifest: got.Manifest, Digest: "sha512:" + got.Hex()},
		"wrong header": {Manifest: strings.Replace(got.Manifest, Version, "aval-fingerprint/v0", 1), Digest: got.Digest},
	}
	for name, fp := range changed {
		if goldenProblem(Version, fp, golden) == "" {
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
// check is logged as not run if that ref is missing (no clone, no remote).
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
		t.Logf("append-only check not run: no %s to compare against (%v)", ref, err)
		return
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
