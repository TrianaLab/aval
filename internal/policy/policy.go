// Package policy loads the trust policy that decides which signer identities
// may produce reusable results.
package policy

import (
	"fmt"

	"github.com/TrianaLab/aval/pkg/evidence"
)

// Policy is the parsed trust policy file.
type Policy struct {
	// Identities accepted as signers, each with the trust level it grants.
	Identities []evidence.Identity `yaml:"identities"`
	// MinTrustLevel required to reuse a result. Developer signatures (level
	// 1) are personal acceleration; CI should require at least runner (2).
	MinTrustLevel evidence.TrustLevel `yaml:"minTrustLevel"`
	// AllowedCacheability extends reuse beyond hermetic and
	// controlled-environment. non-deterministic is never allowed here.
	AllowedCacheability []string `yaml:"allowedCacheability"`
}

// VerifyOptions converts the policy into evidence.VerifyOptions.
func (p *Policy) VerifyOptions(testID, fingerprint string) evidence.VerifyOptions {
	return evidence.VerifyOptions{
		ExpectedFingerprint: fingerprint,
		ExpectedTestID:      testID,
		Identities:          p.Identities,
		MinTrustLevel:       p.MinTrustLevel,
		AllowedCacheability: p.AllowedCacheability,
	}
}

// Load reads a policy file.
//
// TODO(milestone-2): define the file format (aval.dev/v1alpha1, kind
// Policy), validate regexps, reject non-deterministic in
// AllowedCacheability.
func Load(path string) (*Policy, error) {
	return nil, fmt.Errorf("policy.Load: %w", evidence.ErrNotImplemented)
}
