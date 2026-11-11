package fingerprint

import (
	"context"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TrianaLab/aval/internal/contract"
)

// The false-hit corpus (#14): every known kind of change, applied to one
// base project, with the outcome it must have.
//
//   - differ: a relevant change. Same fingerprint here is a false hit.
//   - same: an irrelevant change. A different fingerprint is a needless miss.
//   - failClosed: equivalence cannot be proven, so Compute must error.
//   - gap: a relevant change that Aval cannot see by design (undeclared
//     input). The row asserts the fingerprint stays the same, so the gap is
//     documented here and in docs/contract.md, not hidden. If a future
//     change closes a gap, flip the row to differ.
//   - refused: a relevant change behind a declared name (a repushed tag).
//     The fingerprint cannot see it, so the row asserts it stays the same
//     and that Compute reports an unacknowledged mutable reference, which
//     makes every lookup a miss (internal/pinning, docs/pinning.md).
//
// A new manifest section needs a differ row: TestFalseHitCorpus fails if a
// section of the base manifest is changed by no differ row. A new attribute
// inside an existing section, or a new contract field, is not detected: add
// its row by hand.
type outcome int

const (
	differ outcome = iota
	same
	failClosed
	gap
	refused
)

// corpusYAML parses to exactly testContract(), with comments, odd
// formatting, shuffled keys and unsorted, duplicated set-like lists.
const corpusYAML = `
# comments and key order do not matter
cacheability: controlled-environment
environment:
  env: [MODE, DB_IMAGE, MODE]
  toolchain:
    docker: "27"
    go: "1.25"
scope:
  dependencies: [go.sum]
  exclude:   ["vendor/**"]
  source:
    - compose.yaml
    - "**/*.go"
    - "**/*.go"
runner: {command: [go, test, ./...]}
metadata: {name: it}
kind: Test
apiVersion: aval.trianalab.io/v1
`

