package fingerprint

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/TrianaLab/aval/internal/contract"
)

// avalYAML is a contract named name with scope.source source, the
// scope.contracts refs and extra lines appended under scope.
func avalYAML(name, source string, refs []string, extra ...string) string {
	s := fmt.Sprintf("apiVersion: %s\nkind: Test\nmetadata: {name: %s}\nrunner: {command: [make, test]}\nscope:\n  source: [%q]\n",
		contract.APIVersion, name, source)
	if len(refs) > 0 {
		s += "  contracts: [" + strings.Join(refs, ", ") + "]\n"
	}
	for _, e := range extra {
		s += "  " + e + "\n"
	}
	return s + "cacheability: hermetic\n"
}

// monorepoFiles is a git working tree with frontend -> backend -> shared,
// and files in and out of each scope.
func monorepoFiles() map[string]string {
	return map[string]string{
		".git/HEAD":            "ref: refs/heads/main\n",
		"frontend/aval.yaml":   avalYAML("frontend", "src/**", []string{"../backend/aval.yaml"}),
		"frontend/src/app.js":  "app\n",
		"backend/aval.yaml":    avalYAML("backend", "api/**", []string{"../shared/aval.yaml"}),
		"backend/api/api.go":   "package api\n",
		"backend/README.md":    "not in scope\n",
		"shared/aval.yaml":     avalYAML("shared", "lib/**", nil),
		"shared/lib/lib.go":    "package lib\n",
		"shared/notes.txt":     "not in scope\n",
		"unrelated/other.yaml": "x: 1\n",
	}
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, data := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// computeFile loads the contract at root/file and fingerprints it.
func computeFile(root, file string, outputs ...string) (Fingerprint, error) {
	p := filepath.Join(root, filepath.FromSlash(file))
	c, err := contract.Load(p)
	if err != nil {
		return Fingerprint{}, err
	}
	return Compute(context.Background(), filepath.Dir(p), c, noEnv, outputs...)
}

func noEnv(string) (string, bool) { return "", false }

// TestContractsFalseHits: the dependent's fingerprint follows every input a
// referenced contract declares, transitively, and nothing it does not.
func TestContractsFalseHits(t *testing.T) {
	base, err := computeFile(writeTree(t, monorepoFiles()), "frontend/aval.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, file, data string
		changes          bool
	}{
		{"backend file in scope", "backend/api/api.go", "package api // v2\n", true},
		{"new backend file in scope", "backend/api/new.go", "package api\n", true},
		{"backend contract", "backend/aval.yaml", avalYAML("backend", "api/**", []string{"../shared/aval.yaml"}, "exclude: [api/x]"), true},
		{"shared file in scope (transitive)", "shared/lib/lib.go", "package lib // v2\n", true},
		{"backend file outside scope", "backend/README.md", "edited\n", false},
		{"shared file outside scope", "shared/notes.txt", "edited\n", false},
		{"file in no scope", "unrelated/other.yaml", "x: 2\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := monorepoFiles()
			files[tc.file] = tc.data
			fp, err := computeFile(writeTree(t, files), "frontend/aval.yaml")
			if err != nil {
				t.Fatal(err)
			}
			if got := fp.Digest != base.Digest; got != tc.changes {
				t.Fatalf("digest changed = %v, want %v\n%s", got, tc.changes, fp.Manifest)
			}
		})
	}
}

// TestContractsTree: one contract line per reference, after the other
// lines, and the tree with each referenced fingerprint.
func TestContractsTree(t *testing.T) {
	root := writeTree(t, monorepoFiles())
	fp, err := computeFile(root, "frontend/aval.yaml")
	if err != nil {
		t.Fatal(err)
	}
	backend, err := computeFile(root, "backend/aval.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(fp.Manifest, "\ncontract ../backend/aval.yaml backend "+backend.Digest+"\n") {
		t.Fatalf("manifest:\n%s", fp.Manifest)
	}
	if len(fp.Contracts) != 1 || fp.Contracts[0].Path != "../backend/aval.yaml" || fp.Contracts[0].Name != "backend" ||
		fp.Contracts[0].Digest != backend.Digest || len(fp.Contracts[0].Contracts) != 1 || fp.Contracts[0].Contracts[0].Name != "shared" {
		t.Fatalf("tree = %+v", fp.Contracts)
	}
	// Without scope.contracts the manifest has no contract <path> line.
	if shared, _ := computeFile(root, "shared/aval.yaml"); shared.Contracts != nil || strings.Count(shared.Manifest, "contract ") != 1 {
		t.Fatalf("shared:\n%s", shared.Manifest)
	}
}

