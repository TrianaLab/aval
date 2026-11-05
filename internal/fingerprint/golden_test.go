package fingerprint

import (
	"errors"
	"flag"
	"io/fs"
	"os"
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
	// The exec bit is set here, not in git, so the mode column is the same on
	// every checkout.
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o755); err != nil {
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
