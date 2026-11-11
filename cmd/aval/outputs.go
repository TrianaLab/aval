package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/internal/fingerprint"
	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// collectOutputs reads the files the outputs globs of c match under its
// root dir (contractRoot; name is the contract file relative to it), sorted
// by path. Every glob must match a file; a
// match that is not a regular file (a symlink), that is an input (in scope)
// or the contract file, or that breaks a cap is an error, so nothing is
// published. .git entries (directory, file or symlink) and .aval directories
// are skipped, as in the fingerprint. strict is for a directory that holds
// only outputs (--outputs-dir): there, anything that is not a declared
// output, .git entries and .aval directories included, is an error too.
func collectOutputs(dir, name string, c *contract.Contract, strict bool) ([]registry.Output, error) {
	globs := splitGlobs(c.Outputs)
	matched := make([]bool, len(globs))
	var out []registry.Output
	var total int64
	err := fs.WalkDir(os.DirFS(dir), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil || rel == "." {
			return err
		}
		descend, output := outputEntry(globs, matched, strings.Split(rel, "/"), d)
		switch {
		case descend:
			return nil
		case !output && strict:
			return fmt.Errorf("%s holds %s, which is not a declared output: nothing is published", dir, rel)
		case !output && d.IsDir():
			return fs.SkipDir
		case !output:
			return nil
		}
		if len(out) == registry.MaxOutputFiles {
			return fmt.Errorf("more than %d output files", registry.MaxOutputFiles)
		}
		data, err := readOutput(dir, name, c, rel, d)
		if err != nil {
			return err
		}
		if total += int64(len(data)); len(data) > registry.MaxOutputSize || total > registry.MaxOutputTotal {
			return fmt.Errorf("output %s: outputs are capped at %d bytes per file and %d bytes in total", rel, registry.MaxOutputSize, registry.MaxOutputTotal)
		}
		out = append(out, registry.Output{Path: rel, Data: data})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if i := slices.Index(matched, false); i >= 0 {
		return nil, fmt.Errorf("outputs glob %q matched no files", c.Outputs[i])
	}
	return out, nil
}

// readOutput reads the output rel under the contract root dir, of at most
// one byte over the per-file cap, unless it is not a regular file, has a
// path evidence.Parse rejects, is an input or is the contract file (name,
// relative to dir).
func readOutput(dir, name string, c *contract.Contract, rel string, d fs.DirEntry) ([]byte, error) {
	p := filepath.Join(dir, filepath.FromSlash(rel))
	switch {
	case !d.Type().IsRegular():
		return nil, fmt.Errorf("output %s is not a regular file (symlinks are not published)", rel)
	case strings.ContainsFunc(rel, func(r rune) bool { return r == '\\' || unicode.IsControl(r) }):
		return nil, fmt.Errorf("output %q: backslashes and control characters in a path are not supported", rel)
	case inScope(dir, c, p):
		return nil, fmt.Errorf("output %s is an input of the test (the scope includes it): exclude it from scope", rel)
	case rel == name:
		return nil, fmt.Errorf("output %s is the contract: narrow the outputs globs", rel)
	}
	f, err := os.Open(p)
	var data []byte
	if err == nil { // one error branch for open and read
		data, err = io.ReadAll(io.LimitReader(f, registry.MaxOutputSize+1))
		_ = f.Close() // read-only
	}
	return data, err
}

// verifyOutputs checks the outputs a verified predicate lists against the
// contract c, loaded from file, then fetches them. The contract and the
// predicate both have outputs or neither does; every path matches an
// outputs glob, is not the contract file, lies outside the scope and
// outside .git and .aval; every glob matches a path. Anything else is a verification failure: the
// publisher refuses all of it, so only a forged result gets here.
func verifyOutputs(ctx context.Context, repo string, c *contract.Contract, file string, o *evidence.Outputs) ([]registry.Output, error) {
	if o == nil {
		if len(c.Outputs) > 0 {
			return nil, &evidence.VerificationError{Reason: "the result carries no outputs, the contract declares outputs"}
		}
		return nil, nil
	}
	dir, name, err := contractRoot(file, c)
	if err != nil {
		return nil, err
	}
	globs := splitGlobs(c.Outputs)
	matched := make([]bool, len(globs))
	for _, f := range o.Files {
		segs := strings.Split(f.Path, "/")
		reserved := slices.ContainsFunc(segs, func(s string) bool { return s == ".git" || s == fingerprint.OutputDir })
		if !matchOutput(globs, matched, segs) || reserved || f.Path == name || inScope(dir, c, filepath.Join(dir, filepath.FromSlash(f.Path))) {
			return nil, &evidence.VerificationError{Reason: fmt.Sprintf("output %q is not a declared output of the contract, or is an input", f.Path)}
		}
	}
	if i := slices.Index(matched, false); i >= 0 {
		return nil, &evidence.VerificationError{Reason: fmt.Sprintf("outputs glob %q matches no output of the result", c.Outputs[i])}
	}
	return fetchOutputs(ctx, repo, o)
}

// restore writes verified outputs under dir, the contract root, and prints
// one line each. Each
// is staged next to its target, and renamed into place only once all are
// staged. It never writes through a symlink or over anything but a regular
// file.
// ponytail: an alias of an input on a case-insensitive file system is not
// detected; the outputs come from a signer the policy trusts.
func restore(w io.Writer, dir string, files []registry.Output) error {
	var staged []string
	defer func() {
		for _, s := range staged {
			_ = os.Remove(s) // gone after the rename
		}
	}()
	for _, f := range files {
		target := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := writable(dir, f.Path); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(target), ".aval-restore-*")
		if err == nil { // one error branch for create and write
			staged = append(staged, tmp.Name())
			_, err = tmp.Write(f.Data)
			err = errors.Join(err, tmp.Chmod(0o644), tmp.Close())
		}
		if err != nil {
			return err
		}
	}
	for i, f := range files {
		if err := os.Rename(staged[i], filepath.Join(dir, filepath.FromSlash(f.Path))); err != nil {
			return err
		}
		fmt.Fprintf(w, "restored %s\n", f.Path)
	}
	return nil
}

