// Package fingerprint computes the execution fingerprint: a deterministic
// SHA-256 over everything a contract declares as input.
//
// The fingerprint is built from a canonical, line-oriented manifest:
//
//	aval-fingerprint/v2
//	contract <sha256 of canonical contract JSON>
//	source <sha256> <mode> <path>
//	dependency <sha256> <mode> <path>
//	toolchain <key>=<value>
//	env <NAME> <sha256 of value | unset>
//	resolve <kind> <reference> <version> <immutable id>
//	contract <path> <name> <fingerprint>
//
// resolve lines appear only for contracts with scope.resolve, the one part
// of the manifest that reads the network (internal/resolve); contract
// <path> lines only for contracts with scope.contracts, one per referenced
// contract with its own fingerprint (so references are transitive).
//
// Lines are sorted within each section, paths are slash-separated and
// relative to the contract root, so the result does not depend on the
// checkout location, directory iteration order or glob order. The exec bit
// comes from the filesystem, so Windows and Unix checkouts can differ.
//
// Anything not in the manifest cannot affect the fingerprint. That is the
// point and the risk: undeclared inputs are invisible (see docs/contract.md).
package fingerprint

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/pinning"
	"github.com/TrianaLab/aval/internal/resolve"
)

// Version is the manifest format version. Bump it whenever the manifest
// layout changes, so old and new fingerprints never collide.
const Version = "aval-fingerprint/v2"

// ErrContract marks a contract that is valid on its own but does not fit
// the files in its scope, such as a scope.allowMutable files glob that
// matches none. Like an invalid contract, it is the author's to fix.
var ErrContract = errors.New("invalid contract")

// Fingerprint is the result of Compute.
type Fingerprint struct {
	// Digest is "sha256:<hex>" over Manifest.
	Digest string
	// Manifest is the exact text that was hashed, for debugging misses.
	Manifest string
	// Findings are the mutable references in the hashed files and in
	// runner.command (see internal/pinning), with the reason from
	// scope.allowMutable on the acknowledged ones. They are not part of the
	// manifest: they say what the digest cannot vouch for.
	Findings []pinning.Finding
	// Resolved are the scope.resolve references with the ids hashed in
	// the manifest and the time each took.
	Resolved []resolve.Result
	// Contracts are the scope.contracts references, sorted by path, each
	// with its own fingerprint (and so its own references): the tree
	// --explain shows.
	Contracts []Dependency
}

// Dependency is one scope.contracts reference and its fingerprint. Its
// findings are also in the referencing Fingerprint's Findings.
type Dependency struct {
	// Path is the reference as written, relative to the referencing
	// contract's directory.
	Path string
	// Name is the referenced contract's metadata.name.
	Name string
	Fingerprint
	// root is the referenced contract's scope.root, which its findings'
	// paths are relative to.
	root string
}

const outside = "leads outside the repository root (the git working tree, or the project in the browser demo)"

// below returns dir relative to top, an ancestor or dir itself (both
// relative to the root).
func below(dir, top string) string {
	switch top {
	case dir:
		return "."
	case ".":
		return dir
	}
	return strings.TrimPrefix(dir, top+"/")
}

// Bounds on scope.contracts: how deep references may nest, and how many
// distinct contracts one fingerprint may read.
const (
	maxContractDepth = 8
	maxContracts     = 64
)

// resolveAll is a seam for the golden test, which must not need a network.
var resolveAll = resolve.All

// Unpinned returns the findings scope.allowMutable does not acknowledge.
func (f Fingerprint) Unpinned() []pinning.Finding {
	var out []pinning.Finding
	for _, x := range f.Findings {
		if x.Allowed == "" {
			out = append(out, x)
		}
	}
	return out
}

// Hex returns the hex part of the digest.
func (f Fingerprint) Hex() string { return strings.TrimPrefix(f.Digest, "sha256:") }

// LookupEnv resolves environment variables; os.LookupEnv in production.
type LookupEnv func(string) (string, bool)

// OutputDir is the directory name Aval reserves for its own outputs. A
// directory with this name is skipped at any depth, like ".git".
const OutputDir = ".aval"

