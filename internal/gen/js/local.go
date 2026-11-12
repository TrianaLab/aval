package js

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/TrianaLab/aval/internal/gen"
)

// workspace locates paths in the workspace packages of a repository.
type workspace struct {
	root   string
	byDir  map[string]string // package directory (slash, relative to root) -> name
	byName map[string]string // name -> package directory
}

// owner returns the directory of the deepest workspace package that holds
// p, a clean slash path relative to root, or "" when none does.
func (w workspace) owner(p string) string {
	best := ""
	for dir := range w.byDir {
		if (p == dir || strings.HasPrefix(p, dir+"/")) && len(dir) > len(best) {
			best = dir
		}
	}
	return best
}

// reached collects what the files of one package reach outside it.
type reached struct {
	from  string   // the package directory
	deps  []string // other workspace packages, by name
	files []string // files outside every workspace package
}

// add records target as a dependency when another workspace package holds
// it and reports whether any package holds it.
func (w workspace) add(r *reached, target string) bool {
	o := w.owner(target)
	if o != "" && o != r.from {
		r.deps = append(r.deps, w.byDir[o])
	}
	return o != ""
}

// sourceExt are the file kinds scanned for import specifiers.
var sourceExt = []string{".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts", ".vue", ".svelte", ".astro"}

// specifierRe matches a relative specifier of import ... from, export ...
// from, a side-effect or dynamic import and require. Comments are not
// stripped: a commented-out import still counts, which errs towards
// re-running.
var specifierRe = regexp.MustCompile(`(?:\bfrom\s*|\b(?:import|require)\s*\(?\s*)['"` + "`" + `](\.\.?(?:/[^'"` + "`" + `\n]*)?)['"` + "`" + `]`)