// TestContractsDiamond: a contract referenced twice is not a cycle, and the
// order of scope.contracts does not matter.
func TestContractsDiamond(t *testing.T) {
	files := monorepoFiles()
	files["frontend/aval.yaml"] = avalYAML("frontend", "src/**", []string{"../shared/aval.yaml", "../backend/aval.yaml"})
	a, err := computeFile(writeTree(t, files), "frontend/aval.yaml")
	if err != nil {
		t.Fatal(err)
	}
	files["frontend/aval.yaml"] = avalYAML("frontend", "src/**", []string{"../backend/aval.yaml", "../shared/aval.yaml"})
	b, err := computeFile(writeTree(t, files), "frontend/aval.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(a.Manifest, "\ncontract ../backend/aval.yaml backend "+a.Contracts[0].Digest+"\ncontract ../shared/aval.yaml shared "+a.Contracts[1].Digest+"\n") {
		t.Fatalf("manifest:\n%s", a.Manifest)
	}
	// The canonical contract sorts the list, so the manifests are equal.
	if a.Manifest != b.Manifest {
		t.Fatalf("order changed the manifest:\n%s\n%s", a.Manifest, b.Manifest)
	}
}

// TestContractsFailClosed: anything that keeps a reference from being
// proven fails the fingerprint, so the test runs.
func TestContractsFailClosed(t *testing.T) {
	chain := func(n int) func(map[string]string) {
		return func(f map[string]string) {
			for i := range n {
				f[fmt.Sprintf("c%d/aval.yaml", i)] = avalYAML(fmt.Sprintf("c%d", i), "aval.yaml", []string{fmt.Sprintf("../c%d/aval.yaml", i+1)})
			}
			f[fmt.Sprintf("c%d/aval.yaml", n)] = avalYAML("last", "aval.yaml", nil)
			f["frontend/aval.yaml"] = avalYAML("frontend", "src/**", []string{"../c0/aval.yaml"})
		}
	}
	cases := []struct {
		name   string
		change func(map[string]string)
		want   string
	}{
		{"missing contract", func(f map[string]string) { delete(f, "backend/aval.yaml") },
			"scope.contracts ../backend/aval.yaml: lstat backend/aval.yaml: "},
		{"invalid contract", func(f map[string]string) { f["backend/aval.yaml"] = "kind: Test\n" },
			"scope.contracts ../backend/aval.yaml: apiVersion"},
		{"not yaml", func(f map[string]string) { f["backend/aval.yaml"] = "\t{" },
			"scope.contracts ../backend/aval.yaml: decoding contract"},
		{"referenced scope matches nothing", func(f map[string]string) { delete(f, "backend/api/api.go") },
			"scope.contracts ../backend/aval.yaml: source glob"},
		{"transitive failure", func(f map[string]string) { delete(f, "shared/lib/lib.go") },
			"scope.contracts ../backend/aval.yaml: scope.contracts ../shared/aval.yaml: source glob"},
		{"cycle", func(f map[string]string) {
			f["shared/aval.yaml"] = avalYAML("shared", "lib/**", []string{"../backend/aval.yaml"})
		}, "cycle: backend/aval.yaml -> shared/aval.yaml -> backend/aval.yaml"},
		{"self reference", func(f map[string]string) {
			f["frontend/aval.yaml"] = avalYAML("frontend", "src/**", []string{"aval.yaml"})
		}, "cycle: frontend/aval.yaml -> frontend/aval.yaml"},
		{"outside the repository", func(f map[string]string) {
			f["frontend/aval.yaml"] = avalYAML("frontend", "src/**", []string{"../../elsewhere/aval.yaml"})
		}, "scope.contracts ../../elsewhere/aval.yaml: leads outside the repository root"},
		{"no git working tree", func(f map[string]string) { delete(f, ".git/HEAD") },
			"scope.contracts: no git working tree (.git) at or above"},
		{"deep chain", chain(maxContractDepth + 1), fmt.Sprintf("more than %d levels or %d contracts", maxContractDepth, maxContracts)},
		{"many contracts", func(f map[string]string) {
			var refs []string
			for i := range maxContracts + 1 {
				refs = append(refs, fmt.Sprintf("../m/c%02d.yaml", i))
				f[fmt.Sprintf("m/c%02d.yaml", i)] = avalYAML(fmt.Sprintf("c%02d", i), "aval.yaml", nil)
			}
			f["m/aval.yaml"] = "x\n"
			f["frontend/aval.yaml"] = avalYAML("frontend", "src/**", refs)
		}, "scope.contracts ../m/c64.yaml: more than"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := monorepoFiles()
			tc.change(files)
			_, err := computeFile(writeTree(t, files), "frontend/aval.yaml")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
	// The deepest chain allowed still works.
	files := monorepoFiles()
	chain(maxContractDepth - 1)(files)
	if _, err := computeFile(writeTree(t, files), "frontend/aval.yaml"); err != nil {
		t.Fatal(err)
	}
}

// TestContractsSymlink: a symlink on the way to a referenced contract fails
// closed, like a symlink in scope.
func TestContractsSymlink(t *testing.T) {
	for _, link := range []string{"backend", "backend/aval.yaml"} {
		t.Run(link, func(t *testing.T) {
			root := writeTree(t, monorepoFiles())
			real := filepath.Join(root, "real")
			if err := os.Rename(filepath.Join(root, filepath.FromSlash(link)), real); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, filepath.Join(root, filepath.FromSlash(link))); err != nil {
				t.Fatal(err)
			}
			_, err := computeFile(root, "frontend/aval.yaml")
			if err == nil || !strings.Contains(err.Error(), link+" is a symlink") {
				t.Fatalf("got %v", err)
			}
		})
	}
	// The contract's own directory may be a symlink: ../ is resolved from
	// its real location, as the OS does for the test, not from the link's
	// path text, where a decoy contract sits.
	t.Run("own directory", func(t *testing.T) {
		root := writeTree(t, map[string]string{
			".git/HEAD":              "ref: refs/heads/main\n",
			"packages/web/aval.yaml": avalYAML("web", "aval.yaml", []string{"../api/aval.yaml"}),
			"packages/api/aval.yaml": avalYAML("api", "src/**", nil),
			"packages/api/src/x":     "real\n",
			"apps/api/aval.yaml":     avalYAML("api", "src/**", nil),
			"apps/api/src/x":         "decoy\n",
		})
		if err := os.Symlink(filepath.Join("..", "packages", "web"), filepath.Join(root, "apps", "web")); err != nil {
			t.Fatal(err)
		}
		fp, err := computeFile(root, "apps/web/aval.yaml")
		if err != nil {
			t.Fatal(err)
		}
		real, err := computeFile(root, "packages/api/aval.yaml")
		if err != nil {
			t.Fatal(err)
		}
		if fp.Contracts[0].Digest != real.Digest {
			t.Fatalf("bound the decoy at the link's path text:\n%s", fp.Manifest)
		}
	})
}

// TestContractsUnreadable: a referenced contract that exists but cannot be
// read fails closed.
func TestContractsUnreadable(t *testing.T) {
	fsys := fstest.MapFS{
		"aval.yaml":    {Data: []byte(avalYAML("a", "aval.yaml", []string{"b.yaml"}))},
		"b.yaml/x.txt": {Data: []byte("b.yaml is a directory\n")},
	}
	c, err := contract.Parse(fsys["aval.yaml"].Data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ComputeFS(fsys, c, noEnv); err == nil || !strings.Contains(err.Error(), "scope.contracts b.yaml: ") {
		t.Fatalf("got %v", err)
	}
	// A regular file that cannot be read.
	root := writeTree(t, monorepoFiles())
	if err := os.Chmod(filepath.Join(root, "backend", "aval.yaml"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := computeFile(root, "frontend/aval.yaml"); err == nil || !strings.Contains(err.Error(), "scope.contracts ../backend/aval.yaml: ") {
		t.Fatalf("got %v", err)
	}
	// A FIFO would block the read; it fails instead.
	fsys = fstest.MapFS{"aval.yaml": fsys["aval.yaml"], "b.yaml": {Mode: fs.ModeNamedPipe}}
	if _, err := ComputeFS(fsys, c, noEnv); err == nil || !strings.Contains(err.Error(), "b.yaml is not a regular file") {
		t.Fatalf("got %v", err)
	}
}

// TestContractsFindings: a referenced contract's mutable references block
// the dependent unless the referenced contract itself accepts them; the
// dependent's scope.allowMutable never does.
func TestContractsFindings(t *testing.T) {
	// A compose image and a kustomize images entry (a container finding
	// from the kustomize detector) propagate the same way.
	sources := []struct{ file, data, ref string }{
		{"compose.yaml", "services:\n  db:\n    image: postgres:16\n", "postgres:16"},
		{"kustomization.yaml", "images:\n- name: nginx\n  newTag: \"1.27\"\n", "nginx:1.27"},
	}
	cases := []struct {
		name               string
		backendAllow, own  bool
		wantUnpinned, want int
	}{
		{"unacknowledged", false, false, 1, 1},
		{"acknowledged by the dependent only", false, true, 1, 1},
		{"acknowledged by the referenced contract", true, false, 0, 1},
	}
	for _, src := range sources {
		allow := `allowMutable: [{ref: "` + src.ref + `", reason: "scanned nightly"}]`
		for _, tc := range cases {
			t.Run(src.file+"/"+tc.name, func(t *testing.T) {
				files := monorepoFiles()
				files["backend/api/"+src.file] = src.data
				if tc.backendAllow {
					files["backend/aval.yaml"] = avalYAML("backend", "api/**", []string{"../shared/aval.yaml"}, allow)
				}
				if tc.own {
					files["frontend/aval.yaml"] = avalYAML("frontend", "src/**", []string{"../backend/aval.yaml"}, allow)
				}
				fp, err := computeFile(writeTree(t, files), "frontend/aval.yaml")
				if err != nil {
					t.Fatal(err)
				}
				if len(fp.Findings) != tc.want || len(fp.Unpinned()) != tc.wantUnpinned || fp.Findings[0].File != "../backend/api/"+src.file {
					t.Fatalf("findings = %+v", fp.Findings)
				}
			})
		}
	}
}

// TestContractsAllowMutableByFile: file: in a referenced contract's
// scope.allowMutable is relative to that contract and acknowledges only that
// file's finding; a ref-only entry is ambiguous when two of its own files
// report the ref; the dependent's entries, with or without file:, never
// reach the referenced contract's findings, nor count toward its ambiguity.
func TestContractsAllowMutableByFile(t *testing.T) {
	remote := "resources:\n- https://example.com/remote.yaml\n"
	for _, tc := range []struct {
		name          string
		backend, own  string
		wantAllowed   []string // files, as the dependent reports them
		wantAmbiguous int
	}{
		{"by file in the referenced contract", `allowMutable: [{file: api/a/kustomization.yaml, ref: "resources[0]", reason: r}]`, "",
			[]string{"../backend/api/a/kustomization.yaml"}, 0},
		{"ref-only in the referenced contract is ambiguous", `allowMutable: [{ref: "resources[0]", reason: r}]`, "", nil, 2},
		{"the dependent's file entry does not reach", "", `allowMutable: [{file: api/a/kustomization.yaml, ref: "resources[0]", reason: r}]`, nil, 0},
		{"the dependent's ref-only entry covers its own file only", "", `allowMutable: [{ref: "resources[0]", reason: r}]`,
			[]string{"src/kustomization.yaml"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := monorepoFiles()
			files["backend/api/a/kustomization.yaml"] = remote
			files["backend/api/b/kustomization.yaml"] = remote
			files["frontend/src/kustomization.yaml"] = remote
			files["backend/aval.yaml"] = avalYAML("backend", "api/**", []string{"../shared/aval.yaml"}, tc.backend)
			files["frontend/aval.yaml"] = avalYAML("frontend", "src/**", []string{"../backend/aval.yaml"}, tc.own)
			fp, err := computeFile(writeTree(t, files), "frontend/aval.yaml")
			if err != nil {
				t.Fatal(err)
			}
			var allowed []string
			ambiguous := 0
			for _, f := range fp.Findings {
				if f.Allowed != "" {
					allowed = append(allowed, f.File)
				}
				if strings.Contains(f.Fix, "without file:") {
					ambiguous++
				}
			}
			if len(fp.Findings) != 3 || fmt.Sprint(allowed) != fmt.Sprint(tc.wantAllowed) || ambiguous != tc.wantAmbiguous {
				t.Fatalf("allowed = %v, ambiguous = %d, findings = %+v", allowed, ambiguous, fp.Findings)
			}
		})
	}
}

// TestContractsOutputsInReferencedScopeAreInputs: an Aval output inside a
// referenced contract's scope stays an input there (the #63 guard checks
// only the top scope), so writing it changes the dependent's fingerprint.
func TestContractsOutputsInReferencedScopeAreInputs(t *testing.T) {
	root := writeTree(t, monorepoFiles())
	out := filepath.Join(root, "backend", "api", "result.json")
	before, err := computeFile(root, "frontend/aval.yaml", out)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := computeFile(root, "frontend/aval.yaml", out)
	if err != nil {
		t.Fatal(err)
	}
	if before.Digest == after.Digest {
		t.Fatalf("an input of a referenced contract was skipped:\n%s", after.Manifest)
	}
}

// TestContractsComputeFSMatchesCompute: the browser demo's in-memory root
// gives the manifest Compute gives on disk, and no reference leaves it.
func TestContractsComputeFSMatchesCompute(t *testing.T) {
	files := map[string]string{
		".git/HEAD":          "x\n",
		"aval.yaml":          avalYAML("app", "src/**", []string{"backend/aval.yaml"}),
		"src/app.js":         "app\n",
		"backend/aval.yaml":  avalYAML("backend", "api/**", nil),
		"backend/api/api.go": "package api\n",
	}
	disk, err := computeFile(writeTree(t, files), "aval.yaml")
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{}
	for name, data := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(data), Mode: 0o644}
	}
	c, _ := contract.Parse(fsys["aval.yaml"].Data)
	mem, err := ComputeFS(fsys, c, noEnv)
	if err != nil || mem.Manifest != disk.Manifest {
		t.Fatalf("%v\n--- disk\n%s--- memory\n%s", err, disk.Manifest, mem.Manifest)
	}
	c.Scope.Contracts = []string{"../backend/aval.yaml"}
	if _, err := ComputeFS(fsys, c, noEnv); err == nil || !strings.Contains(err.Error(), "leads outside") {
		t.Fatalf("got %v", err)
	}
}

// contractsGolden pins the contract <path> lines of the current manifest
// version; goldens of older versions stay as a record.
var contractsGolden = filepath.Join(filepath.Dir(goldenFile(Version)), "contracts-"+filepath.Base(goldenFile(Version)))

// TestContractsManifestGolden pins the contract <path> lines of
// aval-fingerprint/v1: one per scope.contracts entry, sorted by path, after
// every other line. Manifests without scope.contracts are pinned by
// TestManifestGolden and stay byte-identical.
func TestContractsManifestGolden(t *testing.T) {
	files := monorepoFiles()
	files["frontend/aval.yaml"] = avalYAML("frontend", "src/**", []string{"../shared/aval.yaml", "../backend/aval.yaml"})
	fp, err := computeFile(writeTree(t, files), "frontend/aval.yaml")
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile(contractsGolden)
	if errors.Is(err, fs.ErrNotExist) && *update {
		if err := os.WriteFile(contractsGolden, []byte(fp.Manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err != nil || string(golden) != fp.Manifest {
		t.Fatalf("contracts manifest changed without a version bump (%v):\n--- golden\n%s--- got\n%s", err, golden, fp.Manifest)
	}
}

// TestContractsTopOutputsSkipped: an Aval output in the top contract's own
// scope is skipped even when that contract is not at the git root.
func TestContractsTopOutputsSkipped(t *testing.T) {
	root := writeTree(t, monorepoFiles())
	out := filepath.Join(root, "frontend", "src", "result.json")
	before, err := computeFile(root, "frontend/aval.yaml", out)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := computeFile(root, "frontend/aval.yaml", out)
	if err != nil {
		t.Fatal(err)
	}
	if before.Digest != after.Digest {
		t.Fatalf("output changed the fingerprint:\n%s", after.Manifest)
	}
}