// Compute fingerprints the contract c in dir, rooted at Root(dir, c).
// outputs are files Aval itself writes (the --result-file and --report-file
// paths); the ones inside the root are not inputs and are skipped, in c's
// own scope only. Empty entries are ignored.
//
// A contract with scope.contracts or scope.root is resolved inside the git
// working tree that holds dir (see repoRoot): no reference or root may lead
// outside it.
func Compute(ctx context.Context, dir string, c *contract.Contract, env LookupEnv, outputs ...string) (Fingerprint, error) {
	return compute(ctx, dir, c, env, nil, outputs)
}

// compute is Compute and ComputeRecorded: recorded, when not nil, is the
// env section of c.
func compute(ctx context.Context, dir string, c *contract.Contract, env LookupEnv, recorded []string, outputs []string) (Fingerprint, error) {
	root, rel := dir, "."
	if len(c.Scope.Contracts) > 0 || c.Scope.Root != "" {
		var err error
		if root, rel, err = repoRoot(dir); err != nil {
			return Fingerprint{}, err
		}
	}
	// Outputs are taken relative to dir as given, not to the root repoRoot
	// resolved through symlinks, then placed under rel. With scope.root
	// both sides are real paths, as in Root.
	base, scope := dir, rel
	if c.Scope.Root != "" {
		scope = path.Join(rel, c.Scope.Root) // compute rejects one outside root
		base = filepath.Join(root, filepath.FromSlash(scope))
		outputs = RealPaths(outputs)
	}
	skip := map[string]bool{}
	for p := range outputsUnder(base, outputs) {
		skip[path.Join(scope, p)] = true
	}
	t := newTree(ctx, os.DirFS(root), env, skip)
	t.recorded = recorded
	return t.compute(rel, c)
}

// Root returns the directory the scope of c, a contract in dir, is rooted
// at: dir itself without scope.root; otherwise the real location of dir
// (symlinks resolved) joined with scope.root, which must lie inside the git
// working tree that holds dir.
func Root(dir string, c *contract.Contract) (string, error) {
	if c.Scope.Root == "" {
		return dir, nil
	}
	top, rel, err := repoRoot(dir)
	if err != nil {
		return "", err
	}
	r := path.Join(rel, c.Scope.Root)
	if !fs.ValidPath(r) {
		return "", fmt.Errorf("scope.root %s leads outside the git working tree %s", c.Scope.Root, top)
	}
	return filepath.Join(top, filepath.FromSlash(r)), nil
}

// RealPaths returns each path absolute, with the symlinks in its directory
// resolved, so it compares with Root. A path whose directory cannot be
// resolved comes back absolute only; "" stays "".
func RealPaths(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		abs, err := filepath.Abs(p)
		if p == "" || err != nil {
			out[i] = p
			continue
		}
		out[i] = abs
		if d, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
			out[i] = filepath.Join(d, filepath.Base(abs))
		}
	}
	return out
}

// repoRoot returns the nearest directory at or above dir with a .git entry
// (a directory, or the file of a worktree or submodule), and dir relative
// to it, slash-separated. Without one, scope.contracts and scope.root have
// no boundary, so it fails closed.
func repoRoot(dir string) (string, string, error) {
	abs, err := filepath.Abs(dir)
	if err == nil {
		abs, err = filepath.EvalSymlinks(abs) // resolve ../ as the OS does from the test's working directory
	}
	for root := abs; err == nil; {
		if _, serr := os.Lstat(filepath.Join(root, ".git")); serr == nil {
			rel, _ := filepath.Rel(root, abs) // both absolute, root a prefix of abs
			return root, filepath.ToSlash(rel), nil
		}
		parent := filepath.Dir(root)
		if parent == root {
			break
		}
		root = parent
	}
	return "", "", fmt.Errorf("no git working tree (.git) at or above %s: referenced contracts (scope.contracts) and the root (scope.root) must lie inside one", dir)
}

