package contract

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

// KindTestSet is a file of many tests that share defaults and fragments.
// Each test expands to a standalone kind: Test (ParseAll).
const KindTestSet = "TestSet"

// ErrTestSet is a kind: TestSet where one kind: Test is expected (Parse,
// and so scope.contracts).
var ErrTestSet = errors.New("kind: TestSet holds many tests; this place takes one kind: Test")

// maxTests bounds the tests one TestSet expands to, plain tests and
// matrices together.
const maxTests = 1024

// testSet is the parsed form of a kind: TestSet file.
type testSet struct {
	APIVersion string           `yaml:"apiVersion"`
	Kind       string           `yaml:"kind"`
	Defaults   entry            `yaml:"defaults"`
	Fragments  map[string]entry `yaml:"fragments"`
	Tests      []entry          `yaml:"tests"`
}

// entry is a test of a TestSet, or its defaults, or a fragment: the body of a
// kind: Test plus use and matrix. Which keys each may set is checked by
// forbidden.
type entry struct {
	Metadata     Metadata            `yaml:"metadata"`
	Runner       Runner              `yaml:"runner"`
	Scope        Scope               `yaml:"scope"`
	Environment  Environment         `yaml:"environment"`
	Cacheability Cacheability        `yaml:"cacheability"`
	Outputs      []string            `yaml:"outputs"`
	Use          []string            `yaml:"use"`
	Matrix       map[string][]string `yaml:"matrix"`
}

// forbidden returns a message for each key of e that is set although not
// allowed in a fragment (frag) or in defaults.
func (e entry) forbidden(frag bool) []string {
	var out []string
	no := func(set bool, key, why string) {
		if set {
			out = append(out, key+" is not allowed here: "+why)
		}
	}
	no(e.Metadata.Name != "", "metadata", "it belongs to a test only")
	no(len(e.Runner.Command) > 0, "runner", "it belongs to a test only")
	no(len(e.Outputs) > 0, "outputs", "they belong to a test only")
	no(e.Matrix != nil, "matrix", "it belongs to a test only")
	if frag {
		no(len(e.Use) > 0, "use", "a fragment cannot use another fragment")
		no(len(e.Scope.Exclude) > 0, "scope.exclude", "exclude is allowed only in defaults or in a test, where it is visible")
		no(e.Cacheability != "", "cacheability", "set it in defaults or in the test")
	} else {
		no(len(e.Use) > 0, "use", "it belongs to a test only")
	}
	return out
}

