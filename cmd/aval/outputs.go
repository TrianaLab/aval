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

// collectOutputs reads the files the outputs globs of c, loaded from file,
// match under its directory, sorted by path. Every glob must match a file; a
// match that is not a regular file (a symlink), that is an input (in scope)
// or the contract file, or that breaks a cap is an error, so nothing is
// published. .git entries (directory, file or symlink) and .aval directories
// are skipped, as in the fingerprint.
func collectOutputs(file string, c *contract.Contract) ([]registry.Output, error) {
	dir := filepath.Dir(file)
	globs := splitGlobs(c.Outputs)
	matched := make([]bool, len(globs))
	var out []registry.Output
	var total int64
	err := fs.WalkDir(os.DirFS(dir), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		segs := strings.Split(rel, "/")
		if d.Name() == ".git" { // VCS metadata of any type, as in the fingerprint
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if rel != "." && (d.Name() == fingerprint.OutputDir || !slices.ContainsFunc(globs, func(g contract.Glob) bool { return g.MayContain(segs) })) {
				return fs.SkipDir
			}
			return nil
		}
		if !matchOutput(globs, matched, segs) {
			return nil
		}
		if len(out) == registry.MaxOutputFiles {
			return fmt.Errorf("more than %d output files", registry.MaxOutputFiles)
		}
		data, err := readOutput(file, c, rel, d)
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

// readOutput reads the output rel, of at most one byte over the per-file
// cap, unless it is not a regular file, has a path evidence.Parse rejects,
// is an input or is the contract file.
func readOutput(file string, c *contract.Contract, rel string, d fs.DirEntry) ([]byte, error) {
	dir := filepath.Dir(file)
	p := filepath.Join(dir, filepath.FromSlash(rel))
	switch {
	case !d.Type().IsRegular():
		return nil, fmt.Errorf("output %s is not a regular file (symlinks are not published)", rel)
	case strings.ContainsFunc(rel, func(r rune) bool { return r == '\\' || unicode.IsControl(r) }):
		return nil, fmt.Errorf("output %q: backslashes and control characters in a path are not supported", rel)
	case inScope(dir, c, p):
		return nil, fmt.Errorf("output %s is an input of the test (the scope includes it): exclude it from scope", rel)
	case rel == filepath.Base(file):
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
	dir := filepath.Dir(file)
	globs := splitGlobs(c.Outputs)
	matched := make([]bool, len(globs))
	for _, f := range o.Files {
		segs := strings.Split(f.Path, "/")
		reserved := slices.ContainsFunc(segs, func(s string) bool { return s == ".git" || s == fingerprint.OutputDir })
		if !matchOutput(globs, matched, segs) || reserved || f.Path == filepath.Base(file) || inScope(dir, c, filepath.Join(dir, filepath.FromSlash(f.Path))) {
			return nil, &evidence.VerificationError{Reason: fmt.Sprintf("output %q is not a declared output of the contract, or is an input", f.Path)}
		}
	}
	if i := slices.Index(matched, false); i >= 0 {
		return nil, &evidence.VerificationError{Reason: fmt.Sprintf("outputs glob %q matches no output of the result", c.Outputs[i])}
	}
	return fetchOutputs(ctx, repo, o)
}

// restore writes verified outputs under dir and prints one line each. Each
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
