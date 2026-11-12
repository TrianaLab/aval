package js

import (
	"flag"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/fingerprint"
	"github.com/TrianaLab/aval/internal/gen"
)

var update = flag.Bool("update", false, "rewrite testdata/monorepo/aval.yaml")

const fixture = "testdata/monorepo"

// generate runs the adapter and the core on dir/aval.yaml.
func generate(t *testing.T, dir string) (before, after string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "aval.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	units, err := Adapter{}.Units(dir)
	if err != nil {
		t.Fatal(err)
	}
	out, err := gen.Rewrite(data, "js", units)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), string(out)
}

// copyFixture copies the fixture repository to a temporary directory.
func copyFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.CopyFS(dir, os.DirFS(fixture)); err != nil {
		t.Fatal(err)
	}
	return dir
}

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The fixture's aval.yaml is the golden output: generating again changes
// nothing (go test -update rewrites it).
func TestGolden(t *testing.T) {
	before, after := generate(t, fixture)
	if *update {
		if err := os.WriteFile(filepath.Join(fixture, "aval.yaml"), []byte(after), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if before != after {
		t.Fatalf("golden out of date (go test ./internal/gen/js -update):\n%s", gen.Diff("golden", "generated", []byte(before), []byte(after)))
	}
}

// digests fingerprints every test of dir/aval.yaml over the files of dir.
func digests(t *testing.T, dir string) map[string]string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(dir, "aval.yaml"))
	cs, err := contract.ParseAll(data)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, c := range cs {
		fp, err := fingerprint.ComputeFS(os.DirFS(dir), c, fingerprint.Env{})
		if err != nil {
			t.Fatal(err)
		}
		out[c.Metadata.Name] = fp.Digest
	}
	return out
}

// web -> ui -> utils: a change to utils re-runs web; a change to api, to an
// excluded package, to docs or under node_modules does not.
func TestFalseHitTransitiveDependency(t *testing.T) {
	dir := copyFixture(t)
	before := digests(t, dir)
	if len(before) != 5 {
		t.Fatalf("tests %v, want 5", slices.Sorted(maps.Keys(before)))
	}
	write(t, dir, map[string]string{
		"apps/api/src/server.ts":           "changed",
		"packages/legacy/index.js":         "changed",
		"docs/notes.txt":                   "changed",
		"apps/web/node_modules/x/index.js": "installed",
	})
	if got := digests(t, dir)["js.acme.web"]; got != before["js.acme.web"] {
		t.Error("js.acme.web changed after an unrelated change")
	}
	write(t, dir, map[string]string{"packages/utils/src/index.ts": "changed"})
	after := digests(t, dir)
	for _, name := range []string{"js.acme.web", "js.acme.ui", "js.acme.api", "js.acme.utils"} {
		if after[name] == before[name] {
			t.Errorf("%s reused after its (transitive) dependency utils changed", name)
		}
	}
	if after["js.acme.tsconfig"] != before["js.acme.tsconfig"] {
		t.Error("js.acme.tsconfig changed after utils changed")
	}
}

// A new local reference, by name, workspace: or file:, is drift.
func TestFalseHitNewReferenceIsDrift(t *testing.T) {
	for _, spec := range []string{`"^1.0.0"`, `"workspace:*"`, `"file:../../packages/ui"`} {
		dir := copyFixture(t)
		write(t, dir, map[string]string{"apps/api/package.json": `{"name": "@acme/api", "dependencies": {"@acme/utils": "workspace:^", "@acme/ui": ` + spec + `}}`})
		before, after := generate(t, dir)
		if d := gen.Diff("a", "b", []byte(before), []byte(after)); !strings.Contains(d, "+      - js.acme.ui") {
			t.Errorf("%s: no drift:\n%s", spec, d)
		}
	}
}

func TestManagers(t *testing.T) {
	pkgs := map[string]string{
		"libs/a/package.json":   `{"name": "a", "dependencies": {"b": "^1"}, "devDependencies": {"left-pad": "^1"}}`,
		"libs/b/package.json":   `{"name": "b", "optionalDependencies": {"c": "link:../c"}}`,
		"libs/c/package.json":   `{"name": "c", "peerDependencies": {"alias": "workspace:a@*"}}`,
		"libs/d/README":         "not a package",
		"node_modules/x/p.json": "{}",
	}
	for name, tc := range map[string]struct {
		files map[string]string
		cmd   []string
	}{
		"npm":        {map[string]string{"package-lock.json": "{}", "package.json": `{"workspaces": ["./libs/*/"]}`}, []string{"npm", "test", "--workspace", "a"}},
		"shrinkwrap": {map[string]string{"npm-shrinkwrap.json": "{}", "package.json": `{"workspaces": ["libs/*"]}`}, []string{"npm", "test", "--workspace", "a"}},
		"yarn":       {map[string]string{"yarn.lock": "", "package.json": `{"workspaces": {"packages": ["libs/**"]}}`}, []string{"yarn", "workspace", "a", "test"}},
		"pnpm":       {map[string]string{"pnpm-lock.yaml": "", "pnpm-workspace.yaml": "packages: [libs/*]"}, []string{"pnpm", "--filter", "a", "test"}},
	} {
		dir := t.TempDir()
		write(t, dir, pkgs)
		write(t, dir, tc.files)
		units, err := Adapter{}.Units(dir)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		slices.SortFunc(units, func(x, y gen.Unit) int { return strings.Compare(x.ID, y.ID) })
		want := []gen.Unit{
			{ID: "a", Source: []string{"libs/a/**"}, Deps: []string{"b"}, Command: tc.cmd},
			{ID: "b", Source: []string{"libs/b/**"}, Deps: []string{"c"}},
			{ID: "c", Source: []string{"libs/c/**"}, Deps: []string{"a"}},
		}
		if len(units) != len(want) {
			t.Fatalf("%s: units %+v", name, units)
		}
		for i, u := range units {
			if u.ID != want[i].ID || !slices.Equal(u.Source, want[i].Source) || !slices.Equal(u.Deps, want[i].Deps) {
				t.Errorf("%s: unit %+v, want %+v", name, u, want[i])
			}
		}
		if !slices.Equal(units[0].Command, tc.cmd) {
			t.Errorf("%s: command %v, want %v", name, units[0].Command, tc.cmd)
		}
	}
}

