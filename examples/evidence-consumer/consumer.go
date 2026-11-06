// Package consumer is an example third-party tool that consumes Aval
// evidence through github.com/TrianaLab/aval/pkg/evidence only, the way any
// external module would. tests/architecture fails if it imports anything
// else from Aval.
package consumer

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/TrianaLab/aval/pkg/evidence"
)

// Ed25519Verifier is this consumer's own evidence.SignatureVerifier: it
// checks a DSSE signature with one ed25519 key and reports the signer by key
// id. It applies no policy: evidence.Verify decides whether the key is
// trusted.
type Ed25519Verifier struct {
	key   ed25519.PublicKey
	keyID string
}

// NewEd25519Verifier builds a verifier from a PEM-encoded PKIX public key.
func NewEd25519Verifier(pemKey string) (*Ed25519Verifier, error) {
	id, err := evidence.PublicKeyID(pemKey)
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode([]byte(pemKey))
	if b == nil {
		return nil, errors.New("no PEM block")
	}
	pub, err := x509.ParsePKIXPublicKey(b.Bytes)
	if err != nil {
		return nil, err
	}
	k, ok := pub.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("not an ed25519 key")
	}
	return &Ed25519Verifier{key: k, keyID: id}, nil
}

// VerifySignature returns the key's signer if any signature verifies over
// the DSSE pre-authentication encoding of the payload.
func (v *Ed25519Verifier) VerifySignature(_ context.Context, env *evidence.Envelope) (evidence.Signer, error) {
	msg := PAE(env.PayloadType, env.Payload)
	for _, s := range env.Signatures {
		if ed25519.Verify(v.key, msg, s.Sig) {
			return evidence.Signer{KeyID: v.keyID}, nil
		}
	}
	return evidence.Signer{}, errors.New("no valid ed25519 signature")
}

// PAE is the DSSE v1 pre-authentication encoding, the bytes that are signed.
func PAE(payloadType string, payload []byte) []byte {
	return fmt.Appendf(nil, "DSSEv1 %d %s %d %s", len(payloadType), payloadType, len(payload), payload)
}

// docs/guides/go-consumer.md includes this section.
// --8<-- [start:check]

// Check returns the verified predicate of a DSSE envelope fetched by digest.
// nil envBytes means the lookup found nothing: evidence.ErrNotFound, a miss.
// Every other failure wraps evidence.ErrVerification, a security event. The
// caller runs the test unless Check returns a passed predicate.
func Check(ctx context.Context, envBytes []byte, opts evidence.VerifyOptions) (*evidence.Predicate, error) {
	if envBytes == nil {
		return nil, evidence.ErrNotFound
	}
	var env evidence.Envelope
	if err := json.Unmarshal(envBytes, &env); err != nil {
		return nil, &evidence.VerificationError{Reason: "decoding envelope: " + err.Error()}
	}
	v, err := evidence.Verify(ctx, &env, opts)
	if err != nil {
		return nil, err
	}
	return v.Predicate, nil
}

// --8<-- [end:check]

// Reuse reads an `aval verify --output json` report and says whether its
// passed result may be reused. It accepts only the report version it knows.
// A verification-failed outcome, or a rejected candidate that failed
// verification, returns an error wrapping evidence.ErrVerification so the
// caller alerts; this consumer then does not reuse at all (fail closed).
func Reuse(report []byte) (bool, error) {
	var r evidence.Report
	if err := json.Unmarshal(report, &r); err != nil {
		return false, fmt.Errorf("decoding report: %w", err)
	}
	if r.Version != evidence.ReportVersion {
		return false, fmt.Errorf("report version %q, want %q", r.Version, evidence.ReportVersion)
	}
	if r.Outcome == evidence.OutcomeVerificationFailed {
		return false, &evidence.VerificationError{Reason: r.Reason}
	}
	for _, rej := range r.Rejected {
		if rej.Outcome == evidence.OutcomeVerificationFailed {
			return false, &evidence.VerificationError{Reason: rej.Reference + ": " + rej.Reason}
		}
	}
	return r.Outcome == evidence.OutcomeVerified && r.Result == evidence.ResultPassed, nil
}