// ComputeFS fingerprints the contract c rooted at fsys, such as the
// in-memory project of the browser demo (cmd/aval-wasm). Its manifest is the
// one Compute writes for the same tree. skip lists slash-separated paths,
// relative to the root, that are Aval outputs and not inputs.
// scope.contracts references must lie inside fsys.
func ComputeFS(fsys fs.FS, c *contract.Contract, env LookupEnv, skip ...string) (Fingerprint, error) {
	m := map[string]bool{}
	for _, s := range skip {
		m[s] = true
	}
	// ponytail: no ctx; the browser demo's contracts declare no scope.resolve.
	return newTree(context.Background(), fsys, env, m).compute(".", c)
}

// tree fingerprints a contract and the contracts it references, all
// inside one root.
type tree struct {
	ctx  context.Context
	root fs.FS
	env  LookupEnv
	// skip holds Aval outputs, relative to root. It applies to the top
	// contract only: the #63 guard checks only that contract's scope, so
	// in a referenced contract an output stays an input (fail closed).
	skip map[string]bool
	// done holds each referenced contract already fingerprinted, by its
	// path relative to root, so a shared one is computed once; stack is
	// the chain being computed, to name a cycle.
	done  map[string]Dependency
	stack []string
	// recorded, when not nil, is the env section of the top contract, from
	// an env file (ComputeRecorded).
	recorded []string
}

func newTree(ctx context.Context, root fs.FS, env LookupEnv, skip map[string]bool) *tree {
	if env == nil {
		env = os.LookupEnv
	}
	return &tree{ctx: ctx, root: root, env: env, skip: skip, done: map[string]Dependency{}}
}

// sub returns the directory dir of the root and, for the top contract,
// the outputs under it, relative to dir.
func (t *tree) sub(dir string) (fs.FS, map[string]bool) {
	fsys := t.root
	if dir != "." {
		fsys, _ = fs.Sub(t.root, dir) // dir is a valid path: Sub cannot fail
	}
	if len(t.stack) > 0 { // a referenced contract: outputs stay inputs (fail closed)
		return fsys, nil
	}
	if dir == "." {
		return fsys, t.skip
	}
	skip := map[string]bool{}
	for p := range t.skip {
		if rel, ok := strings.CutPrefix(p, dir+"/"); ok {
			skip[rel] = true
		}
	}
	return fsys, skip
}

// compute fingerprints c, whose directory is dir (relative to the root).
// Its scope is rooted at dir joined with scope.root.
func (t *tree) compute(dir string, c *contract.Contract) (Fingerprint, error) {
	top := path.Join(dir, c.Scope.Root)
	if !fs.ValidPath(top) {
		return Fingerprint{}, fmt.Errorf("scope.root %s %s", c.Scope.Root, outside)
	}
	fsys, skip := t.sub(top)
	ctx := t.ctx
	canon := canonicalContract(c)
	source, exclude, deps := splitGlobs(c.Scope.Source), splitGlobs(c.Scope.Exclude), splitGlobs(c.Scope.Dependencies)
	files, err := listFiles(fsys, skip, source, deps, exclude)
	if err != nil {
		return Fingerprint{}, err
	}

	var lines []string
	lines = append(lines, Version, "contract "+sum(canon))

	h := &hasher{buf: make([]byte, 32<<10)}
	src, err := h.hashMatching(fsys, files, "source", source, exclude)
	if err != nil {
		return Fingerprint{}, err
	}
	if len(src) == 0 {
		return Fingerprint{}, errors.New("scope.source matched no files")
	}
	lines = append(lines, src...)

	dep, err := h.hashMatching(fsys, files, "dependency", deps, nil)
	if err != nil {
		return Fingerprint{}, err
	}
	lines = append(lines, dep...)

	var section []string
	for k, v := range c.Environment.Toolchain {
		section = append(section, fmt.Sprintf("toolchain %s=%s", k, v))
	}
	slices.Sort(section)
	lines = append(lines, section...)

	section, err = t.envSection(c)
	if err != nil {
		return Fingerprint{}, err
	}
	slices.Sort(section)
	lines = append(lines, slices.Compact(section)...)

	// Last, so a contract without scope.resolve never touches the network
	// and one with a bad scope fails before it does.
	resolved, err := resolveAll(ctx, canonicalResolve(c.Scope.Resolve))
	if err != nil {
		return Fingerprint{}, err
	}
	for _, r := range resolved {
		lines = append(lines, fmt.Sprintf("resolve %s %s %s %s", r.Kind, r.Ref, r.Version, r.ID))
	}

	refs, err := t.contracts(dir, c)
	if err != nil {
		return Fingerprint{}, err
	}
	found, err := findings(fsys, c, slices.Concat(src, dep))
	if err != nil {
		return Fingerprint{}, err
	}
	for _, d := range refs {
		lines = append(lines, fmt.Sprintf("contract %s %s %s", d.Path, d.Name, d.Digest))
		// A referenced contract's findings block this one too; its own
		// scope.allowMutable already marked them, this one's never applies.
		for _, f := range d.Findings {
			// From the referenced contract's root to this one's.
			f.File = path.Join(below(dir, top), path.Dir(d.Path), d.root, f.File)
			found = append(found, f)
		}
	}

	manifest := strings.Join(lines, "\n") + "\n"
	return Fingerprint{Digest: "sha256:" + sum([]byte(manifest)), Manifest: manifest, Findings: found, Resolved: resolved, Contracts: refs}, nil
}