// Every problem fails closed: an error, never a partial list.
func TestErrors(t *testing.T) {
	lock := map[string]string{"package-lock.json": "{}"}
	for name, tc := range map[string]struct {
		files map[string]string
		want  string
	}{
		"no lockfile":    {map[string]string{"package.json": `{"workspaces": ["a"]}`}, "cannot tell the package manager"},
		"two managers":   {map[string]string{"yarn.lock": "", "package-lock.json": "{}"}, "lockfiles of npm and yarn"},
		"no pnpm file":   {map[string]string{"pnpm-lock.yaml": ""}, "pnpm-workspace.yaml"},
		"bad pnpm file":  {map[string]string{"pnpm-lock.yaml": "", "pnpm-workspace.yaml": "packages: {"}, "pnpm-workspace.yaml"},
		"no package":     {lock, "package.json"},
		"bad package":    {map[string]string{"package-lock.json": "{}", "package.json": "{"}, "package.json"},
		"bad workspaces": {map[string]string{"package-lock.json": "{}", "package.json": `{"workspaces": 1}`}, "want a list of globs"},
		"no workspaces":  {map[string]string{"package-lock.json": "{}", "package.json": `{}`}, "no workspaces declared"},
		"escaping glob":  {map[string]string{"package-lock.json": "{}", "package.json": `{"workspaces": ["../x"]}`}, "workspace glob"},
		"no name": {map[string]string{"package-lock.json": "{}", "package.json": `{"workspaces": ["a"]}`, "a/package.json": `{}`},
			"a/package.json: no name"},
		"bad member": {map[string]string{"package-lock.json": "{}", "package.json": `{"workspaces": ["a"]}`, "a/package.json": `[`},
			"package.json"},
		"unknown workspace dep": {map[string]string{"package-lock.json": "{}", "package.json": `{"workspaces": ["a"]}`, "a/package.json": `{"name": "a", "dependencies": {"z": "workspace:*"}}`},
			`z: "workspace:*" is not a workspace package`},
		"file outside": {map[string]string{"package-lock.json": "{}", "package.json": `{"workspaces": ["a"]}`, "a/package.json": `{"name": "a", "dependencies": {"z": "file:../../z"}}`},
			"is not a workspace package"},
		"file not a package": {map[string]string{"package-lock.json": "{}", "package.json": `{"workspaces": ["a"]}`, "a/package.json": `{"name": "a", "dependencies": {"z": "file:../vendor/z.tgz"}}`},
			"add it to the workspaces"},
	} {
		dir := t.TempDir()
		write(t, dir, tc.files)
		if units, err := (Adapter{}).Units(dir); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: units %v, error %v; want %q", name, units, err, tc.want)
		}
	}
	if runtime.GOOS != "windows" { // unreadable directories
		for sub, mode := range map[string]os.FileMode{"a": 0o600, "b": 0o000} { // a stat, a walk error
			dir := t.TempDir()
			write(t, dir, map[string]string{"package-lock.json": "{}", "package.json": `{"workspaces": ["a"]}`, "a/package.json": `{"name": "a"}`, "b/x": ""})
			a := filepath.Join(dir, sub)
			_ = os.Chmod(a, mode)
			t.Cleanup(func() { _ = os.Chmod(a, 0o755) })
			if _, err := (Adapter{}).Units(dir); err == nil {
				t.Errorf("mode %v: want an error", mode)
			}
		}
	}
}

// A package directory named with glob metacharacters gets an escaped glob,
// so a change to its files changes its test's fingerprint; one no contract
// glob can express fails closed.
func TestFalseHitMetacharacterDirectory(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"package-lock.json":      "{}",
		"package.json":           `{"workspaces": ["libs/*"]}`,
		"libs/a[1]/package.json": `{"name": "a"}`,
		"libs/a[1]/index.js":     "a",
		"libs/a1/package.json":   `{"name": "a1"}`,
		"aval.yaml":              "apiVersion: aval.trianalab.io/v1\nkind: TestSet\ndefaults:\n  scope: {dependencies: [package-lock.json]}\n  cacheability: hermetic\n",
	})
	_, out := generate(t, dir)
	if !strings.Contains(out, "libs/a[[]1]/**") {
		t.Fatalf("no escaped glob:\n%s", out)
	}
	write(t, dir, map[string]string{"aval.yaml": out})
	before := digests(t, dir)
	write(t, dir, map[string]string{"libs/a[1]/index.js": "changed"})
	if after := digests(t, dir); after["js.a"] == before["js.a"] || after["js.a1"] != before["js.a1"] {
		t.Errorf("before %v, after %v: want only js.a to change", before, after)
	}
	if runtime.GOOS == "windows" { // no backslash in a file name
		return
	}
	write(t, dir, map[string]string{`libs/b\c/package.json`: `{"name": "b"}`})
	if _, err := (Adapter{}).Units(dir); err == nil || !strings.Contains(err.Error(), `package b: "libs/b\\c": a contract glob cannot express`) {
		t.Errorf("error %v; want one naming libs/b\\c", err)
	}
}
