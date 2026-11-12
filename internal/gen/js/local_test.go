package js

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/internal/gen"
)

// Edges package.json does not name: api's tsconfig references types and
// extends the root tsconfig.base.json, which extends @acme/tsconfig; web
// imports i18n by a relative path. A change to any of them re-runs the
// dependent test and nothing else.
func TestFalseHitLocalEdges(t *testing.T) {
	before := digests(t, fixture)
	for file, want := range map[string][]string{
		"packages/types/src/index.ts": {"js.acme.api", "js.acme.types"},
		"tsconfig.base.json":          {"js.acme.api"},
		"packages/tsconfig/base.json": {"js.acme.api", "js.acme.tsconfig", "js.acme.web"},
		"packages/i18n/src/en.ts":     {"js.acme.i18n", "js.acme.web"},
	} {
		dir := copyFixture(t)
		write(t, dir, map[string]string{file: "changed"})
		after := digests(t, dir)
		for name := range before {
			if changed := after[name] != before[name]; changed != slices.Contains(want, name) {
				t.Errorf("%s changed: %s changed = %v", file, name, changed)
			}
		}
	}
}

// A new reference or relative import is drift that --check reports.
func TestFalseHitNewLocalEdgeIsDrift(t *testing.T) {
	for file, data := range map[string]string{
		"apps/api/tsconfig.json": `{"references": [{"path": "../../packages/ui"}]}`,
		"apps/api/src/server.ts": `export * from "../../../packages/ui/src/button";`,
		"apps/api/src/lazy.mjs":  "const m = await import(\n  '../../../packages/ui/src/button.tsx')",
		"apps/api/src/old.cjs":   "require ( `../../../packages/ui` )",
	} {
		dir := copyFixture(t)
		write(t, dir, map[string]string{file: data})
		before, after := generate(t, dir)
		if d := gen.Diff("a", "b", []byte(before), []byte(after)); !strings.Contains(d, "+      - js.acme.ui") {
			t.Errorf("%s: no drift:\n%s", file, d)
		}
	}
}

func TestLocalEdges(t *testing.T) {
	files := map[string]string{
		"package-lock.json":          "{}",
		"package.json":               `{"workspaces": ["libs/*", "libs/a/nested"]}`,
		"libs/a/package.json":        `{"name": "a"}`,
		"libs/b/package.json":        `{"name": "@s/b"}`,
		"libs/c/package.json":        `{"name": "c"}`,
		"libs/d/package.json":        `{"name": "d"}`,
		"libs/a/nested/package.json": `{"name": "n"}`,
		"libs/a/nested/x.ts":         `import "../../d/x"`, // n's edge, not a's
		"libs/a/node_modules/m/i.js": `import "../../../../outside"`,
		"libs/a/.cache/i.js":         `import "../../../outside"`,
		"libs/a/src/i.ts":            `import { x } from "./own"; import y from '..';`,
		"libs/a/tsconfig.json": `{
  "extends": ["@s/b/conf/base", "c", "./tsconfig.json", "external/base.json"], // "x" /* y */
  "references": [{"path": "./"}],
}`,
		"libs/b/conf/base.json": `{"extends": "../../../shared/ts", "references": [{"path": "../../../nowhere"}]}`,
		"shared/ts.json":        `{"extends": "../libs/a/tsconfig.json", "compilerOptions": {"a\"b": "\\/*"}}`,
		"libs/c/tsconfig.json":  `{}`,
	}
	dir := t.TempDir()
	write(t, dir, files)
	units, err := Adapter{}.Units(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]gen.Unit{}
	for _, u := range units {
		got[u.ID] = u
	}
	if a := got["a"]; !slices.Equal(a.Deps, []string{"@s/b", "c"}) || !slices.Equal(a.Source, []string{"libs/a/**", "shared/ts.json"}) {
		t.Errorf("a: %+v", a)
	}
	if n := got["n"]; !slices.Equal(n.Deps, []string{"d"}) {
		t.Errorf("n: %+v", n)
	}
}

// Every unresolvable or out-of-package local reference fails closed.
func TestLocalErrors(t *testing.T) {
	base := map[string]string{"package-lock.json": "{}", "package.json": `{"workspaces": ["a", "b"]}`, "a/package.json": `{"name": "a"}`, "b/package.json": `{"name": "b"}`}
	for name, tc := range map[string]struct {
		file, data, want string
	}{
		"outside packages": {"a/src/x.ts", "\n\nimport { s } from '../../scripts/s'", `a/src/x.ts:3: "../../scripts/s": scripts/s is outside every workspace package`},
		"outside root":     {"a/x.js", `require("../../x")`, `a/x.js:1: "../../x": outside the contract root`},
		"computed":         {"a/x.js", "import(`../b/${n}`)", "cannot resolve a computed relative specifier"},
		"bad tsconfig":     {"a/tsconfig.json", `{"references": [}`, "a/tsconfig.json: invalid character"},
		"open comment":     {"a/tsconfig.json", `{} /*`, "a/tsconfig.json:"},
		"bad extends":      {"a/tsconfig.json", `{"extends": 1}`, "extends: want a string"},
		"absolute extends": {"a/tsconfig.json", `{"extends": "/etc/ts.json"}`, `extends: "/etc/ts.json": an absolute path`},
		"escaping extends": {"a/tsconfig.json", `{"extends": "../../ts"}`, `extends: "../../ts": outside the contract root`},
		"missing extends":  {"a/tsconfig.json", `{"extends": "./base"}`, "base.json"},
		"glob extends":     {"a/tsconfig.json", `{"extends": "../t[1].json"}`, "a glob character"},
		"chained error":    {"a/tsconfig.json", `{"extends": "../base.json"}`, "base.json"},
		"outside ref":      {"a/tsconfig.json", `{"references": [{"path": "../shared"}]}`, `references: "../shared" is not in a workspace package`},
		"absolute ref":     {"a/tsconfig.json", `{"references": [{"path": "/b"}]}`, `references: "/b" is not in a workspace package`},
	} {
		dir := t.TempDir()
		write(t, dir, base)
		write(t, dir, map[string]string{tc.file: tc.data, "base.json": `{"extends": 1}`})
		if units, err := (Adapter{}).Units(dir); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: units %v, error %v; want %q", name, units, err, tc.want)
		}
	}
	if runtime.GOOS != "windows" { // unreadable files and directories in a package
		for _, f := range []string{"a/x.ts", "a/tsconfig.json"} {
			dir := t.TempDir()
			write(t, dir, base)
			write(t, dir, map[string]string{f: "{}"})
			p := filepath.Join(dir, f)
			_ = os.Chmod(p, 0o000)
			t.Cleanup(func() { _ = os.Chmod(p, 0o755) })
			if _, err := (Adapter{}).Units(dir); err == nil {
				t.Errorf("%s unreadable: want an error", f)
			}
		}
	}
}

// A package directory that vanished after the workspace was listed.
func TestScanWalkError(t *testing.T) {
	if _, _, err := (workspace{root: t.TempDir()}).scan("gone"); err == nil {
		t.Error("want an error")
	}
}

func TestJSONC(t *testing.T) {
	for in, want := range map[string]string{
		`{"a": 1, // c` + "\n}":           `{"a": 1}`,
		`[1, /* x */ 2,]`:                 `[1,  2]`,
		`{"s": "// not \" /* a comment"}`: `{"s": "// not \" /* a comment"}`,
		`"open`:                           `"open`,
		`// end`:                          ``,
	} {
		if got := string(jsonc([]byte(in))); got != want {
			t.Errorf("jsonc(%q) = %q, want %q", in, got, want)
		}
	}
}