// envSection returns the env lines of c, unsorted: the recorded ones for
// the top contract when there are (ComputeRecorded), else hashed from t.env.
func (t *tree) envSection(c *contract.Contract) ([]string, error) {
	if t.recorded != nil && len(t.stack) == 0 {
		return recordedEnv(t.recorded, c.Environment.Env)
	}
	var section []string
	for _, name := range c.Environment.Env {
		v, ok := t.env(name)
		h := "unset"
		if ok {
			h = sum([]byte(v))
		}
		section = append(section, fmt.Sprintf("env %s %s", name, h))
	}
	return section, nil
}

// contracts fingerprints the scope.contracts of c, whose directory is dir,
// sorted by path. Any reference that cannot be proven fails the whole
// fingerprint (fail closed).
func (t *tree) contracts(dir string, c *contract.Contract) ([]Dependency, error) {
	var out []Dependency
	for _, ref := range slices.Sorted(slices.Values(c.Scope.Contracts)) {
		d, err := t.load(path.Join(dir, ref))
		if err != nil {
			return nil, fmt.Errorf("scope.contracts %s: %w", ref, err)
		}
		d.Path = ref
		out = append(out, d)
	}
	return out, nil
}

// load parses and fingerprints the contract at file, relative to the root.
func (t *tree) load(file string) (Dependency, error) {
	if !fs.ValidPath(file) {
		return Dependency{}, errors.New(outside)
	}
	if i := slices.Index(t.stack, file); i >= 0 {
		return Dependency{}, fmt.Errorf("cycle: %s", strings.Join(append(slices.Clone(t.stack[i:]), file), " -> "))
	}
	if d, ok := t.done[file]; ok {
		return d, nil
	}
	if len(t.stack) >= maxContractDepth || len(t.done) >= maxContracts {
		return Dependency{}, fmt.Errorf("more than %d levels or %d contracts", maxContractDepth, maxContracts)
	}
	// Like a symlink in scope, a symlink on the way to a contract may lead
	// anywhere (#118).
	for p := file; p != "."; p = path.Dir(p) {
		fi, err := fs.Lstat(t.root, p)
		if err != nil {
			return Dependency{}, err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return Dependency{}, fmt.Errorf("%s is a symlink: symlinks are not followed", p)
		}
		if p == file && !fi.Mode().IsRegular() {
			return Dependency{}, fmt.Errorf("%s is not a regular file", p)
		}
	}
	data, err := fs.ReadFile(t.root, file)
	if err != nil {
		return Dependency{}, err
	}
	c, err := contract.Parse(data)
	if errors.Is(err, contract.ErrTestSet) {
		// Fragments share scope inside a TestSet; a reference binds one test.
		return Dependency{}, fmt.Errorf("%w: %s is a kind: TestSet; scope.contracts takes a kind: Test (share scope with fragments instead)", ErrContract, file)
	}
	if err != nil {
		return Dependency{}, err
	}
	t.stack = append(t.stack, file)
	fp, err := t.compute(path.Dir(file), c)
	t.stack = t.stack[:len(t.stack)-1]
	if err != nil {
		return Dependency{}, err
	}
	d := Dependency{Name: c.Metadata.Name, Fingerprint: fp, root: c.Scope.Root}
	t.done[file] = d
	return d, nil
}

