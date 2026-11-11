package fingerprint

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// pathsFixture is newFixture whose contract declares ~/.npmrc (a file),
// ~/.config/app (a directory), ~/.absent and $CFG/app, with HOME and CFG
// pointing at temporary directories outside the project.
type pathsFixture struct {
	*fixture
	home, cfg string
}

func newPathsFixture(t *testing.T) *pathsFixture {
	t.Helper()
	f := &pathsFixture{fixture: newFixture(t), home: t.TempDir(), cfg: t.TempDir()}
	t.Setenv("HOME", f.home)
	f.env["CFG"] = f.cfg
	f.c.Environment.Paths = []string{"~/.npmrc", "~/.config/app", "~/.absent", "$CFG/app"}
	f.writeAt(f.home, ".npmrc", "registry=https://r.example\n")
	f.writeAt(f.home, ".config/app/config.yaml", "x: 1\n")
	f.writeAt(f.home, ".config/app/sub/state.json", "{}\n")
	f.writeAt(f.cfg, "app", "cfg\n")
	return f
}

func (f *pathsFixture) writeAt(root, rel, body string) {
	f.t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	f.do(os.MkdirAll(filepath.Dir(p), 0o755))
	f.do(os.WriteFile(p, []byte(body), 0o644))
}

func (f *pathsFixture) at(rel string) string { return filepath.Join(f.home, filepath.FromSlash(rel)) }

// TestPathsFalseHits is the environment.paths part of the false-hit
// corpus: a change to declared content outside the tree changes the
// fingerprint; where it lives and how the entries are ordered does not;
// what cannot be hashed fails, never as absent.
func TestPathsFalseHits(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(*pathsFixture)
		change func(*pathsFixture)
		want   outcome
	}{
		{name: "file content edited", change: func(f *pathsFixture) { f.writeAt(f.home, ".npmrc", "registry=https://evil.example\n") }, want: differ},
		{name: "file added in directory", change: func(f *pathsFixture) { f.writeAt(f.home, ".config/app/new", "") }, want: differ},
		{name: "file removed from directory", change: func(f *pathsFixture) { f.do(os.Remove(f.at(".config/app/sub/state.json"))) }, want: differ},
		{name: "file renamed in directory", change: func(f *pathsFixture) {
			f.do(os.Rename(f.at(".config/app/config.yaml"), f.at(".config/app/config.yml")))
		}, want: differ},
		{name: "nested file edited", change: func(f *pathsFixture) { f.writeAt(f.home, ".config/app/sub/state.json", "{\"a\":1}\n") }, want: differ},
		{name: "exec bit on file", change: func(f *pathsFixture) { f.do(os.Chmod(f.at(".npmrc"), 0o755)) }, want: differ},
		{name: "exec bit in directory", change: func(f *pathsFixture) { f.do(os.Chmod(f.at(".config/app/config.yaml"), 0o744)) }, want: differ},
		{name: "absent to empty file", change: func(f *pathsFixture) { f.writeAt(f.home, ".absent", "") }, want: differ},
		{name: "absent to empty directory", change: func(f *pathsFixture) { f.do(os.Mkdir(f.at(".absent"), 0o755)) }, want: differ},
		{name: "absent to file", change: func(f *pathsFixture) { f.writeAt(f.home, ".absent", "x") }, want: differ},
		{name: "file to absent", change: func(f *pathsFixture) { f.do(os.Remove(f.at(".npmrc"))) }, want: differ},
		{name: "file replaced by directory with the same content", change: func(f *pathsFixture) {
			data, err := os.ReadFile(f.at(".npmrc"))
			f.do(err)
			f.do(os.Remove(f.at(".npmrc")))
			f.writeAt(f.home, ".npmrc/.npmrc", string(data))
		}, want: differ},
		{name: "empty file replaced by empty directory", setup: func(f *pathsFixture) { f.writeAt(f.home, ".absent", "") }, change: func(f *pathsFixture) {
			f.do(os.Remove(f.at(".absent")))
			f.do(os.Mkdir(f.at(".absent"), 0o755))
		}, want: differ},
		{name: "variable unset to set", setup: func(f *pathsFixture) { delete(f.env, "CFG") }, change: func(f *pathsFixture) { f.env["CFG"] = f.t.TempDir() }, want: differ},
		{name: "variable points at other content", change: func(f *pathsFixture) {
			other := f.t.TempDir()
			f.writeAt(other, "app", "other\n")
			f.env["CFG"] = other
		}, want: differ},
		{name: "entry added", change: func(f *pathsFixture) { f.c.Environment.Paths = append(f.c.Environment.Paths, "~/.gitconfig") }, want: differ},

		{name: "other HOME, same content", change: func(f *pathsFixture) {
			other := f.t.TempDir()
			f.do(os.CopyFS(other, os.DirFS(f.home)))
			f.do(os.RemoveAll(f.home))
			f.t.Setenv("HOME", other)
		}, want: same},
		{name: "other HOME, files created in reverse order", change: func(f *pathsFixture) {
			other := f.t.TempDir()
			f.writeAt(other, ".config/app/sub/state.json", "{}\n")
			f.writeAt(other, ".config/app/config.yaml", "x: 1\n")
			f.writeAt(other, ".npmrc", "registry=https://r.example\n")
			f.t.Setenv("HOME", other)
		}, want: same},
		{name: "entry order and duplicates", change: func(f *pathsFixture) {
			slices.Reverse(f.c.Environment.Paths)
			f.c.Environment.Paths = append(f.c.Environment.Paths, "~/.npmrc")
		}, want: same},
		{name: "mtime", change: func(f *pathsFixture) { f.writeAt(f.home, ".npmrc", "registry=https://r.example\n") }, want: same},
		{name: "file next to the entry", change: func(f *pathsFixture) { f.writeAt(f.home, ".config/other", "x") }, want: same},

		{name: "symlink at the entry", change: func(f *pathsFixture) {
			f.do(os.Rename(f.at(".npmrc"), f.at("real")))
			f.do(os.Symlink("real", f.at(".npmrc")))
		}, want: failClosed},
		{name: "symlink at an absent entry", change: func(f *pathsFixture) { f.do(os.Symlink("nowhere", f.at(".absent"))) }, want: failClosed},
		{name: "symlink in a declared directory", change: func(f *pathsFixture) {
			f.do(os.Symlink(f.at(".npmrc"), f.at(".config/app/link")))
		}, want: failClosed},
		{name: "unreadable file", change: func(f *pathsFixture) { f.do(os.Chmod(f.at(".npmrc"), 0)) }, want: failClosed},
		{name: "unreadable file in a declared directory", change: func(f *pathsFixture) { f.do(os.Chmod(f.at(".config/app/config.yaml"), 0)) }, want: failClosed},
		{name: "unreadable directory", change: func(f *pathsFixture) {
			f.do(os.Chmod(f.at(".config/app/sub"), 0))
			f.t.Cleanup(func() { _ = os.Chmod(f.at(".config/app/sub"), 0o755) }) // so TempDir can remove it
		}, want: failClosed},
		{name: "newline in a name in a declared directory", change: func(f *pathsFixture) { f.writeAt(f.home, ".config/app/a\nb", "") }, want: failClosed},
		{name: "parent is a file", change: func(f *pathsFixture) { f.c.Environment.Paths = append(f.c.Environment.Paths, "~/.npmrc/x") }, want: failClosed},
		{name: "HOME unset", change: func(f *pathsFixture) { f.t.Setenv("HOME", "") }, want: failClosed},
		{name: "HOME relative", change: func(f *pathsFixture) { f.t.Setenv("HOME", "home") }, want: failClosed},
		{name: "variable empty", change: func(f *pathsFixture) { f.env["CFG"] = "" }, want: failClosed},
		{name: "variable relative", change: func(f *pathsFixture) { f.env["CFG"] = "cfg" }, want: failClosed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newPathsFixture(t)
			if tc.setup != nil {
				tc.setup(f)
			}
			base, err := f.compute()
			if err != nil {
				t.Fatal(err)
			}
			tc.change(f)
			if tc.want == failClosed && os.Geteuid() == 0 && strings.HasPrefix(tc.name, "unreadable") {
				t.Skip("root reads unreadable files")
			}
			got, err := f.compute()
			switch {
			case tc.want == failClosed:
				if err == nil {
					t.Fatalf("want an error, got\n%s", got.Manifest)
				}
				if !strings.Contains(err.Error(), "environment.paths ") {
					t.Fatalf("error does not name the entry: %v", err)
				}
			case err != nil:
				t.Fatal(err)
			case (got.Digest != base.Digest) != (tc.want == differ):
				t.Fatalf("want %v, got\n--- base\n%s--- got\n%s", tc.want, base.Manifest, got.Manifest)
			}
		})
	}
}