// fixture is one project checkout: a tree, its contract and its environment.
type fixture struct {
	t   *testing.T
	dir string
	c   *contract.Contract
	env map[string]string
	// outputs are the --result and --report paths passed to Compute.
	outputs []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	// A short root, not t.TempDir(): Unix socket paths are limited to ~104 bytes.
	dir, err := os.MkdirTemp("", "fh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	f := &fixture{t: t, dir: dir, c: testContract(), env: map[string]string{"DB_IMAGE": "postgres:16"}}
	for name, body := range baseFiles {
		f.write(name, body)
	}
	return f
}

func (f *fixture) path(name string) string { return filepath.Join(f.dir, filepath.FromSlash(name)) }

func (f *fixture) write(name, body string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.path(name)), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(f.path(name), []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) do(err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) remove(name string) { f.t.Helper(); f.do(os.Remove(f.path(name))) }
func (f *fixture) rename(from, to string) {
	f.t.Helper()
	f.do(os.MkdirAll(filepath.Dir(f.path(to)), 0o755))
	f.do(os.Rename(f.path(from), f.path(to)))
}
func (f *fixture) chmod(name string, m os.FileMode) { f.t.Helper(); f.do(os.Chmod(f.path(name), m)) }
func (f *fixture) symlink(target, name string)      { f.t.Helper(); f.do(os.Symlink(target, f.path(name))) }

func (f *fixture) parse(yaml string) {
	f.t.Helper()
	c, err := contract.Parse([]byte(yaml))
	f.do(err)
	f.c = c
}

func (f *fixture) compute() (Fingerprint, error) {
	return Compute(context.Background(), f.dir, f.c, func(k string) (string, bool) { v, ok := f.env[k]; return v, ok }, f.outputs...)
}

type corpusCase struct {
	name   string
	setup  func(*fixture) // optional, applied before the base fingerprint
	change func(*fixture)
	want   outcome
}

var corpus = []corpusCase{
	// Files: content, rename, add, delete, mode.
	{name: "source content", change: func(f *fixture) { f.write("main.go", "package main // changed\n") }, want: differ},
	{name: "nested source content", change: func(f *fixture) { f.write("pkg/a/a.go", "package a2\n") }, want: differ},
	{name: "non-code source content", change: func(f *fixture) { f.write("compose.yaml", "services: {db: {}}\n") }, want: differ},
	{name: "lockfile content", change: func(f *fixture) { f.write("go.sum", "example.com/x v1.0.1 h1:def\n") }, want: differ},
	{name: "single byte appended", change: func(f *fixture) { f.write("main.go", baseFiles["main.go"]+"\n") }, want: differ},
	{name: "content emptied", change: func(f *fixture) { f.write("main.go", "") }, want: differ},
	{name: "rename in directory", change: func(f *fixture) { f.rename("pkg/a/a.go", "pkg/a/renamed.go") }, want: differ},
	{name: "move to another directory", change: func(f *fixture) { f.rename("pkg/a/a.go", "pkg/b/a.go") }, want: differ},
	{name: "case-only rename", change: func(f *fixture) { f.rename("pkg/a/a.go", "pkg/a/A.go") }, want: differ},
	{name: "swap contents of two files", change: func(f *fixture) {
		f.write("main.go", baseFiles["pkg/a/a.go"])
		f.write("pkg/a/a.go", baseFiles["main.go"])
	}, want: differ},
	{name: "source file added", change: func(f *fixture) { f.write("pkg/b/b.go", "package b\n") }, want: differ},
	{name: "empty source file added", change: func(f *fixture) { f.write("pkg/b/b.go", "") }, want: differ},
	{name: "dotfile added", change: func(f *fixture) { f.write(".hidden.go", "package main\n") }, want: differ},
	{name: "file in dot-directory added", change: func(f *fixture) { f.write(".gen/x.go", "package gen\n") }, want: differ},
	{name: "source file deleted", change: func(f *fixture) { f.remove("pkg/a/a.go") }, want: differ},
	{name: "exec bit set", change: func(f *fixture) { f.chmod("main.go", 0o755) }, want: differ},
	{name: "owner exec bit only", change: func(f *fixture) { f.chmod("main.go", 0o744) }, want: differ},
	{name: "group exec bit only", change: func(f *fixture) { f.chmod("main.go", 0o654) }, want: differ},
	{name: "other exec bit only", change: func(f *fixture) { f.chmod("main.go", 0o645) }, want: differ},
	{name: "LF to CRLF", change: func(f *fixture) { f.write("main.go", strings.ReplaceAll(baseFiles["main.go"], "\n", "\r\n")) }, want: differ},
	{name: "exec bit cleared", setup: func(f *fixture) { f.chmod("main.go", 0o755) }, change: func(f *fixture) { f.chmod("main.go", 0o644) }, want: differ},
	{name: "unicode NFC to NFD rename", setup: func(f *fixture) { f.write("caf\u00e9.go", "package main\n") }, change: func(f *fixture) {
		// Paths are hashed as the bytes the filesystem returns, never
		// normalized: NFC and NFD spellings of a name are different inputs.
		f.rename("caf\u00e9.go", "cafe\u0301.go")
	}, want: differ},
	{name: "dependency under an excluded path", setup: func(f *fixture) {
		f.c.Scope.Dependencies = append(f.c.Scope.Dependencies, "vendor/modules.txt")
		f.write("vendor/modules.txt", "a\n")
	}, change: func(f *fixture) { f.write("vendor/modules.txt", "b\n") }, want: differ},
	{name: "excluded file re-included", change: func(f *fixture) { f.c.Scope.Exclude = nil }, want: differ},

	// Declared environment: toolchain and env vars.
	{name: "toolchain value", change: func(f *fixture) { f.c.Environment.Toolchain["go"] = "1.26" }, want: differ},
	{name: "toolchain key added", change: func(f *fixture) { f.c.Environment.Toolchain["node"] = "22" }, want: differ},
	{name: "toolchain key removed", change: func(f *fixture) { delete(f.c.Environment.Toolchain, "docker") }, want: differ},
	{name: "toolchain value with an embedded newline", change: func(f *fixture) {
		f.c.Environment.Toolchain["go"] = "1.25\nsource " + sum([]byte("x")) + " - x.go"
	}, want: differ},
	{name: "matrix leg of another platform", setup: func(f *fixture) {
		f.write("go.mod", "module x\n")
		f.c = platformLeg(f.t, "unit-darwin")
	},
		change: func(f *fixture) { f.c = platformLeg(f.t, "unit-linux") }, want: differ},
	{name: "env value", change: func(f *fixture) { f.env["DB_IMAGE"] = "postgres:17" }, want: differ},
	{name: "env unset to set", change: func(f *fixture) { f.env["MODE"] = "fast" }, want: differ},
	{name: "env set to unset", change: func(f *fixture) { delete(f.env, "DB_IMAGE") }, want: differ},
	{name: "env unset to set-but-empty", change: func(f *fixture) { f.env["MODE"] = "" }, want: differ},
	{name: "env set-but-empty to unset", setup: func(f *fixture) { f.env["MODE"] = "" }, change: func(f *fixture) { delete(f.env, "MODE") }, want: differ},
	{name: "env name declared", change: func(f *fixture) { f.c.Environment.Env = append(f.c.Environment.Env, "CGO_ENABLED") }, want: differ},
	{name: "env name undeclared", change: func(f *fixture) { f.c.Environment.Env = []string{"DB_IMAGE"} }, want: differ},

	// Command and contract fields.
	{name: "command argument changed", change: func(f *fixture) { f.c.Runner.Command = []string{"go", "test", "./pkg/..."} }, want: differ},
	{name: "command argument added", change: func(f *fixture) { f.c.Runner.Command = []string{"go", "test", "-race", "./..."} }, want: differ},
	{name: "command argv reordered", change: func(f *fixture) { f.c.Runner.Command = []string{"go", "./...", "test"} }, want: differ},
	{name: "command argv joined", change: func(f *fixture) { f.c.Runner.Command = []string{"go", "test ./..."} }, want: differ},
	{name: "metadata.name", change: func(f *fixture) { f.c.Metadata.Name = "other" }, want: differ},
	{name: "cacheability", change: func(f *fixture) { f.c.Cacheability = contract.Hermetic }, want: differ},
	{name: "scope.source glob added", change: func(f *fixture) { f.c.Scope.Source = append(f.c.Scope.Source, "main.go") }, want: differ},
	{name: "scope.exclude glob added", change: func(f *fixture) { f.c.Scope.Exclude = append(f.c.Scope.Exclude, "none/**") }, want: differ},
	{name: "scope.optionalSource glob added", change: func(f *fixture) { f.c.Scope.OptionalSource = []string{"**/*testdata*.yaml"} }, want: differ},
	{name: "glob moved from source to optionalSource", change: func(f *fixture) {
		f.c.Scope.Source, f.c.Scope.OptionalSource = []string{"**/*.go"}, []string{"compose.yaml"}
	}, want: differ},
	{name: "scope.optionalSource reordered and repeated", setup: func(f *fixture) {
		f.c.Scope.OptionalSource = []string{"a/**", "b/**"}
	}, change: func(f *fixture) { f.c.Scope.OptionalSource = []string{"b/**", "a/**", "b/**"} }, want: same},
	{name: "first file matching an optional glob added", setup: func(f *fixture) {
		f.c.Scope.OptionalSource = []string{"**/*testdata*.yaml"}
	}, change: func(f *fixture) { f.write("pkg/a/a_testdata.yaml", "k: v\n") }, want: differ},
	{name: "file matching an optional glob changed", setup: func(f *fixture) {
		f.c.Scope.OptionalSource = []string{"**/*testdata*.yaml"}
		f.write("pkg/a/a_testdata.yaml", "k: v\n")
	}, change: func(f *fixture) { f.write("pkg/a/a_testdata.yaml", "k: w\n") }, want: differ},
	{name: "last file matching an optional glob deleted", setup: func(f *fixture) {
		f.c.Scope.OptionalSource = []string{"**/*testdata*.yaml"}
		f.write("pkg/a/a_testdata.yaml", "k: v\n")
	}, change: func(f *fixture) { f.remove("pkg/a/a_testdata.yaml") }, want: differ},
	{name: "excluded file matching an optional glob added", setup: func(f *fixture) {
		f.c.Scope.OptionalSource = []string{"**/*testdata*.yaml"}
	}, change: func(f *fixture) { f.write("vendor/v/v_testdata.yaml", "k: v\n") }, want: same},
	{name: "optional file excluded only by a trailing **", setup: func(f *fixture) {
		f.c.Scope.OptionalSource = []string{"gen"}
		f.c.Scope.Exclude = append(f.c.Scope.Exclude, "gen/**")
	}, change: func(f *fixture) { f.write("gen", "x\n") }, want: failClosed},
	{name: "scope.dependencies glob added", change: func(f *fixture) { f.c.Scope.Dependencies = append(f.c.Scope.Dependencies, "compose.yaml") }, want: differ},
	{name: "scope.allowMutable added", change: func(f *fixture) {
		f.c.Scope.AllowMutable = []contract.AllowMutable{{Ref: "postgres:16", Reason: "scanned nightly"}}
	}, want: differ},
	{name: "scope.allowMutable reason changed", setup: func(f *fixture) {
		f.c.Scope.AllowMutable = []contract.AllowMutable{{Ref: "postgres:16", Reason: "a"}}
	}, change: func(f *fixture) { f.c.Scope.AllowMutable[0].Reason = "b" }, want: differ},
	{name: "scope.allowMutable files changed", setup: func(f *fixture) {
		f.c.Scope.AllowMutable = []contract.AllowMutable{{Files: []string{"compose.yaml"}, Ref: "postgres:16", Reason: "a"}}
	}, change: func(f *fixture) { f.c.Scope.AllowMutable[0].Files = []string{"**/*.yaml"} }, want: differ},
	// A result without outputs is never reused for a contract that restores them.
	{name: "outputs added", change: func(f *fixture) { f.c.Outputs = []string{"coverage.xml"} }, want: differ},
	{name: "outputs glob changed", setup: func(f *fixture) { f.c.Outputs = []string{"coverage.xml"} }, change: func(f *fixture) { f.c.Outputs = []string{"junit.xml"} }, want: differ},

	{name: "environment.probe added", change: func(f *fixture) {
		f.c.Environment.Probe = map[string][]string{"go": {"go", "version"}}
	}, want: differ},
	{name: "environment.probe argv changed", setup: func(f *fixture) {
		f.c.Environment.Probe = map[string][]string{"go": {"go", "version"}}
	}, change: func(f *fixture) { f.c.Environment.Probe["go"] = []string{"go", "env", "GOVERSION"} }, want: differ},
	{name: "environment.probe argv order", setup: func(f *fixture) {
		f.c.Environment.Probe = map[string][]string{"go": {"go", "version"}}
	}, change: func(f *fixture) { f.c.Environment.Probe["go"] = []string{"version", "go"} }, want: differ},

	// Irrelevant changes.
	{name: "out-of-scope file edited", change: func(f *fixture) { f.write("README.md", "edited\n") }, want: same},
	{name: "out-of-scope file added", change: func(f *fixture) { f.write("docs/new.md", "new\n") }, want: same},
	{name: "excluded file edited", change: func(f *fixture) { f.write("vendor/v/v.go", "package v // edited\n") }, want: same},
	{name: "nested excluded file added", change: func(f *fixture) { f.write("vendor/a/b/c.go", "package c\n") }, want: same},
	{name: "exclude beats an explicit source glob", setup: func(f *fixture) {
		f.c.Scope.Source = append(f.c.Scope.Source, "vendor/v/v.go")
	}, change: func(f *fixture) { f.write("vendor/v/v.go", "package v // edited\n") }, want: same},
	{name: "file excluded by name and by a directory glob", setup: func(f *fixture) {
		f.c.Scope.Source = append(f.c.Scope.Source, "build")
		f.c.Scope.Exclude = append(f.c.Scope.Exclude, "build", "build/**")
		f.write("build", "#!/bin/sh\necho a\n")
	}, change: func(f *fixture) { f.write("build", "#!/bin/sh\necho b\n") }, want: same},
	{name: "symlink no glob can reach", setup: func(f *fixture) {
		f.c.Scope.Source = []string{"main.go", "pkg/**", "compose.yaml"}
	}, change: func(f *fixture) {
		out := f.t.TempDir()
		f.do(os.WriteFile(filepath.Join(out, "b.go"), []byte("package b\n"), 0o644))
		f.symlink(out, "sub")
	}, want: same},
	// A tool symlink (node_modules/.bin, .venv/bin) under a broad glob is
	// skipped when exclude covers its name and its contents (#198).
	{name: "symlink excluded by name and contents", setup: func(f *fixture) {
		nodeModulesTool(f)
	}, change: func(f *fixture) { f.write("pkg/web/node_modules/tool/bin/cli.js", "#!/bin/sh\necho b\n") }, want: same},
	{name: "symlinked directory excluded by name and contents", setup: func(f *fixture) {
		f.c.Scope.Source = []string{"main.go", "pkg/**", "compose.yaml"}
		f.c.Scope.Exclude = append(f.c.Scope.Exclude, "pkg/bin", "pkg/bin/**")
		out := f.t.TempDir()
		f.do(os.WriteFile(filepath.Join(out, "b.go"), []byte("package b\n"), 0o644))
		f.symlink(out, "pkg/bin")
	}, change: func(f *fixture) {
		out, err := os.Readlink(f.path("pkg/bin"))
		f.do(err)
		f.do(os.WriteFile(filepath.Join(out, "b.go"), []byte("package b // edited\n"), 0o644))
	}, want: same},
	{name: "file under .git added", change: func(f *fixture) { f.write(".git/objects/x.go", "package x\n") }, want: same},
	{name: "file under a nested .git added", change: func(f *fixture) { f.write("pkg/a/.git/x.go", "package x\n") }, want: same},
	// Only the exact name .git is skipped: a case-insensitive match would drop
	// in-scope files under .GIT on every filesystem.
	{name: "file under .GIT added", change: func(f *fixture) { f.write(".GIT/x.go", "package x\n") }, want: differ},
	{name: ".GIT regular file added", setup: func(f *fixture) { f.c.Scope.Source = []string{"**"} },
		change: func(f *fixture) { f.write(".GIT", "gitdir: x\n") }, want: differ},
	// A .git file or symlink is VCS metadata too (#199): the gitfile of a
	// linked worktree or submodule holds a machine-specific path git never
	// versions, so checkouts of one commit must share a fingerprint.
	{name: "gitfile edited", setup: func(f *fixture) {
		f.c.Scope.Source = []string{"**"}
		f.write(".git", "gitdir: /src/main/.git/worktrees/a\n")
	}, change: func(f *fixture) { f.write(".git", "gitdir: /src/main/.git/worktrees/b\n") }, want: same},
	{name: "nested gitfile added", setup: func(f *fixture) { f.c.Scope.Source = []string{"**"} },
		change: func(f *fixture) { f.write("pkg/sub/.git", "gitdir: ../../.git/modules/sub\n") }, want: same},
	{name: "gitfile replaced by .git directory", setup: func(f *fixture) {
		f.c.Scope.Source = []string{"**"}
		f.write(".git", "gitdir: /src/main/.git/worktrees/a\n")
	}, change: func(f *fixture) {
		f.remove(".git")
		f.write(".git/HEAD", "ref: refs/heads/main\n")
	}, want: same},
	// An exact glob naming a gitfile matches nothing, so it fails like any
	// glob that matches no file: never a silently dropped input.
	{name: "exact glob naming a gitfile", change: func(f *fixture) {
		f.c.Scope.Source = append(f.c.Scope.Source, "pkg/.git")
		f.write("pkg/.git", "gitdir: ../.git/modules/pkg\n")
	}, want: failClosed},
	{name: ".git symlink", setup: func(f *fixture) { f.c.Scope.Source = []string{"**"} },
		change: func(f *fixture) { f.symlink(f.t.TempDir(), "pkg/.git") }, want: same},
	// Aval's own outputs are never inputs (#63): .aval directories at any
	// depth, and the --result and --report paths inside the root.
	{name: "file under .aval added", change: func(f *fixture) { f.write(".aval/result.go", "package r\n") }, want: same},
	{name: "file under a nested .aval added", change: func(f *fixture) { f.write("pkg/a/.aval/r.go", "package r\n") }, want: same},
	{name: "file under .AVAL added", change: func(f *fixture) { f.write(".AVAL/r.go", "package r\n") }, want: differ},
	{name: "result path inside the root written", setup: func(f *fixture) {
		f.outputs = []string{"", f.path("out/sub/../result.go")}
	}, change: func(f *fixture) { f.write("out/result.go", "package r\n") }, want: same},
	{name: "result path outside the root, same relative name", setup: func(f *fixture) {
		f.outputs = []string{filepath.Join(f.t.TempDir(), "out", "result.go")}
	}, change: func(f *fixture) { f.write("out/result.go", "package r\n") }, want: differ},
	{name: "file next to the result path", setup: func(f *fixture) {
		f.outputs = []string{f.path("out/result.go")}
	}, change: func(f *fixture) { f.write("out/other.go", "package r\n") }, want: differ},
	{name: "checkout path", change: func(f *fixture) {
		to := filepath.Join(f.t.TempDir(), "elsewhere")
		f.do(os.Rename(f.dir, to))
		f.dir = to
	}, want: same},
	{name: "file recreation order", change: func(f *fixture) {
		names := slices.Sorted(maps.Keys(baseFiles))
		slices.Reverse(names)
		for _, n := range names {
			f.remove(n)
			f.write(n, baseFiles[n])
		}
	}, want: same},
	{name: "timestamps", change: func(f *fixture) {
		f.do(os.Chtimes(f.path("main.go"), time.Unix(0, 0), time.Unix(0, 0)))
	}, want: same},
	{name: "non-exec permission bits", change: func(f *fixture) { f.chmod("main.go", 0o600); f.chmod("go.sum", 0o444) }, want: same},
	{name: "exec bit variants", setup: func(f *fixture) { f.chmod("main.go", 0o755) }, change: func(f *fixture) { f.chmod("main.go", 0o711) }, want: same},
	{name: "glob order", change: func(f *fixture) { f.c.Scope.Source = []string{"compose.yaml", "**/*.go"} }, want: same},
	{name: "duplicate globs", change: func(f *fixture) { f.c.Scope.Source = append(f.c.Scope.Source, "**/*.go") }, want: same},
	{name: "env name order", change: func(f *fixture) { f.c.Environment.Env = []string{"MODE", "DB_IMAGE"} }, want: same},
	{name: "duplicate env names", change: func(f *fixture) { f.c.Environment.Env = append(f.c.Environment.Env, "MODE") }, want: same},
	{name: "empty lists and maps as omitted", setup: func(f *fixture) {
		f.c.Scope.Exclude, f.c.Environment.Toolchain, f.c.Environment.Probe = nil, nil, nil
	}, change: func(f *fixture) {
		f.c.Scope.Exclude, f.c.Environment.Toolchain, f.c.Environment.Probe = []string{}, map[string]string{}, map[string][]string{}
	}, want: same},
	{name: "aval.yaml comments, formatting and key order", change: func(f *fixture) { f.parse(corpusYAML) }, want: same},
	{name: "allowMutable order and duplicates", setup: func(f *fixture) {
		f.c.Scope.AllowMutable = []contract.AllowMutable{{Ref: "a", Reason: "x"}, {Ref: "b", Reason: "y"}}
	}, change: func(f *fixture) {
		f.c.Scope.AllowMutable = []contract.AllowMutable{{Ref: "b", Reason: "y"}, {Ref: "a", Reason: "x"}, {Ref: "b", Reason: "y"}}
	}, want: same},
	{name: "outputs order, duplicates and empty", setup: func(f *fixture) { f.c.Outputs = []string{"a.xml", "b.xml"} }, change: func(f *fixture) {
		f.c.Outputs = []string{"b.xml", "a.xml", "b.xml"}
	}, want: same},
	{name: "empty outputs as omitted", change: func(f *fixture) { f.c.Outputs = []string{} }, want: same},

	// Fail closed.
	{name: "dependency deleted", change: func(f *fixture) { f.remove("go.sum") }, want: failClosed},
	{name: "every source deleted", change: func(f *fixture) {
		for _, n := range []string{"main.go", "pkg/a/a.go", "compose.yaml"} {
			f.remove(n)
		}
	}, want: failClosed},
	{name: "every source excluded", change: func(f *fixture) { f.c.Scope.Exclude = append(f.c.Scope.Exclude, "**") }, want: failClosed},
	{name: "symlink to file inside root", change: func(f *fixture) { f.symlink(f.path("main.go"), "link.go") }, want: failClosed},
	{name: "symlink to file outside root", change: func(f *fixture) {
		out := filepath.Join(f.t.TempDir(), "x.go")
		f.do(os.WriteFile(out, []byte("package x\n"), 0o644))
		f.symlink(out, "link.go")
	}, want: failClosed},
	{name: "symlink to directory", change: func(f *fixture) { f.symlink(f.path("pkg"), "dir.go") }, want: failClosed},
	{name: "symlinked directory outside root", change: func(f *fixture) {
		// The name sub matches no glob, but **/*.go could match sub/b.go.
		out := f.t.TempDir()
		f.do(os.WriteFile(filepath.Join(out, "b.go"), []byte("package b\n"), 0o644))
		f.symlink(out, "pkg/sub")
	}, want: failClosed},
	{name: "symlinked directory excluded by name", change: func(f *fixture) {
		// exclude matches names only and does not prune the walk, so
		// excluding sub alone would drop sub/b.go unhashed: the contents
		// must be excluded too (sub/**).
		f.c.Scope.Source = []string{"**"}
		f.c.Scope.Exclude = append(f.c.Scope.Exclude, "sub")
		out := f.t.TempDir()
		f.do(os.WriteFile(filepath.Join(out, "b.go"), []byte("package b\n"), 0o644))
		f.symlink(out, "sub")
	}, want: failClosed},
	// The rows below use the default source **/*.go, which matches no
	// symlink name, so nothing but the walk can refuse them.
	// "sub/**" matches sub itself only by its zero-segment form, which does
	// not exclude a file (or symlink) named sub.
	{name: "symlinked directory excluded only by its contents", change: func(f *fixture) {
		f.c.Scope.Exclude = append(f.c.Scope.Exclude, "sub/**")
		symlinkDir(f, "sub")
	}, want: failClosed},
	// exclude does not apply to dependencies, so a dependency glob that
	// could match files under the symlink still needs them.
	{name: "excluded symlink a dependency glob reaches", setup: func(f *fixture) {
		f.write("pkg/web/index.js", "x\n")
		f.c.Scope.Dependencies = append(f.c.Scope.Dependencies, "pkg/web/**/*.js")
		f.c.Scope.Exclude = append(f.c.Scope.Exclude, "**/node_modules/**")
	}, change: func(f *fixture) {
		f.do(os.MkdirAll(f.path("pkg/web/node_modules"), 0o755))
		symlinkDir(f, "pkg/web/node_modules/lib")
	}, want: failClosed},
	{name: "symlink excluded by name, contents in scope", change: func(f *fixture) {
		f.c.Scope.Exclude = append(f.c.Scope.Exclude, "**/tool")
		symlinkDir(f, "pkg/tool")
	}, want: failClosed},
	{name: "symlink whose parent no exclude glob matches", change: func(f *fixture) {
		f.c.Scope.Exclude = append(f.c.Scope.Exclude, "**/node_modules/*.go")
		f.do(os.MkdirAll(f.path("pkg/node_modules"), 0o755))
		symlinkDir(f, "pkg/node_modules/lib")
	}, want: failClosed},
	{name: "source replaced by symlink", change: func(f *fixture) {
		f.rename("main.go", "real.txt")
		f.symlink("real.txt", "main.go")
	}, want: failClosed},
	{name: "socket in scope", change: func(f *fixture) {
		// FIFOs and devices take the same !IsRegular path; a socket is the
		// one special file every platform can create without privileges.
		l, err := net.Listen("unix", f.path("s.go"))
		f.do(err)
		f.t.Cleanup(func() { _ = l.Close() })
	}, want: failClosed},
	{name: "newline in path", change: func(f *fixture) {
		f.write("x\nsource "+sum([]byte("c"))+" - y.go", "c")
	}, want: failClosed},
	{name: "source glob matches no file", change: func(f *fixture) { f.c.Scope.Source = append(f.c.Scope.Source, "mian.go") }, want: failClosed},
	{name: "literal source glob differs from the file name by case", setup: func(f *fixture) {
		f.write("makefile", "all:\n")
	}, change: func(f *fixture) {
		// The walk returns "makefile" on case-sensitive and case-insensitive
		// filesystems alike, so "Makefile" matches nothing.
		f.c.Scope.Source = append(f.c.Scope.Source, "Makefile")
	}, want: failClosed},
	{name: "file named like an excluded directory", setup: func(f *fixture) {
		f.c.Scope.Source = append(f.c.Scope.Source, "build")
		f.write("build", "#!/bin/sh\necho a\n")
	}, change: func(f *fixture) {
		// "build/**" declares the files under build/, not a file named build.
		f.c.Scope.Exclude = append(f.c.Scope.Exclude, "build/**")
	}, want: failClosed},
	{name: "carriage return in path", change: func(f *fixture) { f.write("x\r.go", "c") }, want: failClosed},

	// Known gaps: relevant to the test, invisible to the fingerprint by
	// design (invariant 2). See docs/contract.md, "Known gaps".
	{name: "gap: undeclared file the test reads", change: func(f *fixture) { f.write("testdata/fixture.json", "{}") }, want: gap},
	{name: "gap: undeclared env var the test reads", change: func(f *fixture) { f.env["GOFLAGS"] = "-tags=slow" }, want: gap},

	// Content behind a declared name: postgres:16 is repushed in the
	// registry, no byte in the scope changes. Closed by refusing reuse.
	{name: "repushed image tag", setup: func(f *fixture) {
		f.write("compose.yaml", "services:\n  db:\n    image: postgres:16\n")
	}, change: func(*fixture) {}, want: refused},
	{name: "repushed tag in runner.command", setup: func(f *fixture) {
		f.c.Runner.Command = []string{"docker", "run", "--rm", "postgres:16"}
	}, change: func(*fixture) {}, want: refused},
}

// platformLeg is the test named name of platformSet over the corpus tree.
func platformLeg(t *testing.T, name string) *contract.Contract {
	t.Helper()
	cs, err := contract.LoadAll(platformSet)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Metadata.Name == name {
			return c
		}
	}
	t.Fatalf("no test %s", name)
	return nil
}

