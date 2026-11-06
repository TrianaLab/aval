// Package fingerprint computes the execution fingerprint: a deterministic
// SHA-256 over everything a contract declares as input.
//
// The fingerprint is built from a canonical, line-oriented manifest:
//
//	aval-fingerprint/v1
//	contract <sha256 of canonical contract JSON>
//	source <sha256> <mode> <path>
//	dependency <sha256> <mode> <path>
//	toolchain <key>=<value>
//	env <NAME> <sha256 of value | unset>
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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/TrianaLab/aval/internal/contract"
)

// Version is the manifest format version. Bump it whenever the manifest
// layout changes, so old and new fingerprints never collide.
const Version = "aval-fingerprint/v1"

// Fingerprint is the result of Compute.
type Fingerprint struct {
	// Digest is "sha256:<hex>" over Manifest.
	Digest string
	// Manifest is the exact text that was hashed, for debugging misses.
	Manifest string
}

// Hex returns the hex part of the digest.
func (f Fingerprint) Hex() string { return strings.TrimPrefix(f.Digest, "sha256:") }

// LookupEnv resolves environment variables; os.LookupEnv in production.
type LookupEnv func(string) (string, bool)

// OutputDir is the directory name Aval reserves for its own outputs. A
// directory with this name is skipped at any depth, like ".git".
const OutputDir = ".aval"

// Compute fingerprints the contract c rooted at dir. outputs are files Aval
// itself writes (the --result and --report paths); the ones inside dir are
// not inputs and are skipped. Empty entries are ignored.
func Compute(dir string, c *contract.Contract, env LookupEnv, outputs ...string) (Fingerprint, error) {
	if env == nil {
		env = os.LookupEnv
	}
	canon := canonicalContract(c)
	files, err := listFiles(dir, outputsUnder(dir, outputs))
	if err != nil {
		return Fingerprint{}, err
	}

	var lines []string
	lines = append(lines, Version, "contract "+sum(canon))

	src, err := hashMatching(dir, files, "source", c.Scope.Source, c.Scope.Exclude)
	if err != nil {
		return Fingerprint{}, err
	}
	if len(src) == 0 {
		return Fingerprint{}, errors.New("scope.source matched no files")
	}
	lines = append(lines, src...)

	deps, err := hashMatching(dir, files, "dependency", c.Scope.Dependencies, nil)
	if err != nil {
		return Fingerprint{}, err
	}
	lines = append(lines, deps...)

	var section []string
	for k, v := range c.Environment.Toolchain {
		section = append(section, fmt.Sprintf("toolchain %s=%s", k, v))
	}
	slices.Sort(section)
	lines = append(lines, section...)

	section = nil
	for _, name := range c.Environment.Env {
		v, ok := env(name)
		h := "unset"
		if ok {
			h = sum([]byte(v))
		}
		section = append(section, fmt.Sprintf("env %s %s", name, h))
	}
	slices.Sort(section)
	lines = append(lines, slices.Compact(section)...)

	manifest := strings.Join(lines, "\n") + "\n"
	return Fingerprint{Digest: "sha256:" + sum([]byte(manifest)), Manifest: manifest}, nil
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

// listFiles returns all regular files and symlinks under dir as sorted,
// slash-separated relative paths. VCS metadata, Aval's output directories
// and the paths in skip are left out.
func listFiles(dir string, skip map[string]bool) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == OutputDir {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p) // cannot fail: WalkDir yields paths under dir
		if rel = filepath.ToSlash(rel); !skip[rel] {
			out = append(out, rel)
		}
		return nil
	})
	slices.Sort(out)
	return out, err
}

// hashMatching hashes every file matching any include glob and no exclude
// glob. Every include glob must match at least one file (before exclude): a
// glob that matches nothing is a typo or a name that differs on disk (case,
// Unicode form), and ignoring it would silently drop a declared input.
func hashMatching(dir string, files []string, kind string, include, exclude []string) ([]string, error) {
	var lines []string
	for _, g := range include {
		n := 0
		for _, f := range files {
			if contract.Match(g, f) {
				n++
			}
		}
		if n == 0 {
			return nil, fmt.Errorf("%s glob %q matched no files", kind, g)
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
			if !slices.ContainsFunc(exclude, func(g string) bool { return contract.MatchContents(g, f) }) {
				return nil, fmt.Errorf("%s %q is excluded only by a trailing \"**\", which means the files under it: exclude it by name or narrow the glob", kind, f)
			}
			continue
		}
		// Paths go into a line-oriented manifest unescaped; a newline in a
		// name could forge extra lines and collide with another tree.
		if strings.ContainsAny(f, "\n\r") {
			return nil, fmt.Errorf("%s %q: control characters in path are not supported", kind, f)
		}
		h, mode, err := hashFile(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", kind, f, err)
		}
		lines = append(lines, fmt.Sprintf("%s %s %s %s", kind, h, mode, f))
	}
	return lines, nil // files is sorted, so lines are too
}

func matchesAny(globs []string, f string) bool {
	return slices.ContainsFunc(globs, func(g string) bool { return contract.Match(g, f) })
}

// hashFile returns the content hash and a portable mode ("x" executable,
// "-" otherwise). Symlinks are rejected: their target may live outside the
// scope, and we cannot prove it did not change (fail closed).
func hashFile(p string) (string, string, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return "", "", err
	}
	if !fi.Mode().IsRegular() {
		return "", "", errors.New("only regular files are supported in scope (symlinks and devices are rejected)")
	}
	h := sha256.New()
	f, err := os.Open(p)
	if err == nil { // one error branch for open and read
		defer f.Close()
		_, err = io.Copy(h, f)
	}
	if err != nil {
		return "", "", err
	}
	mode := "-"
	if fi.Mode()&0o111 != 0 {
		mode = "x"
	}
	return hex.EncodeToString(h.Sum(nil)), mode, nil
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