// TestPathsInsideTheTree: an entry that leads into the scope root is a
// contract error, present or not: scope globs declare those.
func TestPathsInsideTheTree(t *testing.T) {
	for _, entry := range []string{"$TREE/main.go", "$TREE/pkg", "$TREE/pkg/a/a.go", "$TREE/missing/x"} {
		f := newPathsFixture(t)
		f.env["TREE"] = f.dir
		f.c.Environment.Paths = []string{entry}
		_, err := f.compute()
		if !errors.Is(err, ErrContract) || !strings.Contains(err.Error(), "inside the contract tree") {
			t.Errorf("%s: want a contract error, got %v", entry, err)
		}
	}
}

// pathsGolden pins the path lines of the current manifest version: a file,
// a directory, an absent entry and an unset variable, after the env lines.
var pathsGolden = filepath.Join(filepath.Dir(goldenFile(Version)), "paths-"+filepath.Base(goldenFile(Version)))

func TestPathsManifestGolden(t *testing.T) {
	f := newPathsFixture(t)
	f.do(os.Chmod(f.at(".config/app/config.yaml"), 0o755))
	f.c.Environment.Paths = append(f.c.Environment.Paths, "${UNSET}/app")
	fp, err := f.compute()
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile(pathsGolden)
	if errors.Is(err, fs.ErrNotExist) && *update {
		f.do(os.WriteFile(pathsGolden, []byte(fp.Manifest), 0o644))
		return
	}
	if err != nil || string(golden) != fp.Manifest {
		t.Fatalf("paths manifest changed without a version bump (%v):\n--- golden\n%s--- got\n%s", err, golden, fp.Manifest)
	}
}