// symlinkTool adds pkg/web/node_modules/.bin/tool -> ../tool/bin/cli.js,
// as npm ci does.
func symlinkTool(f *fixture) {
	f.write("pkg/web/node_modules/tool/bin/cli.js", "#!/bin/sh\necho a\n")
	f.do(os.MkdirAll(f.path("pkg/web/node_modules/.bin"), 0o755))
	f.symlink("../tool/bin/cli.js", "pkg/web/node_modules/.bin/tool")
}

// symlinkDir adds name -> a directory outside the root holding b.go.
func symlinkDir(f *fixture, name string) {
	out := f.t.TempDir()
	f.do(os.WriteFile(filepath.Join(out, "b.go"), []byte("package b\n"), 0o644))
	f.symlink(out, name)
}

// nodeModulesTool is symlinkTool under source pkg/** with node_modules
// excluded by name and contents.
func nodeModulesTool(f *fixture) {
	f.c.Scope.Source = []string{"main.go", "pkg/**", "compose.yaml"}
	f.c.Scope.Exclude = append(f.c.Scope.Exclude, "**/node_modules/**")
	symlinkTool(f)
}

func TestFalseHitCorpus(t *testing.T) {
	sections := map[string]bool{} // manifest sections some differ row changed
	ran := 0
	for _, tc := range corpus {
		t.Run(tc.name, func(t *testing.T) {
			ran++
			f := newFixture(t)
			if tc.setup != nil {
				tc.setup(f)
			}
			before, err := f.compute()
			if err != nil {
				t.Fatalf("base: %v", err)
			}
			tc.change(f)
			after, err := f.compute()
			checkRow(t, tc.want, before, after, err)
			if tc.want == differ {
				for _, s := range changedSections(before.Manifest, after.Manifest) {
					sections[s] = true
				}
			}
		})
	}
	if ran == len(corpus) { // -run or -skip may select some rows only
		checkSectionsCovered(t, sections)
	}
}

