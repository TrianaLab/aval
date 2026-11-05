// Package attest builds in-toto statements for an execution and signs them
// as DSSE envelopes with Cosign/Sigstore.
package attest

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/TrianaLab/aval/pkg/evidence"
)

// NewStatement wraps a predicate in an in-toto v1 Statement whose subject is
// the tested revision or artifact.
func NewStatement(p *evidence.Predicate) (*evidence.Statement, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return &evidence.Statement{
		Type:          evidence.StatementType,
		Subject:       []evidence.ResourceDigest{{Name: p.Subject.Name, Digest: p.Subject.Digest}},
		PredicateType: evidence.PredicateType,
		Predicate:     raw,
	}, nil
}

// SignOptions selects the signing identity.
type SignOptions struct {
	// Key is a path or KMS URI for key-based signing. Empty means keyless
	// (OIDC + Fulcio), which is what CI runners should use.
	Key string
}

// Sign produces a DSSE envelope for the statement.
//
// TODO(milestone-2): sign with sigstore-go (keyless and key-based) and
// record the entry in Rekor.
func Sign(ctx context.Context, s *evidence.Statement, opts SignOptions) (*evidence.Envelope, error) {
	return nil, fmt.Errorf("attest.Sign: %w", evidence.ErrNotImplemented)
}
