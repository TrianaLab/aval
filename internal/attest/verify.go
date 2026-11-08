package attest

import (
	"bytes"
	"context"
	"crypto"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
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
	keyed  *verify.Verifier // nil when the policy accepts no keys
	keyIDs map[string]bool  // the key ids keyed accepts
	// The keyless verifier needs the trusted root, which may be fetched with
	// TUF (network, writes under $HOME), so it is built on first use: only
	// when a candidate's bundle carries a certificate.
	wantKeyless bool
	trustedRoot string
	keylessOnce sync.Once
	keyless     *verify.Verifier
	keylessErr  error
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
	keyedOptions   = []verify.VerifierOption{verify.WithNoObserverTimestamps()}
)

// NewVerifier builds a Verifier for the accepted identities. Key identities
// carry their public key. Keyless identities need a trusted root: the
// trusted_root.json file at trustedRoot, or the Sigstore public-good root
// (fetched with TUF) when it is empty. The trusted root is loaded by For,
// the first time a bundle carries a certificate.
func NewVerifier(trustedRoot string, ids []evidence.Identity) (*Verifier, error) {
	v := &Verifier{trustedRoot: trustedRoot, keyIDs: map[string]bool{}}
	keys := map[string]*root.ExpiringKey{}
	for _, id := range ids {
		if id.PublicKey == "" {
			v.wantKeyless = v.wantKeyless || id.Issuer != ""
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
		v.keyIDs[kid] = true
	}
	var err error
	if len(keys) > 0 {
		if v.keyed, err = verify.NewVerifier(root.NewTrustedPublicKeyMaterialFromMapping(keys), keyedOptions...); err != nil {
			return nil, fmt.Errorf("key verifier: %w", err)
		}
	}
	return v, nil
}

// loadKeyless builds the keyless verifier once; its error is kept.
func (v *Verifier) loadKeyless() error {
	v.keylessOnce.Do(func() {
		tm, err := loadTrustedRoot(v.trustedRoot)
		if err != nil {
			v.keylessErr = fmt.Errorf("sigstore trusted root: %w", err)
			return
		}
		if v.keyless, err = verify.NewVerifier(tm, keylessOptions...); err != nil {
			v.keylessErr = fmt.Errorf("keyless verifier: %w", err)
		}
	})
	return v.keylessErr
}

// For returns the evidence.SignatureVerifier for one attestation, given the
// Sigstore bundle JSON stored with it. A missing bundle never verifies. When
// the policy accepts keyless identities and the bundle is not a key-based
// one, For first loads the trusted root; failing to load it says nothing
// about the attestation, so callers must not report it as a verification
// failure.
func (v *Verifier) For(bundleJSON []byte) (evidence.SignatureVerifier, error) {
	if v.wantKeyless && !keyBased(bundleJSON) {
		if err := v.loadKeyless(); err != nil {
			return nil, err
		}
	}
	return bundleSignature{v: v, raw: bundleJSON}, nil
}

// keyBased reports whether verifying the bundle can never need the trusted
// root: it does not decode or carries no usable verification material (so it
// never verifies), or it names a public key. It asks sigstore, which
// VerifySignature uses too, instead of parsing the JSON by hand.
func keyBased(raw []byte) bool {
	var b bundle.Bundle
	if b.UnmarshalJSON(raw) != nil {
		return true
	}
	vc, err := b.VerificationContent()
	return err != nil || vc.PublicKey() != nil
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
	// The hint is only a claim: a listed key still has to verify below.
	if !v.keyIDs[hint] {
		return evidence.Signer{}, fmt.Errorf("signer key id %q is not in the trust policy", hint)
	}
	if _, err := v.keyed.Verify(e, verify.NewPolicy(verify.WithoutArtifactUnsafe(), verify.WithKey())); err != nil {
		return evidence.Signer{}, err
	}
	// The hint selected the trusted key that verified the signature.
	return evidence.Signer{KeyID: hint}, nil
}

func (v *Verifier) verifyKeyless(e verify.SignedEntity) (evidence.Signer, error) {
	if !v.wantKeyless {
		// The certificate is unverified here; it only names the claimed signer.
		who := "certificate signer"
		if vc, err := e.VerificationContent(); err == nil && vc.Certificate() != nil {
			if s, err := certificate.SummarizeCertificate(vc.Certificate()); err == nil {
				who = fmt.Sprintf("certificate claiming signer %q (issuer %q)", s.SubjectAlternativeName, s.Issuer)
			}
		}
		return evidence.Signer{}, fmt.Errorf("%s is not accepted by the policy: it lists no keyless identities", who)
	}
	if err := v.loadKeyless(); err != nil {
		return evidence.Signer{}, err
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

// VerifyBlob checks that bundleJSON is a keyless Sigstore signature over
// artifact by exactly issuer and subject (no regexp), against the trusted
// root at trustedRoot (the public-good root fetched with TUF when empty). It
// requires the same SCT, Rekor entry and timestamp as attestations do.
func VerifyBlob(trustedRoot string, bundleJSON, artifact []byte, issuer, subject string) error {
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return fmt.Errorf("decoding Sigstore bundle: %w", err)
	}
	return verifyBlob(trustedRoot, &b, artifact, issuer, subject)
}

func verifyBlob(trustedRoot string, e verify.SignedEntity, artifact []byte, issuer, subject string) error {
	// An empty matcher accepts any value, so an empty identity fails closed.
	if issuer == "" || subject == "" {
		return errors.New("signer issuer and subject are required")
	}
	tm, err := loadTrustedRoot(trustedRoot)
	if err != nil {
		return fmt.Errorf("sigstore trusted root: %w", err)
	}
	v, err := verify.NewVerifier(tm, keylessOptions...)
	if err != nil {
		return fmt.Errorf("keyless verifier: %w", err)
	}
	id, _ := verify.NewShortCertificateIdentity(issuer, "", subject, "") // cannot fail: exact values, no regexp
	_, err = v.Verify(e, verify.NewPolicy(verify.WithArtifact(bytes.NewReader(artifact)), verify.WithCertificateIdentity(id)))
	return err
}