var (
	varRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	refRe = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// ParseAll decodes a kind: Test or a kind: TestSet and returns its tests,
// each a valid standalone kind: Test, in file order (a matrix expands in
// place, by sorted variable name, then value order).
func ParseAll(data []byte) ([]*Contract, error) {
	if peekKind(data) != KindTestSet {
		c, err := Parse(data)
		if err != nil {
			return nil, err
		}
		return []*Contract{c}, nil
	}
	var s testSet
	if err := decodeStrict(data, &s); err != nil {
		return nil, err
	}
	return s.expand()
}

// expand merges and expands every test, or returns every problem found.
func (s *testSet) expand() ([]*Contract, error) {
	errs := s.checkKeys()
	var out []*Contract
	names := map[string]int{}
	used := map[string]bool{}
	for i, t := range s.Tests {
		at := fmt.Sprintf("tests[%d]", i)
		if t.Metadata.Name != "" {
			at += " (" + t.Metadata.Name + ")"
		}
		if len(out) >= maxTests {
			return nil, errors.Join(append(errs, fmt.Errorf("%s: the set expands to more than %d tests", at, maxTests))...)
		}
		cs, err := s.test(t, used, maxTests-len(out))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", at, err))
			continue
		}
		for _, c := range cs {
			if err := c.Validate(); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", at, err))
			} else if j, dup := names[c.Metadata.Name]; dup {
				errs = append(errs, fmt.Errorf("%s: metadata.name %q is also the name of tests[%d]", at, c.Metadata.Name, j))
			} else {
				names[c.Metadata.Name] = i
			}
		}
		out = append(out, cs...)
	}
	for _, name := range slices.Sorted(maps.Keys(s.Fragments)) {
		if !used[name] {
			errs = append(errs, fmt.Errorf("fragments.%s: not used by any test", name))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

// checkKeys checks apiVersion, which keys defaults and fragments set, the
// fragment names, and that there is a test.
func (s *testSet) checkKeys() []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if err := CheckAPIVersion(s.APIVersion); err != nil {
		errs = append(errs, err)
	}
	for _, m := range s.Defaults.forbidden(false) {
		add("defaults: %s", m)
	}
	for _, name := range slices.Sorted(maps.Keys(s.Fragments)) {
		if !nameRe.MatchString(name) {
			add("fragments.%s: the name must be 1-64 chars of [a-z0-9._-], starting and ending alphanumeric", name)
		}
		for _, m := range s.Fragments[name].forbidden(true) {
			add("fragments.%s: %s", name, m)
		}
	}
	if len(s.Tests) == 0 {
		add("tests: at least one test is required")
	}
	return errs
}

// test merges t with defaults and the fragments it uses (marking them in
// used) and expands its matrix into at most limit tests.
func (s *testSet) test(t entry, used map[string]bool, limit int) ([]*Contract, error) {
	layers := []layer{{"defaults", s.Defaults}}
	var errs []error
	for _, f := range slices.Compact(slices.Sorted(slices.Values(t.Use))) {
		used[f] = true
		if e, ok := s.Fragments[f]; ok {
			layers = append(layers, layer{"fragment " + f, e})
		} else {
			errs = append(errs, fmt.Errorf("use: unknown fragment %q", f))
		}
	}
	c, err := merge(append(layers, layer{"the test", t}))
	if err := errors.Join(append(errs, err)...); err != nil {
		return nil, err
	}
	return matrix(c, t.Matrix, limit)
}

// layer is one source of a test's keys, with the name errors give it.
type layer struct {
	name string
	e    entry
}

// merge folds layers (defaults, the used fragments, the test) into one
// kind: Test. Lists are unioned, so nothing inherited is removed; a map key
// or scope.root set by two layers to different values is an error naming
// both. metadata, runner and outputs come from the test, which the last
// layer is; its cacheability overrides the defaults'.
func merge(layers []layer) (*Contract, error) {
	c := &Contract{APIVersion: APIVersion, Kind: Kind}
	var errs []error
	rootFrom, toolFrom, probeFrom := "", map[string]string{}, map[string]string{}
	for _, l := range layers {
		s, e := l.e.Scope, l.e.Environment
		c.Scope.Source = union(c.Scope.Source, s.Source)
		c.Scope.OptionalSource = union(c.Scope.OptionalSource, s.OptionalSource)
		c.Scope.Exclude = union(c.Scope.Exclude, s.Exclude)
		c.Scope.Dependencies = union(c.Scope.Dependencies, s.Dependencies)
		c.Scope.AllowMutable = union(c.Scope.AllowMutable, s.AllowMutable)
		c.Scope.Resolve = union(c.Scope.Resolve, s.Resolve)
		c.Scope.Contracts = union(c.Scope.Contracts, s.Contracts)
		c.Environment.Env = union(c.Environment.Env, e.Env)
		if s.Root != "" {
			if rootFrom != "" && c.Scope.Root != s.Root {
				errs = append(errs, fmt.Errorf("scope.root: %q from %s conflicts with %q from %s", c.Scope.Root, rootFrom, s.Root, l.name))
			} else {
				c.Scope.Root, rootFrom = s.Root, l.name
			}
		}
		for _, k := range slices.Sorted(maps.Keys(e.Toolchain)) {
			v := e.Toolchain[k]
			if old, ok := c.Environment.Toolchain[k]; ok && old != v {
				errs = append(errs, fmt.Errorf("environment.toolchain.%s: %q from %s conflicts with %q from %s", k, old, toolFrom[k], v, l.name))
				continue
			}
			if c.Environment.Toolchain == nil {
				c.Environment.Toolchain = map[string]string{}
			}
			c.Environment.Toolchain[k], toolFrom[k] = v, l.name
		}
		for _, k := range slices.Sorted(maps.Keys(e.Probe)) {
			v := e.Probe[k]
			if old, ok := c.Environment.Probe[k]; ok && !slices.Equal(old, v) {
				errs = append(errs, fmt.Errorf("environment.probe.%s: %q from %s conflicts with %q from %s", k, old, probeFrom[k], v, l.name))
				continue
			}
			if c.Environment.Probe == nil {
				c.Environment.Probe = map[string][]string{}
			}
			c.Environment.Probe[k], probeFrom[k] = v, l.name
		}
		if l.e.Cacheability != "" {
			c.Cacheability = l.e.Cacheability
		}
	}
	t := layers[len(layers)-1].e
	c.Metadata, c.Runner, c.Outputs = t.Metadata, t.Runner, t.Outputs
	return c, errors.Join(errs...)
}

// union appends to a the items of b it does not hold yet. a is never a
// slice another contract shares: merge starts every list from nil.
func union[T any](a, b []T) []T {
	for _, x := range b {
		if !slices.ContainsFunc(a, func(y T) bool { return reflect.DeepEqual(x, y) }) {
			a = append(a, x)
		}
	}
	return a
}

// matrix expands c into one test per combination of the values of m, by
// sorted variable name, substituting {var} into metadata.name,
// runner.command and environment.toolchain values only. Without a matrix,
// c is the only test, nothing is substituted, and a {var} in a toolchain
// value is an error: it would be hashed as literal text. More than limit
// tests is an error.
func matrix(c *Contract, m map[string][]string, limit int) ([]*Contract, error) {
	if m == nil {
		if err := noMatrixRefs(c); err != nil {
			return nil, err
		}
		return []*Contract{c}, nil
	}
	if len(m) == 0 {
		return nil, errors.New("matrix: at least one variable is required")
	}
	vars := slices.Sorted(maps.Keys(m))
	if err := checkMatrix(c, m, vars, limit); err != nil {
		return nil, err
	}
	combos := []map[string]string{{}}
	for _, v := range vars {
		var next []map[string]string
		for _, b := range combos {
			for _, val := range m[v] {
				nb := maps.Clone(b)
				nb[v] = val
				next = append(next, nb)
			}
		}
		combos = next
	}
	out := make([]*Contract, len(combos))
	for i, vals := range combos {
		x := *c
		x.Metadata.Name = subst(c.Metadata.Name, vals)
		x.Runner.Command = make([]string, len(c.Runner.Command))
		for j, a := range c.Runner.Command {
			x.Runner.Command[j] = subst(a, vals)
		}
		x.Environment.Toolchain = maps.Clone(c.Environment.Toolchain) // nil stays nil
		for k, v := range c.Environment.Toolchain {
			x.Environment.Toolchain[k] = subst(v, vals)
		}
		out[i] = &x
	}
	return out, nil
}

// noMatrixRefs rejects a {var} in a toolchain value of c, a TestSet test
// without a matrix. A plain kind: Test is not checked: it is also what
// 'aval explain --expand' prints for a matrix leg, whose substituted value
// may hold "{x}".
func noMatrixRefs(c *Contract) error {
	var errs []error
	for _, k := range slices.Sorted(maps.Keys(c.Environment.Toolchain)) {
		if r := refRe.FindStringSubmatch(c.Environment.Toolchain[k]); r != nil {
			errs = append(errs, fmt.Errorf("environment.toolchain.%s: {%s} is a matrix reference but the test has no matrix", k, r[1]))
		}
	}
	return errors.Join(errs...)
}

// checkMatrix checks the variables and values of m, its size against
// limit, and that every {var} in c's name, command and toolchain values is
// a variable of m.
func checkMatrix(c *Contract, m map[string][]string, vars []string, limit int) error {
	var errs []error
	n := 1
	for _, v := range vars {
		if !varRe.MatchString(v) {
			errs = append(errs, fmt.Errorf("matrix: invalid variable name %q (want [A-Za-z_][A-Za-z0-9_]*)", v))
		}
		if len(m[v]) == 0 {
			errs = append(errs, fmt.Errorf("matrix.%s: at least one value is required", v))
		}
		n *= max(1, len(m[v]))
		if n > limit {
			return fmt.Errorf("matrix: expands to more than %d tests in the set", maxTests)
		}
	}
	unknown := func(where, s string) {
		for _, r := range refRe.FindAllStringSubmatch(s, -1) {
			if _, ok := m[r[1]]; !ok {
				errs = append(errs, fmt.Errorf("unknown matrix variable {%s} in %s%q (the matrix has %s)", r[1], where, s, strings.Join(vars, ", ")))
			}
		}
	}
	for _, s := range append([]string{c.Metadata.Name}, c.Runner.Command...) {
		unknown("", s)
	}
	for _, k := range slices.Sorted(maps.Keys(c.Environment.Toolchain)) {
		unknown("environment.toolchain."+k+" ", c.Environment.Toolchain[k])
	}
	return errors.Join(errs...)
}

// subst replaces each {var} in s with its value, in one pass, so a value
// that holds "{x}" is never substituted again.
func subst(s string, vals map[string]string) string {
	return refRe.ReplaceAllStringFunc(s, func(r string) string {
		return vals[r[1:len(r)-1]] // matrix rejected unknown variables
	})
}