// writable fails when an existing component of the slash-separated path rel
// under dir is a symlink, or rel exists and is not a regular file. A
// component it cannot stat stops the check: writing there fails too.
func writable(dir, rel string) error {
	p := dir
	var fi fs.FileInfo
	for _, s := range strings.Split(rel, "/") {
		p = filepath.Join(p, s)
		var err error
		if fi, err = os.Lstat(p); err != nil {
			return nil
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink: outputs are never written through one", p)
		}
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", p)
	}
	return nil
}

// outputEntry reports whether collectOutputs walks into the entry d at the
// split path segs, a directory that may hold outputs, or reads it, a file
// that matches a glob (marked in matched). .git entries of any type and
// .aval directories are neither.
func outputEntry(globs []contract.Glob, matched []bool, segs []string, d fs.DirEntry) (descend, output bool) {
	switch {
	case d.Name() == ".git", d.IsDir() && d.Name() == fingerprint.OutputDir:
		return false, false
	case d.IsDir():
		return slices.ContainsFunc(globs, func(g contract.Glob) bool { return g.MayContain(segs) }), false
	}
	return false, matchOutput(globs, matched, segs)
}

// matchOutput reports whether the split path matches any of globs, and
// marks each glob it matches.
func matchOutput(globs []contract.Glob, matched []bool, segs []string) bool {
	hit := false
	for i, g := range globs {
		if g.Match(segs) {
			matched[i], hit = true, true
		}
	}
	return hit
}

func splitGlobs(globs []string) []contract.Glob {
	out := make([]contract.Glob, len(globs))
	for i, g := range globs {
		out[i] = contract.SplitGlob(g)
	}
	return out
}

// saveOutputs copies the outputs of c, as push would collect them, into
// dst (--outputs-dir), so a later 'aval push --outputs-dir' in another job
// publishes the files this run wrote. dst is empty or missing (preflight).
func saveOutputs(dst, file string, c *contract.Contract) error {
	dir, name, err := contractRoot(file, c)
	var files []registry.Output
	if err == nil {
		files, err = collectOutputs(dir, name, c, false)
	}
	for _, f := range files {
		p := filepath.Join(dst, filepath.FromSlash(f.Path))
		if err = os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
			err = os.WriteFile(p, f.Data, 0o644)
		}
		if err != nil {
			_ = os.RemoveAll(dst) // fail closed: no partial set; dst was empty or missing (preflight)
			break
		}
	}
	return err
}

// outputsFrom checks --outputs-dir of push: dir is a directory, not a
// symlink to one, and the contract declares outputs. A missing dir is fine
// only for a contract without outputs; with outputs, push never falls back
// to the checkout.
func outputsFrom(dir string, c *contract.Contract) error {
	fi, err := os.Lstat(dir)
	if len(c.Outputs) == 0 {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("--outputs-dir %s: the contract declares no outputs: nothing is published", dir)
	}
	if err == nil && !fi.IsDir() {
		err = errors.New("not a directory (symlinks are not followed)")
	}
	if err != nil {
		return fmt.Errorf("--outputs-dir %s: %w", dir, err)
	}
	return nil
}

// emptyOrMissing fails unless dir does not exist or is an empty directory.
func emptyOrMissing(dir string) error {
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) > 0 {
		err = errors.New("is not empty")
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
