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
	"os"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// APIVersion is the only contract version accepted by this build.
// Provisional: it may change before v1.
const APIVersion = "aval.dev/v1alpha1"

// Kind is the only kind accepted in v1alpha1.
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

	if c.APIVersion != APIVersion {
		add("apiVersion: got %q, want %q", c.APIVersion, APIVersion)
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
	for k := range c.Environment.Toolchain {
		if !toolKeyRe.MatchString(k) {
			add("environment.toolchain: invalid key %q", k)
		}
	}
	for i, e := range c.Environment.Env {
		if !envRe.MatchString(e) {
			add("environment.env[%d]: invalid variable name %q", i, e)
		}
	}
	if c.Cacheability == "" {
		add("cacheability: required (hermetic, controlled-environment, external-dependencies, non-deterministic)")
	} else if !c.Cacheability.valid() {
		add("cacheability: unknown class %q", c.Cacheability)
	}
	return errors.Join(errs...)
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
// more segments. The glob is assumed valid (see ValidateGlob).
func Match(glob, name string) bool {
	return matchSegs(strings.Split(glob, "/"), strings.Split(name, "/"))
}

func matchSegs(p, n []string) bool {
	for len(p) > 0 {
		if p[0] == "**" {
			p = p[1:]
			if len(p) == 0 {
				return true
			}
			for i := 0; i <= len(n); i++ {
				if matchSegs(p, n[i:]) {
					return true
				}
			}
			return false
		}
		if len(n) == 0 {
			return false
		}
		if ok, _ := path.Match(p[0], n[0]); !ok {
			return false
		}
		p, n = p[1:], n[1:]
	}
	return len(n) == 0
}