// canonicalResolve sorts and deduplicates scope.resolve, so its order does
// not matter; resolve lines come out sorted too.
func canonicalResolve(refs []contract.Reference) []contract.Reference {
	refs = slices.Clone(refs)
	slices.SortFunc(refs, func(a, b contract.Reference) int {
		return cmp.Or(strings.Compare(a.Kind, b.Kind), strings.Compare(a.Ref, b.Ref))
	})
	return slices.Compact(refs)
}

// findings scans the hashed files (from their manifest lines) and
// runner.command, and marks the findings scope.allowMutable acknowledges.
// scope.resolve never clears a finding: it binds an id into the manifest,
// but cannot prove that id is what runs (invariant 3).
func findings(fsys fs.FS, c *contract.Contract, lines []string) ([]pinning.Finding, error) {
	paths := make([]string, len(lines))
	for i, l := range lines {
		paths[i] = strings.SplitN(l, " ", 4)[3] // kind sum mode path
	}
	if err := unmatchedFiles(c.Scope.AllowMutable, paths); err != nil {
		return nil, err
	}
	out := pinning.Scan(fsys, paths, c.Runner.Command)
	files := map[string]map[string]bool{} // ref -> files reporting it
	for _, f := range out {
		if files[f.Ref] == nil {
			files[f.Ref] = map[string]bool{}
		}
		files[f.Ref][f.File] = true
	}
	for i := range out {
		if out[i].Ecosystem == "scan" { // a file Aval cannot read is never accepted
			continue
		}
		ambiguous := false
		for _, a := range c.Scope.AllowMutable {
			ok, amb := accepts(a, out[i], len(files[a.Ref]))
			if ok {
				out[i].Allowed = a.Reason
			}
			ambiguous = ambiguous || amb
		}
		if ambiguous && out[i].Allowed == "" {
			out[i].Fix += fmt.Sprintf("; scope.allowMutable lists this ref without file:, and %d files report it: add file: %s to the entry", len(files[out[i].Ref]), out[i].File)
		}
	}
	return out, nil
}

// accepts reports whether the entry a accepts the finding f, whose ref
// reporting files report, or whether it names f's ref ambiguously.
func accepts(a contract.AllowMutable, f pinning.Finding, reporting int) (ok, ambiguous bool) {
	switch {
	case a.Ref != f.Ref:
		return false, false
	case len(a.Files) > 0: // in-scope files only: runner.command is not one
		return f.File != pinning.CommandFile && slices.ContainsFunc(a.Files, func(g string) bool { return contract.Match(g, f.File) }), false
	case a.File != "":
		return a.File == f.File, false
	}
	// Without file or files, which file it means is unprovable when several
	// report the ref: fail closed.
	return reporting == 1, reporting > 1
}

// unmatchedFiles rejects a scope.allowMutable files glob that matches none
// of paths, the files in scope: like an unmatched scope glob, it is a typo
// or a stale entry.
func unmatchedFiles(allow []contract.AllowMutable, paths []string) error {
	for i, a := range allow {
		for j, g := range a.Files {
			if !slices.ContainsFunc(paths, func(p string) bool { return contract.Match(g, p) }) {
				return fmt.Errorf("%w: scope.allowMutable[%d].files[%d]: glob %q matches no file in scope", ErrContract, i, j, g)
			}
		}
	}
	return nil
}

