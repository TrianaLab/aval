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
	"net/url"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/mod/module"
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

// Kind is a contract of one test. A file may also hold a set of tests
// (KindTestSet).
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
	// Never reusable, whatever the policy says.
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
	// Outputs are globs of files the test writes that a reused result
	// restores (a coverage or junit report). They are published with the
	// result and are never inputs. Omitted when empty, so contracts without
	// it hash as before.
	Outputs []string `yaml:"outputs,omitempty" json:"outputs,omitempty"`
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
// relative to the contract root (Root, by default the contract's
// directory); `**` matches any number of segments.
type Scope struct {
	// Source globs select files whose content is part of the fingerprint.
	Source []string `yaml:"source" json:"source"`
	// Exclude globs remove files selected by Source.
	Exclude []string `yaml:"exclude,omitempty" json:"exclude"`
	// Dependencies are lockfiles or manifests. Each glob must match at
	// least one file, otherwise fingerprinting fails (fail closed).
	Dependencies []string `yaml:"dependencies,omitempty" json:"dependencies"`
	// AllowMutable acknowledges mutable references (a container tag, a
	// manifest without a lockfile, a script fetched at run time) that the
	// test may reuse anyway. It is part of the canonical contract, so
	// adding or removing one changes the fingerprint. Omitted when empty,
	// so contracts without it hash as before.
	AllowMutable []AllowMutable `yaml:"allowMutable,omitempty" json:"allowMutable,omitempty"`
	// Resolve declares external artifacts by name (a tag, a version range,
	// a branch, a URL). At fingerprint time each is resolved to an
	// immutable id (digest, checksum or commit) that goes into the manifest
	// (internal/resolve). Omitted when empty, so contracts without it hash
	// as before.
	Resolve []Reference `yaml:"resolve,omitempty" json:"resolve,omitempty"`
	// Contracts are paths to other contracts, relative to this one's
	// directory (../backend/aval.yaml). The fingerprint of each goes into
	// this one's manifest. Omitted when empty, so contracts without it
	// hash as before.
	Contracts []string `yaml:"contracts,omitempty" json:"contracts,omitempty"`
	// Root is the path from the contract's directory up to the root of its
	// tree: only ".." segments (.., ../..). Globs, outputs, findings,
	// runner.command and environment.probe all resolve from it; Contracts
	// do not. Omitted when empty, so contracts without it hash as before.
	Root string `yaml:"root,omitempty" json:"root,omitempty"`
}

// ResolveKinds are the ecosystems scope.resolve accepts, one resolver each.
var ResolveKinds = []string{"image", "npm", "pypi", "go", "maven", "crate", "gem", "git", "url"}

// Reference is one scope.resolve entry, written `kind: reference`.
type Reference struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// MarshalYAML writes the one-key form `npm: react@^18`.
func (r Reference) MarshalYAML() (any, error) { return map[string]string{r.Kind: r.Ref}, nil }

// UnmarshalYAML reads the one-key form `npm: react@^18`.
func (r *Reference) UnmarshalYAML(n *yaml.Node) error {
	var m map[string]string
	if err := n.Decode(&m); err != nil {
		return err
	}
	if len(m) != 1 {
		return fmt.Errorf("line %d: a scope.resolve entry is one `kind: reference` pair", n.Line)
	}
	for k, v := range m {
		r.Kind, r.Ref = k, v
	}
	return nil
}

// AllowMutable accepts one mutable reference, by the exact text Aval reports
// for it, with the reason it is acceptable. File, when set, limits it to
// the finding in that file (as findings print it: relative to the contract
// root, or runner.command); without it the ref must be reported in one file
// only. Files, globs relative to the contract root that exclude File, accepts
// the ref in every in-scope file they match (never in runner.command); each
// must match a file in scope. File and Files are omitted when empty, so
// contracts without them hash as before.
type AllowMutable struct {
	File   string   `yaml:"file,omitempty" json:"file,omitempty"`
	Files  []string `yaml:"files,omitempty" json:"files,omitempty"`
	Ref    string   `yaml:"ref" json:"ref"`
	Reason string   `yaml:"reason" json:"reason"`
}

