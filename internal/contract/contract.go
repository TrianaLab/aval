// Package contract parses and validates aval.yaml, the test contract.
//
// The contract is the single source of truth for what a test depends on.
// Scope is always declared, never inferred: if an input is not declared here,
// Aval cannot prove it did not change, and reuse is unsafe.
package contract

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// APIVersion is the only contract and policy version accepted by this
// build. Frozen: changes within it are additive only (docs/contract.md).
const APIVersion = "aval.trianalab.io/v1"

// CheckAPIVersion rejects any apiVersion but APIVersion. A pre-freeze
// aval.dev/ version gets a hint that says which line to change.
func CheckAPIVersion(got string) error {
	if got == APIVersion {
		return nil
	}
	if strings.HasPrefix(got, "aval.dev/") {
		return fmt.Errorf("apiVersion: got %q, want %q: that is the pre-freeze version, change the line to apiVersion: %s", got, APIVersion, APIVersion)
	}
	return fmt.Errorf("apiVersion: got %q, want %q", got, APIVersion)
}

// Kind is the only contract kind accepted in v1.
const Kind = "Test"

// DefaultFile is the conventional contract file name.
const DefaultFile = "aval.yaml"

// Cacheability classifies how safe it is to reuse a result.
type Cacheability string

const (
	// Hermetic: the result depends only on declared files and toolchain.
	Hermetic Cacheability = "hermetic"
	// ControlledEnvironment: depends on declared services/images whose
	// versions are pinned in the scope (e.g. a compose file with digests).
	ControlledEnvironment Cacheability = "controlled-environment"
	// ExternalDependencies: talks to things outside the scope (SaaS APIs,
	// shared databases). Reuse only if policy explicitly allows it.
	ExternalDependencies Cacheability = "external-dependencies"
	// NonDeterministic: results vary between runs with identical inputs.
	// Never reusable by default.
	NonDeterministic Cacheability = "non-deterministic"
)

// ReusableByDefault reports whether results of this class may be reused
// without an explicit policy opt-in.
func (c Cacheability) ReusableByDefault() bool {
	return c == Hermetic || c == ControlledEnvironment
}

func (c Cacheability) valid() bool {
	switch c {
	case Hermetic, ControlledEnvironment, ExternalDependencies, NonDeterministic:
		return true
	}
	return false
}

// Contract is the parsed form of aval.yaml.
type Contract struct {
	APIVersion   string       `yaml:"apiVersion" json:"apiVersion"`
	Kind         string       `yaml:"kind" json:"kind"`
	Metadata     Metadata     `yaml:"metadata" json:"metadata"`
	Runner       Runner       `yaml:"runner" json:"runner"`
	Scope        Scope        `yaml:"scope" json:"scope"`
	Environment  Environment  `yaml:"environment,omitempty" json:"environment"`
	Cacheability Cacheability `yaml:"cacheability" json:"cacheability"`
}

// Metadata identifies the test. Name is the test identity: *what* is being
// checked, independent of the inputs of any particular execution.
type Metadata struct {
	Name string `yaml:"name" json:"name"`
}

// Runner describes how to execute the test. v0 only supports a generic
// command (argv form, no shell interpolation).
type Runner struct {
	Command []string `yaml:"command" json:"command"`
}

// Scope declares the inputs of the test. Paths are slash-separated globs
// relative to the contract directory; `**` matches any number of segments.
type Scope struct {
	// Source globs select files whose content is part of the fingerprint.
	Source []string `yaml:"source" json:"source"`
	// Exclude globs remove files selected by Source.
	Exclude []string `yaml:"exclude,omitempty" json:"exclude"`
	// Dependencies are lockfiles or manifests. Each glob must match at
	// least one file, otherwise fingerprinting fails (fail closed).
	Dependencies []string `yaml:"dependencies,omitempty" json:"dependencies"`
}

// Environment declares execution conditions that are part of the fingerprint.
type Environment struct {
	// Toolchain is a declared map such as {go: "1.25"}. v0 trusts the
	// declaration; detecting the real toolchain is future work.
	Toolchain map[string]string `yaml:"toolchain,omitempty" json:"toolchain"`
	// Env lists environment variable names whose values are hashed.
	Env []string `yaml:"env,omitempty" json:"env"`
}