// canonicalContract serializes the contract so that formatting, comments and
// the order of set-like lists (globs, env names) do not matter, while argv
// order (which is semantic) does.
func canonicalContract(c *contract.Contract) []byte {
	cc := *c
	// Empty and omitted lists/maps must hash the same, so both become nil.
	sorted := func(s []string) []string {
		if len(s) == 0 {
			return nil
		}
		s = slices.Clone(s)
		slices.Sort(s)
		return slices.Compact(s)
	}
	if len(cc.Environment.Toolchain) == 0 {
		cc.Environment.Toolchain = nil
	}
	cc.Scope.Source = sorted(c.Scope.Source)
	cc.Scope.Exclude = sorted(c.Scope.Exclude)
	cc.Scope.Dependencies = sorted(c.Scope.Dependencies)
	cc.Environment.Env = sorted(c.Environment.Env)
	cc.Scope.AllowMutable = slices.Clone(c.Scope.AllowMutable)
	for i := range cc.Scope.AllowMutable {
		cc.Scope.AllowMutable[i].Files = sorted(cc.Scope.AllowMutable[i].Files)
	}
	slices.SortFunc(cc.Scope.AllowMutable, func(a, b contract.AllowMutable) int {
		return cmp.Or(strings.Compare(a.File, b.File), slices.Compare(a.Files, b.Files), strings.Compare(a.Ref, b.Ref), strings.Compare(a.Reason, b.Reason))
	})
	cc.Scope.AllowMutable = slices.CompactFunc(cc.Scope.AllowMutable, func(a, b contract.AllowMutable) bool {
		return a.File == b.File && slices.Equal(a.Files, b.Files) && a.Ref == b.Ref && a.Reason == b.Reason
	})
	cc.Scope.Resolve = canonicalResolve(c.Scope.Resolve)
	cc.Scope.Contracts = sorted(c.Scope.Contracts)
	cc.Outputs = sorted(c.Outputs)
	// Marshal cannot fail: the contract holds only strings, string slices
	// and string maps. Map keys are sorted by encoding/json.
	b, _ := json.Marshal(cc)
	return b
}

// outputsUnder returns the outputs that lie inside dir as slash-separated
// paths relative to dir. An output that cannot be resolved is not skipped:
// hashing one file too many only causes a miss (fail closed).
func outputsUnder(dir string, outputs []string) map[string]bool {
	skip := map[string]bool{}
	root, err := filepath.Abs(dir)
	for _, o := range outputs {
		if o == "" || err != nil {
			continue
		}
		abs, aerr := filepath.Abs(o)
		rel, rerr := filepath.Rel(root, abs)
		if aerr == nil && rerr == nil && filepath.IsLocal(rel) {
			skip[filepath.ToSlash(rel)] = true
		}
	}
	return skip
}

func splitGlobs(globs []string) []contract.Glob {
	out := make([]contract.Glob, len(globs))
	for i, g := range globs {
		out[i] = contract.SplitGlob(g)
	}
	return out
}

// file is a slash-separated path relative to the contract root and its
// segments, split once for every glob match.
type file struct {
	rel  string
	segs []string
}

