package attest

import (
	"bytes"
	"context"
	"crypto"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"github.com/sigstore/sigstore/pkg/signature"

	"github.com/TrianaLab/aval/pkg/evidence"
)

// Verifier checks the Sigstore bundle stored next to an envelope: keyless
// bundles against a Sigstore trusted root (Fulcio CA, CT logs, Rekor),
// key-based ones against the public keys the policy accepts. It returns the
// signer; evidence.Verify applies the rest of the policy.
type Verifier struct {
	keyless, keyed *verify.Verifier // nil when the policy accepts no such signer
}

// Seams for tests: where the trusted root comes from (a trusted_root.json
// file, or the Sigstore public-good root fetched with TUF when the path is
// empty), and what a keyless signature must carry: an embedded SCT from a
// trusted CT log, a Rekor entry, and a trusted timestamp (Rekor or a TSA)
// inside the certificate's validity.
var (
	fetchPublicGood = root.FetchTrustedRoot
	loadTrustedRoot = func(path string) (root.TrustedMaterial, error) {
		if path == "" {
			return fetchPublicGood()
		}
		return root.NewTrustedRootFromPath(path)
	}
	keylessOptions = []verify.VerifierOption{verify.WithSignedCertificateTimestamps(1), verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1)}
)

// NewVerifier builds a Verifier for the accepted identities. Key identities
// carry their public key. Keyless identities need a trusted root: the
// trusted_root.json file at trustedRoot, or the Sigstore public-good root
// (fetched with TUF) when it is empty.
func NewVerifier(trustedRoot string, ids []evidence.Identity) (*Verifier, error) {
	v := &Verifier{}
	keys := map[string]*root.ExpiringKey{}
	keyless := false
	for _, id := range ids {
		if id.PublicKey == "" {
			keyless = keyless || id.Issuer != ""
			continue
		}
		kid, err := evidence.PublicKeyID(id.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("public key: %w", err)
		}
		pub, _ := cryptoutils.UnmarshalPEMToPublicKey([]byte(id.PublicKey)) // cannot fail: PublicKeyID parsed it
		sv, err := signature.LoadVerifier(pub, crypto.SHA256)
		if err != nil {
			return nil, fmt.Errorf("public key: %w", err)
		}
		keys[kid] = root.NewExpiringKey(sv, time.Time{}, time.Time{})
	}
	// ponytail: fixed, valid option sets; NewVerifier only fails on invalid ones.
	if len(keys) > 0 {
		v.keyed, _ = verify.NewVerifier(root.NewTrustedPublicKeyMaterialFromMapping(keys), verify.WithNoObserverTimestamps())
	}
	if keyless {
		tm, err := loadTrustedRoot(trustedRoot)
		if err != nil {
			return nil, fmt.Errorf("sigstore trusted root: %w", err)
		}
		v.keyless, _ = verify.NewVerifier(tm, keylessOptions...)
	}
	return v, nil
}

// For returns the evidence.SignatureVerifier for one attestation, given the
// Sigstore bundle JSON stored with it. A missing bundle never verifies.
func (v *Verifier) For(bundleJSON []byte) evidence.SignatureVerifier {
	return bundleSignature{v: v, raw: bundleJSON}
}

type bundleSignature struct {
	v   *Verifier
	raw []byte
}

func (b bundleSignature) VerifySignature(_ context.Context, env *evidence.Envelope) (evidence.Signer, error) {
	if len(b.raw) == 0 {
		return evidence.Signer{}, errors.New("no Sigstore bundle stored with the attestation")
	}
	var bun bundle.Bundle
	if err := bun.UnmarshalJSON(b.raw); err != nil {
		return evidence.Signer{}, fmt.Errorf("decoding Sigstore bundle: %w", err)
	}
	return b.v.verifyEntity(&bun, env)
}

// verifyEntity verifies a signed entity and checks that what it signs is
// exactly env, the envelope evidence.Verify goes on to decode.
func (v *Verifier) verifyEntity(e verify.SignedEntity, env *evidence.Envelope) (evidence.Signer, error) {
	if err := signsEnvelope(e, env); err != nil {
		return evidence.Signer{}, err
	}
	vc, err := e.VerificationContent()
	if err != nil {
		return evidence.Signer{}, err
	}
	if pk := vc.PublicKey(); pk != nil {
		return v.verifyKeyed(e, pk.Hint())
	}
	return v.verifyKeyless(e)
}

func signsEnvelope(e verify.SignedEntity, env *evidence.Envelope) error {
	sc, err := e.SignatureContent()
	if err != nil {
		return err
	}
	ec := sc.EnvelopeContent()
	if ec == nil {
		return errors.New("bundle does not carry a DSSE envelope")
	}
	raw := ec.RawEnvelope()
	payload, err := base64.StdEncoding.DecodeString(raw.Payload)
	if err != nil || raw.PayloadType != env.PayloadType || !bytes.Equal(payload, env.Payload) {
		return errors.New("bundle signs a different envelope")
	}
	return nil
}

func (v *Verifier) verifyKeyed(e verify.SignedEntity, hint string) (evidence.Signer, error) {
	if v.keyed == nil {
		return evidence.Signer{}, errors.New("key-based signature, but the policy accepts no keys")
	}
	if _, err := v.keyed.Verify(e, verify.NewPolicy(verify.WithoutArtifactUnsafe(), verify.WithKey())); err != nil {
		return evidence.Signer{}, err
	}
	// The hint selected the trusted key that verified the signature.
	return evidence.Signer{KeyID: hint}, nil
}

func (v *Verifier) verifyKeyless(e verify.SignedEntity) (evidence.Signer, error) {
	if v.keyless == nil {
		return evidence.Signer{}, errors.New("certificate signature, but the policy accepts no keyless identities")
	}
	// Identities are matched by evidence.Verify against the policy, using
	// the certificate summary returned here.
	res, err := v.keyless.Verify(e, verify.NewPolicy(verify.WithoutArtifactUnsafe(), verify.WithoutIdentitiesUnsafe()))
	if err != nil {
		return evidence.Signer{}, err
	}
	if s := res.Signature; s != nil && s.Certificate != nil && s.Certificate.Issuer != "" && s.Certificate.SubjectAlternativeName != "" {
		return evidence.Signer{Issuer: s.Certificate.Issuer, Subject: s.Certificate.SubjectAlternativeName}, nil
	}
	return evidence.Signer{}, errors.New("certificate has no OIDC issuer or subject")
}
