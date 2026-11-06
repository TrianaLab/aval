// Package policy loads the trust policy that decides which signer identities
// may produce reusable results.
package policy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"

	"github.com/TrianaLab/aval/internal/contract"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// Kind is the kind of a policy file. Its apiVersion is contract.APIVersion.
const Kind = "Policy"

// Policy is the parsed trust policy file.
type Policy struct {
	// Identities accepted as signers, each with the trust level it grants.
	Identities []evidence.Identity
	// MinTrustLevel required to reuse a result. Developer signatures (level
	// 1) are personal acceleration; CI should require at least runner (2).
	MinTrustLevel evidence.TrustLevel
	// AllowedCacheability extends reuse beyond hermetic and
	// controlled-environment. non-deterministic is never allowed here.
	AllowedCacheability []string
	// TrustedRoot is the path of the Sigstore trusted_root.json used for
	// keyless signatures. Empty means the Sigstore public-good instance.
	TrustedRoot string
}

// VerifyOptions converts the policy into evidence.VerifyOptions. The caller
// sets the signature verifier per attestation.
func (p *Policy) VerifyOptions(testID, fingerprint string) evidence.VerifyOptions {
	return evidence.VerifyOptions{
		ExpectedFingerprint: fingerprint,
		ExpectedTestID:      testID,
		Identities:          p.Identities,
		MinTrustLevel:       p.MinTrustLevel,
		AllowedCacheability: p.AllowedCacheability,
	}
}

// file is the YAML document. Trust levels are written by name.
type file struct {
	APIVersion          string     `yaml:"apiVersion"`
	Kind                string     `yaml:"kind"`
	TrustedRoot         string     `yaml:"trustedRoot"`
	MinTrustLevel       string     `yaml:"minTrustLevel"`
	Identities          []identity `yaml:"identities"`
	AllowedCacheability []string   `yaml:"allowedCacheability"`
}

type identity struct {
	Issuer        string `yaml:"issuer"`
	SubjectRegexp string `yaml:"subjectRegexp"`
	PublicKey     string `yaml:"publicKey"`
	TrustLevel    string `yaml:"trustLevel"`
}

var levels = map[string]evidence.TrustLevel{
	"developer": evidence.TrustDeveloper,
	"runner":    evidence.TrustRunner,
	"isolated":  evidence.TrustIsolated,
}

// Load reads a policy file. Relative trustedRoot and publicKey paths are
// resolved against the file's directory.
func Load(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p, err := Parse(data, filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// Parse decodes and validates a policy document, reading key files relative
// to dir. Unknown fields and anything it cannot validate are rejected: a
// policy that does not say exactly what it trusts is not loaded.
func Parse(data []byte, dir string) (*Policy, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f file
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("decoding policy: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("decoding policy: multiple YAML documents are not allowed")
	}

	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if err := contract.CheckAPIVersion(f.APIVersion); err != nil {
		errs = append(errs, err)
	}
	if f.Kind != Kind {
		add("kind: got %q, want %q", f.Kind, Kind)
	}
	p := &Policy{TrustedRoot: resolve(dir, f.TrustedRoot)}
	var ok bool
	if p.MinTrustLevel, ok = levels[f.MinTrustLevel]; !ok {
		add("minTrustLevel: %q must be developer, runner or isolated", f.MinTrustLevel)
	}
	if len(f.Identities) == 0 {
		add("identities: at least one is required")
	}
	for i, id := range f.Identities {
		v, err := id.validate(dir)
		if err != nil {
			add("identities[%d]: %w", i, err)
		}
		p.Identities = append(p.Identities, v)
	}
	for _, c := range f.AllowedCacheability {
		switch contract.Cacheability(c) {
		case contract.Hermetic, contract.ControlledEnvironment, contract.ExternalDependencies:
			p.AllowedCacheability = append(p.AllowedCacheability, c)
		case contract.NonDeterministic:
			add("allowedCacheability: non-deterministic results are never reusable")
		default:
			add("allowedCacheability: unknown class %q", c)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return p, nil
}

func (id identity) validate(dir string) (evidence.Identity, error) {
	lvl, ok := levels[id.TrustLevel]
	if !ok {
		return evidence.Identity{}, fmt.Errorf("trustLevel: %q must be developer, runner or isolated", id.TrustLevel)
	}
	if id.PublicKey != "" {
		if id.Issuer != "" || id.SubjectRegexp != "" {
			return evidence.Identity{}, errors.New("publicKey cannot be combined with issuer or subjectRegexp")
		}
		key, err := readKey(resolve(dir, id.PublicKey))
		if err != nil {
			return evidence.Identity{}, fmt.Errorf("publicKey: %w", err)
		}
		if _, err := evidence.PublicKeyID(string(key)); err != nil {
			return evidence.Identity{}, fmt.Errorf("publicKey %s: %w", id.PublicKey, err)
		}
		return evidence.Identity{PublicKey: string(key), TrustLevel: lvl}, nil
	}
	if id.Issuer == "" || id.SubjectRegexp == "" {
		return evidence.Identity{}, errors.New("issuer and subjectRegexp are required (or publicKey)")
	}
	// Compiled alone and anchored, as evidence.MatchIdentity does; either
	// failing would make the entry silently match nothing.
	if _, err := regexp.Compile(id.SubjectRegexp); err != nil {
		return evidence.Identity{}, fmt.Errorf("subjectRegexp: %w", err)
	}
	if _, err := regexp.Compile(`^(?:` + id.SubjectRegexp + `)$`); err != nil {
		return evidence.Identity{}, fmt.Errorf("subjectRegexp: %w", err)
	}
	return evidence.Identity{Issuer: id.Issuer, SubjectRegexp: id.SubjectRegexp, TrustLevel: lvl}, nil
}

// maxKeySize bounds a public key file, so a policy pointing at a device or a
// huge file fails instead of reading without end.
const maxKeySize = 64 << 10

func readKey(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	key, err := io.ReadAll(io.LimitReader(f, maxKeySize+1))
	if err == nil && len(key) > maxKeySize {
		err = fmt.Errorf("%s: larger than %d bytes", path, maxKeySize)
	}
	return key, err
}

func resolve(dir, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}