// checkRow fails the row unless the change had the outcome want.
func checkRow(t *testing.T, want outcome, before, after Fingerprint, err error) {
	t.Helper()
	switch {
	case want == failClosed:
		if err == nil {
			t.Fatalf("want an error (fail closed), got %s", after.Digest)
		}
	case err != nil:
		t.Fatal(err)
	case want == refused && (after.Digest != before.Digest || len(after.Unpinned()) == 0):
		t.Fatalf("false hit: a repushed reference stays reusable (digest %s, findings %v)", after.Digest, after.Findings)
	case want == differ && after.Digest == before.Digest:
		t.Fatalf("false hit: relevant change kept %s\n%s", after.Digest, after.Manifest)
	case want != differ && after.Digest != before.Digest:
		t.Fatalf("irrelevant change altered the fingerprint\n--- before\n%s--- after\n%s", before.Manifest, after.Manifest)
	}
}

// checkSectionsCovered fails for each section of the base manifest that no
// differ row changed.
func checkSectionsCovered(t *testing.T, covered map[string]bool) {
	t.Helper()
	base, err := newFixture(t).compute()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sectionsOf(base.Manifest) {
		if !covered[s] {
			t.Errorf("manifest section %q is changed by no differ row: add corpus cases for it", s)
		}
	}
}

func sectionsOf(manifest string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSuffix(manifest, "\n"), "\n")[1:] { // [0] is the version header
		out = append(out, strings.SplitN(l, " ", 2)[0])
	}
	return slices.Compact(out)
}

// changedSections lists the sections of lines present in only one manifest.
func changedSections(a, b string) []string {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	var out []string
	for _, l := range append(la, lb...) {
		if slices.Contains(la, l) != slices.Contains(lb, l) {
			out = append(out, strings.SplitN(l, " ", 2)[0])
		}
	}
	return out
}
