package fingerprint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/TrianaLab/aval/internal/contract"
)

// rootYAML is a contract named unit with scope.root root (none if empty),
// source src/** and dependency go.mod.
func rootYAML(root string, extra ...string) string {
	if root != "" {
		extra = append([]string{"root: " + root}, extra...)
	}
	return avalYAML("unit", "src/**", nil, append([]string{`dependencies: ["go.mod"]`}, extra...)...)
}

// rootFiles is a git working tree with a contract in aval/ rooted at the
// tree, the same contract at the top, and src/ and go.mod in both the
// tree and aval/, so a scope rooted at the wrong level hashes other files.
func rootFiles() map[string]string {
	return map[string]string{
		".git/HEAD":       "ref: refs/heads/main\n",
		"aval/unit.yaml":  rootYAML(".."),
		"aval.unit.yaml":  rootYAML(""),
		"src/a.go":        "package a\n",
		"go.mod":          "module x\n",
		"aval/src/a.go":   "package decoy\n",
		"aval/go.mod":     "module decoy\n",
		"docs/README.md":  "not in scope\n",
		"aval/notes.txt":  "not in scope\n",
		"x.json":          "{\"input\": true}\n",
		"aval/other.yaml": "x: 1\n",
	}
}

// inputLines drops the header and the contract line of a manifest.
func inputLines(manifest string) string {
	lines := strings.SplitAfter(manifest, "\n")
	return strings.Join(lines[2:], "")
}

// TestRootListsTheSameInputs (golden): aval/unit.yaml with root: .. lists
// the same source and dependency lines as aval.unit.yaml at the root; only
// the contract line differs.
func TestRootListsTheSameInputs(t *testing.T) {
	root := writeTree(t, rootFiles())
	sub, err := computeFile(root, "aval/unit.yaml")
	if err != nil {
		t.Fatal(err)
	}
	top, err := computeFile(root, "aval.unit.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if inputLines(sub.Manifest) != inputLines(top.Manifest) || sub.Manifest == top.Manifest {
		t.Fatalf("manifests:\n%s\nwant the inputs of\n%s", sub.Manifest, top.Manifest)
	}
	if !strings.Contains(sub.Manifest, " src/a.go\n") || !strings.Contains(sub.Manifest, " go.mod\n") {
		t.Fatalf("manifest paths are not relative to the root:\n%s", sub.Manifest)
	}
}

// TestRootFalseHits: everything under the root that the scope declares
// moves the fingerprint, even outside the contract's directory; root: ..
// and root: ../.. over the same relative paths never collide.
func TestRootFalseHits(t *testing.T) {
	base, err := computeFile(writeTree(t, rootFiles()), "aval/unit.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, file, data string
		changes          bool
	}{
		{"source under the root, outside the contract dir", "src/a.go", "package a // v2\n", true},
		{"new source under the root", "src/b.go", "package a\n", true},
		{"dependency under the root", "go.mod", "module y\n", true},
		{"contract dir decoy", "aval/src/a.go", "package decoy // v2\n", false},
		{"out of scope", "docs/README.md", "edited\n", false},
		{"contract without root (hashes the decoys)", "aval/unit.yaml", rootYAML(""), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := rootFiles()
			files[tc.file] = tc.data
			fp, err := computeFile(writeTree(t, files), "aval/unit.yaml")
			if err != nil {
				t.Fatal(err)
			}
			if got := fp.Digest != base.Digest; got != tc.changes {
				t.Fatalf("digest changed = %v, want %v\n%s", got, tc.changes, fp.Manifest)
			}
		})
	}

	t.Run("root .. vs ../..", func(t *testing.T) {
		files := map[string]string{
			".git/HEAD":        "x\n",
			"src/a.go":         "package outer\n",
			"go.mod":           "module outer\n",
			"ci/src/a.go":      "package inner\n",
			"ci/go.mod":        "module inner\n",
			"ci/aval/one.yaml": rootYAML(".."),
			"ci/aval/two.yaml": rootYAML("../.."),
		}
		root := writeTree(t, files)
		one, err1 := computeFile(root, "ci/aval/one.yaml")
		two, err2 := computeFile(root, "ci/aval/two.yaml")
		if err1 != nil || err2 != nil {
			t.Fatal(err1, err2)
		}
		if one.Digest == two.Digest || inputLines(one.Manifest) == inputLines(two.Manifest) {
			t.Fatalf("same inputs for different roots:\n%s\n%s", one.Manifest, two.Manifest)
		}
	})
}