// listFiles returns the regular files and symlinks in fsys, sorted by
// path. VCS metadata, Aval's output directories and the paths in skip are
// left out, and so is every directory no include glob can match a file in:
// an unreadable or deep directory outside the scope is never opened.
//
// The walk does not follow symlinks. A symlink under which a source or
// dependency glob could match files may point to a directory whose files
// cannot be hashed, so it is an error here (fail closed), unless
// excludedSymlink proves nothing under it is an input. A symlink only an
// exact glob reaches is rejected when hashed (unless excluded).
func listFiles(fsys fs.FS, skip map[string]bool, source, deps, exclude []contract.Glob) ([]file, error) {
	include := slices.Concat(source, deps)
	var out []file
	err := fs.WalkDir(fsys, ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		f := file{rel, strings.Split(rel, "/")}
		mayContain := slices.ContainsFunc(include, func(g contract.Glob) bool { return g.MayContain(f.segs) })
		// .git is VCS metadata whatever its type: a directory, or the gitfile
		// of a linked worktree or submodule (a machine-specific pointer git
		// never versions), or a symlink. The name is exact (.GIT is an input).
		if d.Name() == ".git" {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if d.Name() == OutputDir {
				return fs.SkipDir
			}
			// WalkDir calls this before reading the directory, so a pruned
			// directory is never opened.
			if rel != "." && !mayContain {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 && mayContain && !excludedSymlink(f.segs, deps, exclude) {
			return fmt.Errorf("%q is a symlink a glob could match files under: only regular files are supported in scope (symlinks are not followed)", rel)
		}
		if skip[rel] {
			return nil
		}
		out = append(out, f)
		return nil
	})
	slices.SortFunc(out, func(a, b file) int { return strings.Compare(a.rel, b.rel) })
	return out, err
}

// excludedSymlink reports whether a symlink at name can be walked past
// without following it: exclude drops its name (by the rule hashMatching
// applies) and everything under it (a glob ending in ** that matches name
// matches every path below it), and no dependency glob, which exclude does
// not apply to, can reach it. The symlink stays in the file list, where
// hashMatching drops it as excluded.
func excludedSymlink(name []string, deps, exclude []contract.Glob) bool {
	nameExcluded := slices.ContainsFunc(exclude, func(g contract.Glob) bool { return g.MatchContents(name) })
	contentsExcluded := slices.ContainsFunc(exclude, func(g contract.Glob) bool { return g[len(g)-1] == "**" && g.Match(name) })
	depReaches := slices.ContainsFunc(deps, func(g contract.Glob) bool { return g.Match(name) || g.MayContain(name) })
	return nameExcluded && contentsExcluded && !depReaches
}

// hasher hashes files through one reused copy buffer.
type hasher struct{ buf []byte }

// hashMatching hashes every file matching any include glob and no exclude
// glob. Every include glob must match at least one file (before exclude): a
// glob that matches nothing is a typo or a name that differs on disk (case,
// Unicode form), and ignoring it would silently drop a declared input.
func (h *hasher) hashMatching(fsys fs.FS, files []file, kind string, include, exclude []contract.Glob) ([]string, error) {
	var lines []string
	for _, g := range include {
		if !slices.ContainsFunc(files, func(f file) bool { return g.Match(f.segs) }) {
			return nil, fmt.Errorf("%s glob %q matched no files", kind, strings.Join(g, "/"))
		}
	}
	for _, f := range files {
		if !matchesAny(include, f) {
			continue
		}
		if matchesAny(exclude, f) {
			// `dir/**` also matches a file named dir (** matches zero
			// segments). Excluding that file would drop an input the glob
			// does not declare, so it must be excluded by another glob.
			if !slices.ContainsFunc(exclude, func(g contract.Glob) bool { return g.MatchContents(f.segs) }) {
				return nil, fmt.Errorf("%s %q is excluded only by a trailing \"**\", which means the files under it: exclude it by name or narrow the glob", kind, f.rel)
			}
			continue
		}
		// Paths go into a line-oriented manifest unescaped; a newline in a
		// name could forge extra lines and collide with another tree.
		if strings.ContainsAny(f.rel, "\n\r") {
			return nil, fmt.Errorf("%s %q: control characters in path are not supported", kind, f.rel)
		}
		sum, mode, err := h.hashFile(fsys, f.rel)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", kind, f.rel, err)
		}
		lines = append(lines, kind+" "+sum+" "+mode+" "+f.rel)
	}
	return lines, nil // files is sorted, so lines are too
}

func matchesAny(globs []contract.Glob, f file) bool {
	return slices.ContainsFunc(globs, func(g contract.Glob) bool { return g.Match(f.segs) })
}

// hashFile returns the content hash and a portable mode ("x" executable,
// "-" otherwise). Symlinks are rejected: their target may live outside the
// scope, and we cannot prove it did not change (fail closed).
func (h *hasher) hashFile(fsys fs.FS, p string) (string, string, error) {
	fi, err := fs.Lstat(fsys, p)
	if err != nil {
		return "", "", err
	}
	if !fi.Mode().IsRegular() {
		return "", "", errors.New("only regular files are supported in scope (symlinks and devices are rejected)")
	}
	d := sha256.New()
	f, err := fsys.Open(p)
	if err == nil { // one error branch for open and read
		defer func() { _ = f.Close() }() // read-only: a close error loses nothing
		// Hide *os.File's WriteTo, which copies through a fresh buffer.
		_, err = io.CopyBuffer(d, struct{ io.Reader }{f}, h.buf)
	}
	if err != nil {
		return "", "", err
	}
	mode := "-"
	if fi.Mode()&0o111 != 0 {
		mode = "x"
	}
	return hex.EncodeToString(d.Sum(nil)), mode, nil
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