var (
	nameRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$`)
	envRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	toolKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
)

// Load reads and validates a contract file.
func Load(file string) (*Contract, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: create %s (see docs/contract.md) or pass its path with -f", err, file)
	}
	if err != nil {
		return nil, err
	}
	c, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return c, nil
}

// Parse decodes and validates contract bytes. Unknown fields are rejected so
// that typos never silently shrink the declared scope.
func Parse(data []byte) (*Contract, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var c Contract
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("decoding contract: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("decoding contract: multiple YAML documents are not allowed")
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate checks the contract and returns all problems found, joined.
func (c *Contract) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if err := CheckAPIVersion(c.APIVersion); err != nil {
		errs = append(errs, err)
	}
	if c.Kind != Kind {
		add("kind: got %q, want %q", c.Kind, Kind)
	}
	if !nameRe.MatchString(c.Metadata.Name) {
		add("metadata.name: %q must be 1-64 chars of [a-z0-9._-], starting and ending alphanumeric", c.Metadata.Name)
	}
	if len(c.Runner.Command) == 0 || strings.TrimSpace(c.Runner.Command[0]) == "" {
		add("runner.command: must be a non-empty argv list")
	}
	if len(c.Scope.Source) == 0 {
		add("scope.source: at least one glob is required (scope is always declared)")
	}
	for field, globs := range map[string][]string{
		"scope.source":       c.Scope.Source,
		"scope.exclude":      c.Scope.Exclude,
		"scope.dependencies": c.Scope.Dependencies,
	} {
		for i, g := range globs {
			if err := ValidateGlob(g); err != nil {
				add("%s[%d]: %v", field, i, err)
			}
		}
	}
	c.Environment.validate(add)
	if c.Cacheability == "" {
		add("cacheability: required (hermetic, controlled-environment, external-dependencies, non-deterministic)")
	} else if !c.Cacheability.valid() {
		add("cacheability: unknown class %q", c.Cacheability)
	}
	return errors.Join(errs...)
}

func (e Environment) validate(add func(string, ...any)) {
	for k := range e.Toolchain {
		if !toolKeyRe.MatchString(k) {
			add("environment.toolchain: invalid key %q", k)
		}
	}
	for i, v := range e.Env {
		if !envRe.MatchString(v) {
			add("environment.env[%d]: invalid variable name %q", i, v)
		}
	}
}

// ValidateGlob rejects globs that could escape the contract directory or are
// malformed. `**` is only allowed as a whole path segment.
func ValidateGlob(g string) error {
	if g == "" {
		return errors.New("empty glob")
	}
	if strings.HasPrefix(g, "/") || strings.Contains(g, `\`) {
		return fmt.Errorf("%q: must be a relative, slash-separated path", g)
	}
	for _, seg := range strings.Split(g, "/") {
		switch {
		case seg == "" || seg == "." || seg == "..":
			return fmt.Errorf("%q: empty, '.' or '..' segments are not allowed", g)
		case seg != "**" && strings.Contains(seg, "**"):
			return fmt.Errorf("%q: '**' must be a whole path segment", g)
		}
		if _, err := path.Match(seg, ""); err != nil {
			return fmt.Errorf("%q: %w", g, err)
		}
	}
	return nil
}

// Match reports whether a slash-separated relative path matches glob.
// Each segment uses path.Match semantics; a `**` segment matches zero or
// more segments. The glob is assumed valid (see ValidateGlob). A name that
// is not a clean relative path (leading '/', empty, '.' or '..' segments)
// never matches, so no glob can reach outside the contract root.
func Match(glob, name string) bool {
	return SplitGlob(glob).Match(strings.Split(name, "/"))
}

// MatchContents is Match, except that a trailing `**` matches one or more
// segments: `dir/**` matches the files under dir, never a file named dir.
func MatchContents(glob, name string) bool {
	return SplitGlob(glob).MatchContents(strings.Split(name, "/"))
}

// Glob is a glob split into its path segments once, to match many names.
type Glob []string

// SplitGlob splits a valid glob (see ValidateGlob) into segments.
func SplitGlob(g string) Glob { return strings.Split(g, "/") }

// Match is the package-level Match over a name already split on "/".
func (g Glob) Match(name []string) bool {
	return clean(name) && matchSegs(g, name)
}

// MatchContents is the package-level MatchContents over a split name.
func (g Glob) MatchContents(name []string) bool {
	if !clean(name) {
		return false
	}
	if len(g) > 0 && g[len(g)-1] == "**" {
		// P/** matches A+B with |B| >= 1 exactly when P/** matches the
		// name without its last segment.
		return len(name) > 0 && matchSegs(g, name[:len(name)-1])
	}
	return matchSegs(g, name)
}

// MayContain reports whether a file under the directory dir (split, clean,
// relative to the root; nil for the root) could match g. The fingerprint
// walk skips directories for which no include glob returns true.
func (g Glob) MayContain(dir []string) bool {
	for i, s := range dir {
		if i < len(g) && g[i] == "**" {
			return true
		}
		// Without a `**` so far, g matches exactly len(g) segments, so a
		// file under dir needs len(g) > len(dir).
		if i >= len(g)-1 {
			return false
		}
		if !segMatch(g[i], s) {
			return false
		}
	}
	return true
}

func clean(name []string) bool {
	return !slices.ContainsFunc(name, func(s string) bool { return s == "" || s == "." || s == ".." })
}

// matchSegs matches segments with the classic wildcard algorithm: on a
// mismatch, only the most recent `**` takes one more segment. Earlier `**`
// never need to, so the cost is O(len(p)*len(n)) however many `**` the glob
// has (a recursive matcher is exponential in them).
func matchSegs(p, n []string) bool {
	pi, ni := 0, 0
	star, mark := -1, 0
	for ni < len(n) {
		switch {
		case pi < len(p) && p[pi] == "**":
			star, mark = pi, ni
			pi++
		case pi < len(p) && segMatch(p[pi], n[ni]):
			pi++
			ni++
		case star >= 0:
			mark++
			pi, ni = star+1, mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == "**" {
		pi++
	}
	return pi == len(p)
}

func segMatch(p, n string) bool {
	ok, _ := path.Match(p, n)
	return ok
}
