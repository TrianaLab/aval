// Package gen turns a repository's dependency graph into declared scope in a
// kind: TestSet: one fragment per unit with the unit's own files, and one
// test per unit that uses the fragments of the unit and of every unit it
// depends on, directly or not. It runs at authoring time; aval run, verify
// and push never call it (invariant 2). It knows no language: an Adapter
// lists the units of one ecosystem (invariant 8).
package gen

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/TrianaLab/aval/internal/contract"
)

// Unit is one testable part of a repository: a package, a workspace
// package, a module, a crate, a project.
type Unit struct {
	// ID is the unit's stable identity in its ecosystem, such as a package
	// path relative to the module. The generated names derive from it.
	ID string
	// Source are globs of the unit's own files, relative to the contract
	// root. Shared inputs (lockfiles, root build files) belong in the
	// hand-written defaults or fragments, not here.
	Source []string
	// Deps are the IDs of the units this unit depends on directly.
	Deps []string
	// Command is the argv that tests this unit.
	Command []string
}

// globEscape turns the glob metacharacters path.Match knows into one-character
// classes. `]`, `{` and `}` are literal to path.Match outside a class, so they
// stay as they are; contract globs reject backslashes, so `\` cannot be
// escaped.
var globEscape = strings.NewReplacer("*", "[*]", "?", "[?]", "[", "[[]")

// Literal returns the glob that matches exactly the slash-separated path p,
// relative to the contract root. Adapters use it, or Under, for every real
// file or directory name in Unit.Source, so a name with glob metacharacters
// still selects its files. A path a contract glob cannot express (a
// backslash, an empty, "." or ".." segment) is an error (invariant 3).
func Literal(p string) (string, error) {
	g := globEscape.Replace(p)
	if contract.ValidateGlob(g) != nil { // rejects backslashes too
		return "", fmt.Errorf("%q: a contract glob cannot express this path; rename it", p)
	}
	return g, nil
}

// Under returns the glob pattern below the directory dir: Literal(dir), a
// slash, then pattern, which is a glob already. dir "." is the contract
// root, where the result is pattern itself.
func Under(dir, pattern string) (string, error) {
	if dir == "." {
		return pattern, nil
	}
	g, err := Literal(dir)
	if err != nil {
		return "", err
	}
	return g + "/" + pattern, nil
}

// Adapter lists the units of one ecosystem under root, the contract root.
// A missing tool, a parse error or a unit whose files cannot be listed is
// an error, never a partial list (invariant 3).
type Adapter interface {
	Units(root string) ([]Unit, error)
}

// ErrUnits marks a problem in the units an adapter returned (an unknown
// dependency, a duplicate ID, two IDs with the same name), as opposed to a
// problem in the contract file.
var ErrUnits = errors.New("invalid units")

var langRe = regexp.MustCompile(`^[a-z0-9]+$`)

// Name returns the fragment and test name of the unit id generated for
// lang: lang, a dot, then id lower-cased with each "/" turned into "." and
// each run of other characters outside [a-z0-9._-] into "-"; a name over 64
// characters is cut and ends in a hash of id. It depends on
// id alone, so adding or removing other units never renames a unit.
func Name(lang, id string) (string, error) {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(id) {
		switch {
		case r == '/':
			b.WriteByte('.')
			dash = false
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
			dash = false
		case !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "._-")
	if s == "" {
		return "", fmt.Errorf("unit %q: the ID gives an empty name", id)
	}
	name := lang + "." + s
	if len(name) > 64 { // still unique: the suffix hashes the whole id
		sum := sha256.Sum256([]byte(id))
		name = strings.TrimRight(name[:55], "._-") + "-" + hex.EncodeToString(sum[:4])
	}
	if err := contract.ValidateName(name); err != nil {
		return "", fmt.Errorf("unit %q: %w", id, err)
	}
	return name, nil
}

// owned reports whether a fragment or test name belongs to the generator of
// lang: the prefix "<lang>." is reserved for it.
func owned(lang, name string) bool { return strings.HasPrefix(name, lang+".") }

// planned is one unit ready to write: its name, its own files, its command
// and the names of the fragments its test uses (itself and its transitive
// dependencies), sorted.
type planned struct {
	name    string
	source  []string
	command []string
	use     []string
}

// plan checks units and computes each unit's transitive closure, sorted by
// name. A cycle is allowed: every unit on it reaches every other, so a
// change to any of them re-runs all of their tests.
func plan(lang string, units []Unit) ([]planned, error) {
	out, byID, err := check(lang, units)
	if err != nil {
		return nil, err
	}
	// ponytail: one walk per unit, O(n*(n+e)); a TestSet holds at most 1024 tests.
	for i := range units {
		seen := map[int]bool{i: true}
		stack := []int{i}
		for len(stack) > 0 {
			u := units[stack[len(stack)-1]]
			stack = stack[:len(stack)-1]
			for _, d := range u.Deps {
				if j := byID[d]; !seen[j] {
					seen[j] = true
					stack = append(stack, j)
				}
			}
		}
		for j := range seen {
			out[i].use = append(out[i].use, out[j].name)
		}
		slices.Sort(out[i].use)
	}
	slices.SortFunc(out, func(a, b planned) int { return strings.Compare(a.name, b.name) })
	return out, nil
}

// check validates lang and units and returns each unit's name and files,
// in the order of units, and the index of each ID.
func check(lang string, units []Unit) ([]planned, map[string]int, error) {
	if !langRe.MatchString(lang) {
		return nil, nil, fmt.Errorf("language %q: want [a-z0-9]+", lang)
	}
	if len(units) == 0 {
		return nil, nil, fmt.Errorf("%w: no units found", ErrUnits)
	}
	var errs []error
	byID := map[string]int{}
	names := map[string]string{}
	out := make([]planned, len(units))
	for i, u := range units {
		if _, dup := byID[u.ID]; dup {
			errs = append(errs, fmt.Errorf("unit %q: listed twice", u.ID))
			continue
		}
		byID[u.ID] = i
		name, err := Name(lang, u.ID)
		if other, dup := names[name]; err == nil && dup {
			err = fmt.Errorf("units %q and %q both get the name %s", other, u.ID, name)
		}
		if len(u.Source) == 0 {
			err = errors.Join(err, fmt.Errorf("unit %q: no source files", u.ID))
		}
		if len(u.Command) == 0 {
			err = errors.Join(err, fmt.Errorf("unit %q: no test command", u.ID))
		}
		names[name] = u.ID
		errs = append(errs, err)
		out[i] = planned{name: name, source: slices.Compact(slices.Sorted(slices.Values(u.Source))), command: u.Command}
	}
	for _, u := range units {
		for _, d := range u.Deps {
			if _, ok := byID[d]; !ok {
				errs = append(errs, fmt.Errorf("unit %q: unknown dependency %q", u.ID, d))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrUnits, err)
	}
	return out, byID, nil
}

// Root returns the contract root of the TestSet data in dir: dir, or the
// directory defaults.scope.root names above it. Adapters list units there.
func Root(data []byte, dir string) (string, error) {
	doc, err := parseSet(data)
	if err != nil {
		return "", err
	}
	root := value(value(value(doc, "defaults"), "scope"), "root")
	if root == nil || root.Value == "" {
		return dir, nil
	}
	for seg := range strings.SplitSeq(root.Value, "/") {
		if seg != ".." {
			return "", fmt.Errorf("defaults.scope.root: %q must be .. segments only", root.Value)
		}
	}
	return filepath.Join(dir, filepath.FromSlash(root.Value)), nil
}