// TestRootAllowMutableFiles: with scope.root, allowMutable file and files
// match findings by their path relative to the root, never relative to the
// contract's directory; a files glob relative to the contract directory
// matches nothing in scope and the contract is rejected.
func TestRootAllowMutableFiles(t *testing.T) {
	remote := "resources:\n- https://example.com/remote.yaml\n"
	for _, tc := range []struct {
		name, allow string
		allowed     bool
		wantErr     string
	}{
		{"file relative to the root", `allowMutable: [{file: src/kustomization.yaml, ref: "resources[0]", reason: r}]`, true, ""},
		{"files relative to the root", `allowMutable: [{files: ["src/*.yaml"], ref: "resources[0]", reason: r}]`, true, ""},
		{"file relative to the contract dir", `allowMutable: [{file: kustomization.yaml, ref: "resources[0]", reason: r}]`, false, ""},
		{"files relative to the contract dir", `allowMutable: [{files: ["*.yaml"], ref: "resources[0]", reason: r}]`, false, "matches no file in scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := rootFiles()
			files["src/kustomization.yaml"] = remote
			files["aval/unit.yaml"] = rootYAML("..", tc.allow)
			fp, err := computeFile(writeTree(t, files), "aval/unit.yaml")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(fp.Findings) != 1 || fp.Findings[0].File != "src/kustomization.yaml" || (fp.Findings[0].Allowed != "") != tc.allowed {
				t.Fatalf("findings = %+v, want allowed %v", fp.Findings, tc.allowed)
			}
		})
	}
}