// Environment declares execution conditions that are part of the fingerprint.
type Environment struct {
	// Toolchain is a declared map such as {go: "1.25"}. It is trusted as
	// declared unless Probe checks it.
	Toolchain map[string]string `yaml:"toolchain,omitempty" json:"toolchain"`
	// Probe maps a Toolchain key to an argv that prints the installed
	// version, such as {go: [go, version]}. Only `aval run` executes it.
	// Omitted when empty, so contracts without it hash as before.
	Probe map[string][]string `yaml:"probe,omitempty" json:"probe,omitempty"`
	// Env lists environment variable names whose values are hashed.
	Env []string `yaml:"env,omitempty" json:"env"`
}

var (
	nameRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$`)
	envRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	toolKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	rootRe    = regexp.MustCompile(`^\.\.(/\.\.)*$`)
)

// Load reads and validates a contract file and returns its test named test.
// An empty test selects the only test of the file; a file of more tests
// then fails and lists their names (there is no implicit run-all). On a
// kind: Test a non-empty test must equal metadata.name.
func Load(file, test string) (*Contract, error) {
	cs, err := LoadAll(file)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(cs))
	for i, c := range cs {
		if test != "" && c.Metadata.Name == test {
			return c, nil
		}
		names[i] = c.Metadata.Name
	}
	switch {
	case test != "":
		return nil, fmt.Errorf("%s: no test named %q; its tests: %s", file, test, strings.Join(names, ", "))
	case len(cs) > 1:
		return nil, fmt.Errorf("%s: the TestSet holds %d tests (%s); select one with --test", file, len(cs), strings.Join(names, ", "))
	}
	return cs[0], nil
}

// LoadAll reads and validates a contract file and returns its tests: one
// for a kind: Test, each expanded test for a kind: TestSet (ParseAll).
func LoadAll(file string) ([]*Contract, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: create %s (see docs/contract.md) or pass its path with -f", err, file)
	}
	if err != nil {
		return nil, err
	}
	cs, err := ParseAll(data)
	if err != nil {
		return nil, &FileError{File: file, Err: err}
	}
	return cs, nil
}

// FileError is a contract file that does not decode or validate. Err holds
// the problems, joined: one per errors.Join member.
type FileError struct {
	File string
	Err  error
}

func (e *FileError) Error() string { return e.File + ": " + e.Err.Error() }
func (e *FileError) Unwrap() error { return e.Err }

// Parse decodes and validates the bytes of one kind: Test. Unknown fields
// are rejected so that typos never silently shrink the declared scope. A
// kind: TestSet is ErrTestSet: use ParseAll.
func Parse(data []byte) (*Contract, error) {
	if peekKind(data) == KindTestSet {
		return nil, ErrTestSet
	}
	var c Contract
	if err := decodeStrict(data, &c); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// peekKind returns the kind of a YAML document, or "" if it has none or
// does not decode; decodeStrict then reports why.
func peekKind(data []byte) string {
	var k struct {
		Kind string `yaml:"kind"`
	}
	_ = yaml.Unmarshal(data, &k)
	return k.Kind
}

// decodeStrict decodes the one YAML document in data into v, rejecting
// unknown fields and further documents.
func decodeStrict(data []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		var te *yaml.TypeError
		if errors.As(err, &te) {
			return typeErrors(data, te)
		}
		return fmt.Errorf("decoding contract: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("decoding contract: multiple YAML documents are not allowed")
	}
	return nil
}

// Validate checks the contract and returns all problems found, joined.
func (c *Contract) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if err := CheckAPIVersion(c.APIVersion); err != nil {
		errs = append(errs, err)
	}
	if c.Kind != Kind {
		add("kind: got %q, want %q or %q", c.Kind, Kind, KindTestSet)
	}
	if err := ValidateName(c.Metadata.Name); err != nil {
		errs = append(errs, err)
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
		"outputs":            c.Outputs,
	} {
		for i, g := range globs {
			if err := ValidateGlob(g); err != nil {
				add("%s[%d]: %v", field, i, err)
			}
		}
	}
	c.Scope.validate(add)
	c.Environment.validate(add)
	if c.Cacheability == "" {
		add("cacheability: required (hermetic, controlled-environment, external-dependencies, non-deterministic)")
	} else if !c.Cacheability.valid() {
		add("cacheability: unknown class %q", c.Cacheability)
	}
	return errors.Join(errs...)
}

// validate checks scope.root, scope.allowMutable, scope.resolve and
// scope.contracts; Validate checks the globs.
func (s Scope) validate(add func(string, ...any)) {
	if s.Root != "" && !rootRe.MatchString(s.Root) {
		add("scope.root: %q must be '..' segments only (.., ../..): the path up from the contract's directory to the root of its tree", s.Root)
	}
	for i, a := range s.AllowMutable {
		a.validate(i, add)
	}
	for i, r := range s.Resolve {
		if err := r.validate(); err != nil {
			add("scope.resolve[%d]: %v", i, err)
		}
	}
	for i, p := range s.Contracts {
		if err := validateContractPath(p); err != nil {
			add("scope.contracts[%d]: %v", i, err)
		} else if slices.Index(s.Contracts, p) < i {
			add("scope.contracts[%d]: %q is listed twice", i, p)
		}
	}
}

// validate checks scope.allowMutable[i].
func (a AllowMutable) validate(i int, add func(string, ...any)) {
	if strings.TrimSpace(a.Ref) == "" || strings.TrimSpace(a.Reason) == "" {
		add("scope.allowMutable[%d]: ref and reason are both required", i)
	}
	if a.File != "" && !fs.ValidPath(a.File) {
		add("scope.allowMutable[%d]: file %q must be a slash-separated path relative to the contract root, as findings print it", i, a.File)
	}
	if a.File != "" && len(a.Files) > 0 {
		add("scope.allowMutable[%d]: file and files exclude each other: file is one exact path, files are globs", i)
	}
	for j, g := range a.Files {
		if err := ValidateGlob(g); err != nil {
			add("scope.allowMutable[%d].files[%d]: %v", i, j, err)
		}
	}
}

// validateContractPath accepts a clean, relative, slash-separated path to a
// .yaml or .yml file, without spaces or control characters (it goes into a
// manifest line). Leading ".." segments are allowed: where the path may
// lead is checked when the fingerprint is computed.
func validateContractPath(p string) error {
	switch {
	case strings.IndexFunc(p, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) || c == '\\' }) >= 0:
		return fmt.Errorf("%q: must be slash-separated, without spaces or control characters", p)
	case path.IsAbs(p) || path.Clean(p) != p:
		return fmt.Errorf("%q: must be a clean relative path (no leading '/', no '.' or empty segments, '..' only at the start)", p)
	case !strings.HasSuffix(p, ".yaml") && !strings.HasSuffix(p, ".yml"):
		return fmt.Errorf("%q: must name a contract file (.yaml or .yml)", p)
	}
	return nil
}

func (r Reference) validate() error {
	switch {
	case !slices.Contains(ResolveKinds, r.Kind):
		return fmt.Errorf("unknown kind %q (want one of %s)", r.Kind, strings.Join(ResolveKinds, ", "))
	case r.Ref == "" || strings.IndexFunc(r.Ref, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) }) >= 0:
		return fmt.Errorf("%s: the reference must be non-empty, without spaces or control characters", r.Kind)
	}
	// A token in the user name (https://tok@host) is a credential too; an
	// ssh user name (ssh://git@host) is not; a scheme-less //tok@host may
	// be https to the tool, so it is.
	// A URL that does not parse still reaches a tool that may read it: any
	// @ in its authority counts as credentials.
	u, err := url.Parse(r.Ref)
	_, rest, scheme := strings.Cut(r.Ref, "://")
	authority, _, _ := strings.Cut(rest, "/")
	creds := err != nil && scheme && strings.Contains(authority, "@")
	if err == nil && u.User != nil {
		_, hasPass := u.User.Password()
		creds = hasPass || u.Scheme == "http" || u.Scheme == "https" || u.Scheme == ""
	}
	if creds || credentialQuery(r.Ref) {
		// The contract is committed: never echo the reference.
		return fmt.Errorf("%s: credentials in the reference are not allowed: put them in ~/.netrc", r.Kind)
	}
	_, _, err = r.Package()
	return err
}

// Package splits a package reference (npm, pypi, go, crate, gem, maven) into
// its name and version spec ("" when there is none; for maven the name is
// group:artifact) and checks both. The name must match its ecosystem's
// naming rules, so nothing a tool reads as a path, a file or a URL passes
// for a registry name; the spec must not name another source. image, git
// and url are not packages: all empty, no error.
func (r Reference) Package() (name, spec string, err error) {
	ref, valid := r.Ref, nameRules[r.Kind]
	switch r.Kind {
	case "npm", "go", "crate", "gem":
		// The last @ after the first byte, so an npm scope stays in the name.
		name = ref
		if i := strings.LastIndex(ref, "@"); i > 0 {
			name, spec = ref[:i], ref[i+1:]
		}
		if r.Kind != "go" && registrySpecRe.MatchString(spec) {
			return "", "", fmt.Errorf("%s: spec %q is not a registry version, range or tag: use the git or url kind, or pin", r.Kind, spec)
		}
	case "pypi":
		i := strings.IndexAny(ref, "=<>!~")
		if i < 0 {
			i = len(ref)
		}
		name, spec = ref[:i], ref[i:]
		if m := pypiReqRe.FindStringSubmatch(name); m != nil {
			name = m[1] // extras do not change the files
		}
		if !pypiSpecRe.MatchString(spec) {
			return "", "", fmt.Errorf("pypi: spec %q is not a registry version, range or tag: use the url kind, or pin", spec)
		}
	case "maven":
		p := strings.Split(ref, ":")
		if len(p) != 3 || p[0] == "" || p[1] == "" || p[2] == "" {
			return "", "", errors.New("maven: want group:artifact:version")
		}
		name, spec = p[0]+":"+p[1], p[2]
	default:
		return "", "", nil
	}
	if !valid(name) {
		return "", "", fmt.Errorf("not a valid %s package name: %q", r.Kind, name)
	}
	return name, spec, nil
}

// nameRules checks a package name, per ecosystem.
var nameRules = map[string]func(string) bool{
	"npm":   func(n string) bool { return len(n) <= 214 && npmNameRe.MatchString(n) && !npmFileRe.MatchString(n) },
	"go":    func(n string) bool { return module.CheckPath(n) == nil },
	"pypi":  func(n string) bool { return pypiNameRe.MatchString(n) && !pyFileRe.MatchString(n) },
	"crate": crateNameRe.MatchString,
	"gem":   func(n string) bool { return gemNameRe.MatchString(n) && !strings.HasSuffix(strings.ToLower(n), ".gem") },
	"maven": func(n string) bool {
		g, a, _ := strings.Cut(n, ":")
		return mavenIDRe.MatchString(g) && mavenIDRe.MatchString(a) && !strings.Contains(n, "..")
	},
}

// Package names, per ecosystem: npm's rules for old packages
// (validate-npm-package-name validForOldPackages, so JSONStream passes),
// PEP 508 with extras, crates.io, RubyGems and Maven coordinates. npm and
// pip read a name ending in an archive extension (pip's ARCHIVE_EXTENSIONS)
// as a local file, gem a name ending in .gem.
const pypiName = `[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?`

var (
	npmNameRe   = regexp.MustCompile(`^(@[a-z0-9-~][a-z0-9-._~]*/)?[A-Za-z0-9-~][A-Za-z0-9-._~]*$`)
	npmFileRe   = regexp.MustCompile(`(?i)\.(tgz|tar\.gz|tar)$`)
	pypiNameRe  = regexp.MustCompile(`^` + pypiName + `$`)
	pypiReqRe   = regexp.MustCompile(`^(` + pypiName + `)(?:\[` + pypiName + `(?:,` + pypiName + `)*\])?$`)
	pyFileRe    = regexp.MustCompile(`(?i)\.(whl|tar\.gz|tgz|zip|tar|egg|tar\.bz2|tbz|tar\.xz|txz|tlz|tar\.lz|tar\.lzma)$`)
	crateNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	gemNameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	mavenIDRe   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	// registrySpecRe is an npm, Cargo or gem spec the tool installs from
	// somewhere else than the registry entry (npm:alias, github:, org/repo,
	// git+https:, file:, ./dir, ~/dir, a tarball, a Windows path).
	registrySpecRe = regexp.MustCompile(`(?i)[:/#\\]|^\.|^~/|\.(tgz|tar\.gz|tar)$`)
	// pypiSpecRe is a version specifier: no path, URL or marker.
	pypiSpecRe = regexp.MustCompile(`^[=<>!~0-9A-Za-z.*+,_-]*$`)
)

// credentialQuery reports whether ref's query string (read as text, so a
// URL that does not parse is covered) has a parameter named like a
// credential: a presigned URL, a GitLab private_token. Keys compare in
// any case, without a trailing [] and with - read as _.
func credentialQuery(ref string) bool {
	_, q, _ := strings.Cut(ref, "?")
	q, _, _ = strings.Cut(q, "#")
	for _, kv := range strings.FieldsFunc(q, func(c rune) bool { return c == '&' || c == ';' }) {
		k, _, _ := strings.Cut(kv, "=")
		if u, err := url.QueryUnescape(k); err == nil {
			k = u
		}
		k = strings.ReplaceAll(strings.TrimSuffix(strings.ToLower(k), "[]"), "-", "_")
		if slices.Contains(credentialKeys, k) {
			return true
		}
	}
	return false
}

var credentialKeys = []string{"token", "private_token", "access_token", "security_token", "x_amz_security_token", "key", "apikey", "api_key",
	"sig", "signature", "x_amz_signature", "x_goog_signature", "auth", "password", "secret", "client_secret", "credential", "session", "sessionid"}

// RunStackEnv names the variable `aval run` sets for runner.command: the
// contracts of the enclosing runs. It differs between an outer and a nested
// run of the same inputs, so it may not appear in environment.env.
const RunStackEnv = "AVAL_RUN_STACK"

func (e Environment) validate(add func(string, ...any)) {
	for k := range e.Toolchain {
		if !toolKeyRe.MatchString(k) {
			add("environment.toolchain: invalid key %q", k)
		}
	}
	for k, argv := range e.Probe {
		if v, ok := e.Toolchain[k]; !ok {
			add("environment.probe.%s: not a key of environment.toolchain", k)
		} else if strings.TrimSpace(v) == "" {
			add("environment.probe.%s: environment.toolchain.%s must not be empty", k, k)
		}
		if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
			add("environment.probe.%s: must be a non-empty argv list", k)
		}
	}
	for i, v := range e.Env {
		if !envRe.MatchString(v) {
			add("environment.env[%d]: invalid variable name %q", i, v)
		} else if strings.EqualFold(v, RunStackEnv) {
			add("environment.env[%d]: %s is set by aval run and is not an input", i, v)
		}
	}
}

// ValidateName rejects a metadata.name that is not 1-64 chars of
// [a-z0-9._-], starting and ending alphanumeric.
func ValidateName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("metadata.name: %q must be 1-64 chars of [a-z0-9._-], starting and ending alphanumeric", name)
	}
	return nil
}

// ValidateGlob rejects globs that could escape the contract root or are
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