// scan walks the package in dir (its source files and tsconfig*.json, not
// node_modules, .git or nested workspace packages) and
// returns the workspace packages it reaches through relative imports and
// TypeScript references and extends, and the files outside every package
// its tsconfig extends. A relative import outside every package, or one it
// cannot resolve, is an error naming the file and line.
func (w workspace) scan(dir string) (deps, files []string, err error) {
	r := &reached{from: dir}
	seen := map[string]bool{}
	err = filepath.WalkDir(filepath.Join(w.root, filepath.FromSlash(dir)), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(w.root, p)
		rel = filepath.ToSlash(rel)
		name := d.Name()
		if d.IsDir() {
			if rel != dir && (name == "node_modules" || name == ".git" || w.byDir[rel] != "") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, "tsconfig") && strings.HasSuffix(name, ".json") {
			return w.tsconfig(r, rel, true, seen)
		}
		if slices.Contains(sourceExt, path.Ext(name)) {
			return w.imports(r, rel)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return r.deps, r.files, nil
}

// imports adds the packages the relative specifiers of file reach.
func (w workspace) imports(r *reached, file string) error {
	data, err := os.ReadFile(filepath.Join(w.root, filepath.FromSlash(file)))
	if err != nil {
		return err
	}
	for _, m := range specifierRe.FindAllSubmatchIndex(data, -1) {
		spec := string(data[m[2]:m[3]])
		at := fmt.Sprintf("%s:%d: %q", file, 1+bytes.Count(data[:m[0]], []byte("\n")), spec)
		if strings.Contains(spec, "${") {
			return fmt.Errorf("%s: cannot resolve a computed relative specifier", at)
		}
		target, err := under(path.Dir(file), spec)
		if err != nil {
			return fmt.Errorf("%s: %w", at, err)
		}
		if !w.add(r, target) {
			return fmt.Errorf("%s: %s is outside every workspace package: move it into one or make it a workspace package", at, target)
		}
	}
	return nil
}

// tsconfig adds what file reaches: its references when refs is set, and
// every file its extends chain reads. extends does not inherit references,
// so the chain is followed without them.
func (w workspace) tsconfig(r *reached, file string, refs bool, seen map[string]bool) error {
	key := file // a file seen through extends has not had its references read
	if refs {
		key += "\x00refs"
	}
	if seen[key] {
		return nil
	}
	seen[key] = true
	data, err := os.ReadFile(filepath.Join(w.root, filepath.FromSlash(file)))
	if err != nil {
		return err
	}
	var tc struct {
		Extends    stringList
		References []struct{ Path string }
	}
	if err := json.Unmarshal(jsonc(data), &tc); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	dir := path.Dir(file)
	for _, ref := range tc.References {
		if !refs {
			break
		}
		target, err := under(dir, ref.Path)
		if err != nil || !w.add(r, target) {
			return fmt.Errorf("%s: references: %q is not in a workspace package", file, ref.Path)
		}
	}
	for _, e := range tc.Extends {
		target, ok, err := w.extended(dir, e)
		if err != nil {
			return fmt.Errorf("%s: extends: %w", file, err)
		}
		if !ok {
			continue // an external package: the lockfile in defaults covers it
		}
		if !w.add(r, target) {
			g, err := gen.Literal(target)
			if err != nil {
				return fmt.Errorf("%s: extends: %w", file, err)
			}
			r.files = append(r.files, g)
		}
		if err := w.tsconfig(r, target, false, seen); err != nil {
			return err
		}
	}
	return nil
}

// extended resolves the extends entry e of a tsconfig in dir to a file
// relative to root, the way TypeScript does: a relative path, a directory
// meaning its tsconfig.json, ".json" added when the name lacks it and does
// not exist. ok is false for a package that is not a workspace package.
func (w workspace) extended(dir, e string) (target string, ok bool, err error) {
	switch {
	case strings.HasPrefix(e, "/"):
		return "", false, fmt.Errorf("%q: an absolute path", e)
	case e == "." || e == ".." || strings.HasPrefix(e, "./") || strings.HasPrefix(e, "../"):
		if target, err = under(dir, e); err != nil {
			return "", false, fmt.Errorf("%q: %w", e, err)
		}
	default:
		n := 1 // a package name, then a path in it
		if strings.HasPrefix(e, "@") {
			n = 2
		}
		seg := strings.SplitN(e, "/", n+1)
		name := strings.Join(seg[:min(n, len(seg))], "/")
		rest := strings.Join(seg[min(n, len(seg)):], "/")
		d, ok := w.byName[name]
		if !ok {
			return "", false, nil
		}
		target = path.Join(d, rest)
	}
	full := filepath.Join(w.root, filepath.FromSlash(target))
	if fi, err := os.Stat(full); err == nil && fi.IsDir() {
		target = path.Join(target, "tsconfig.json")
	} else if errors.Is(err, fs.ErrNotExist) && !strings.HasSuffix(target, ".json") {
		target += ".json"
	}
	return target, true, nil
}

// stringList is a JSON string or list of strings, like tsconfig extends.
type stringList []string

func (l *stringList) UnmarshalJSON(data []byte) error {
	var one string
	if json.Unmarshal(data, &one) == nil {
		*l = []string{one}
		return nil
	}
	if json.Unmarshal(data, (*[]string)(l)) != nil {
		return errors.New("extends: want a string or a list of strings")
	}
	return nil
}

// under joins spec to dir, both relative to the contract root, and fails
// when the result leaves it.
func under(dir, spec string) (string, error) {
	p := path.Join(dir, spec)
	if p == ".." || strings.HasPrefix(p, "../") || path.IsAbs(spec) {
		return "", errors.New("outside the contract root")
	}
	return p, nil
}

// jsonc turns the JSON with comments and trailing commas of a tsconfig into
// JSON.
func jsonc(data []byte) []byte {
	var out []byte
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case c == '"':
			j := stringEnd(data, i)
			out = append(out, data[i:min(j+1, len(data))]...)
			i = j
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			i += bytes.IndexByte(append(data[i:len(data):len(data)], '\n'), '\n') - 1 // keep the newline
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			end := bytes.Index(data[i+2:], []byte("*/"))
			if end < 0 {
				return append(out, data[i:]...) // unterminated: let json report it
			}
			i += end + 3
		case c == '}' || c == ']':
			out = append(bytes.TrimSuffix(bytes.TrimRight(out, " \t\r\n"), []byte(",")), c)
		default:
			out = append(out, c)
		}
	}
	return out
}

// stringEnd returns the index of the quote that closes the JSON string
// opening at i, or len(data) when it does not close.
func stringEnd(data []byte, i int) int {
	for i++; i < len(data) && data[i] != '"'; i++ {
		if data[i] == '\\' {
			i++
		}
	}
	return i
}