// TestRootOutputSkip: an output beside the contract is skipped at its own
// path, relative to the root; a real input of the same name at the root
// stays hashed. An output at the root is skipped.
func TestRootOutputSkip(t *testing.T) {
	files := rootFiles()
	files["aval/unit.yaml"] = avalYAML("unit", "*.json", nil, "root: ..")
	files["aval/x.json"] = "{\"result\": true}\n"
	root := writeTree(t, files)
	fp, err := computeFile(root, "aval/unit.yaml", filepath.Join(root, "aval", "x.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fp.Manifest, " x.json\n") {
		t.Fatalf("the input x.json at the root was skipped:\n%s", fp.Manifest)
	}
	files["aval/unit.yaml"] = avalYAML("unit", "**/*.json", nil, "root: ..", `exclude: ["x.json"]`)
	root = writeTree(t, files)
	fp, err = computeFile(root, "aval/unit.yaml", filepath.Join(root, "x.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fp.Manifest, " x.json\n") || !strings.Contains(fp.Manifest, " aval/x.json\n") {
		t.Fatalf("manifest:\n%s", fp.Manifest)
	}
}

// TestRootOfReferencedContract: a scope.contracts target with its own
// scope.root resolves that root, not the referrer's, and its findings are
// reported relative to the referrer's root.
func TestRootOfReferencedContract(t *testing.T) {
	files := map[string]string{
		".git/HEAD":            "x\n",
		"app/ci/aval.yaml":     avalYAML("app", "src/**", []string{"../../lib/ci/unit.yaml"}, "root: .."),
		"app/src/a.go":         "package app\n",
		"app/src/l.go":         "package decoy\n",
		"lib/ci/unit.yaml":     avalYAML("lib", "src/**", nil, "root: .."),
		"lib/src/l.go":         "package lib\n",
		"lib/src/compose.yaml": "services:\n  db:\n    image: postgres:16\n",
	}
	root := writeTree(t, files)
	fp, err := computeFile(root, "app/ci/aval.yaml")
	if err != nil {
		t.Fatal(err)
	}
	lib, err := computeFile(root, "lib/ci/unit.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(fp.Contracts) != 1 || fp.Contracts[0].Digest != lib.Digest || !strings.Contains(lib.Manifest, " src/compose.yaml\n") {
		t.Fatalf("contracts = %+v\nlib:\n%s", fp.Contracts, lib.Manifest)
	}
	if len(fp.Findings) != 1 || fp.Findings[0].File != "../lib/src/compose.yaml" {
		t.Fatalf("findings = %+v", fp.Findings)
	}
	// A referrer rooted at the top of the tree.
	files["ci/aval.yaml"] = avalYAML("top", "app/src/**", []string{"../lib/ci/unit.yaml"}, "root: ..")
	root = writeTree(t, files)
	if fp, err := computeFile(root, "ci/aval.yaml"); err != nil || len(fp.Findings) != 1 || fp.Findings[0].File != "lib/src/compose.yaml" {
		t.Fatalf("findings = %+v, %v", fp.Findings, err)
	}
	files["lib/src/l.go"] = "package lib // v2\n"
	changed, err := computeFile(writeTree(t, files), "app/ci/aval.yaml")
	if err != nil || changed.Digest == fp.Digest {
		t.Fatalf("a change under the referenced root did not move the fingerprint: %v", err)
	}
}

// TestRootFailsClosed: a root above the git working tree, no working tree
// at all, or a root outside the browser demo's project is an error.
func TestRootFailsClosed(t *testing.T) {
	files := rootFiles()
	files["aval/unit.yaml"] = rootYAML("../..")
	root := writeTree(t, files)
	if _, err := computeFile(root, "aval/unit.yaml"); err == nil || !strings.Contains(err.Error(), "scope.root ../.. "+outside) {
		t.Fatalf("root above the tree: %v", err)
	}
	c := mustLoad(t, filepath.Join(root, "aval", "unit.yaml"))
	if _, err := Root(filepath.Join(root, "aval"), c); err == nil || !strings.Contains(err.Error(), "leads outside the git working tree") {
		t.Fatalf("Root: %v", err)
	}

	files = rootFiles()
	delete(files, ".git/HEAD")
	root = writeTree(t, files)
	if _, err := computeFile(root, "aval/unit.yaml"); err == nil || !strings.Contains(err.Error(), "no git working tree") {
		t.Fatalf("no working tree: %v", err)
	}
	if _, err := Root(filepath.Join(root, "aval"), mustLoad(t, filepath.Join(root, "aval", "unit.yaml"))); err == nil {
		t.Fatal("Root without a working tree")
	}

	c, err := contract.Parse([]byte(rootYAML("..")))
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{"src/a.go": {Data: []byte("x")}, "go.mod": {Data: []byte("x")}}
	if _, err := ComputeFS(fsys, c, noEnv); err == nil || !strings.Contains(err.Error(), outside) {
		t.Fatalf("ComputeFS: %v", err)
	}
}

// TestRootPath: Root is the contract's directory without scope.root, and
// the real location of that directory joined with it otherwise, so a
// symlinked contract directory resolves from its target.
func TestRootPath(t *testing.T) {
	root := writeTree(t, rootFiles())
	c := &contract.Contract{}
	if got, err := Root("some/dir", c); got != "some/dir" || err != nil {
		t.Fatalf("Root without scope.root = %q, %v", got, err)
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	c.Scope.Root = ".."
	if got, err := Root(filepath.Join(root, "aval"), c); got != real || err != nil {
		t.Fatalf("Root = %q, %v, want %q", got, err, real)
	}
	// A symlink elsewhere to aval/ still roots at the tree aval/ is in.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Join(root, "aval"), link); err != nil {
		t.Fatal(err)
	}
	if got, err := Root(link, c); got != real || err != nil {
		t.Fatalf("Root through a symlink = %q, %v, want %q", got, err, real)
	}
	fp, err := Compute(t.Context(), link, mustLoad(t, filepath.Join(root, "aval", "unit.yaml")), noEnv)
	if err != nil || !strings.Contains(fp.Manifest, " src/a.go\n") {
		t.Fatalf("Compute through a symlink: %v\n%s", err, fp.Manifest)
	}
}

// TestRealPaths: absolute with the directory's symlinks resolved; "" kept;
// a missing directory leaves the path absolute.
func TestRealPaths(t *testing.T) {
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing", "x")
	got := RealPaths([]string{"", filepath.Join(dir, "x"), missing})
	if got[0] != "" || got[1] != filepath.Join(real, "x") || got[2] != missing {
		t.Fatalf("RealPaths = %q", got)
	}
	if rel := RealPaths([]string{"x"})[0]; !filepath.IsAbs(rel) {
		t.Fatalf("relative path stayed %q", rel)
	}
}

func mustLoad(t *testing.T, file string) *contract.Contract {
	t.Helper()
	c, err := contract.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
