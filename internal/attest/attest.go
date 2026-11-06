// Package attest builds in-toto statements for an execution and signs them
// as DSSE envelopes with Sigstore (sigstore-go).
package attest

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/sign"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/TrianaLab/aval/pkg/evidence"
)

// Keyless signing uses the Sigstore public-good instance.
// TODO(milestone-5): let callers sign against a private Fulcio and Rekor.
const (
	DefaultFulcioURL = "https://fulcio.sigstore.dev"
	DefaultRekorURL  = "https://rekor.sigstore.dev"
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
	// Key is the path to a PEM-encoded ECDSA P-256 private key (PKCS#8 or
	// SEC1, unencrypted) for key-based signing, typically on a developer
	// machine. Empty means keyless: an ephemeral key certified by Fulcio for
	// the identity in IDToken, which is what CI runners should use.
	Key string
	// IDToken is the OIDC token presented to Fulcio. Required for keyless.
	// Obtaining it is vendor specific (CI provider, interactive flow) and is
	// the caller's job.
	IDToken string
}

// Signed is the result of Sign.
type Signed struct {
	// Envelope is the DSSE envelope over the statement.
	Envelope *evidence.Envelope
	// Bundle is the Sigstore bundle (v0.3 JSON): the same envelope plus the
	// verification material (certificate or key hint) and any transparency
	// log entry. It is what a verifier needs besides the envelope.
	Bundle []byte
}

// Seams for tests; production talks to real Fulcio and Rekor over HTTP.
var (
	newEphemeralKeypair = func() (sign.Keypair, error) { return sign.NewEphemeralKeypair(nil) }
	newFulcio           = func() sign.CertificateProvider {
		return sign.NewFulcio(&sign.FulcioOptions{BaseURL: DefaultFulcioURL, Retries: 1})
	}
	newRekor = func() sign.Transparency {
		return sign.NewRekor(&sign.RekorOptions{BaseURL: DefaultRekorURL, Retries: 1})
	}
)

// Sign produces a DSSE envelope for the statement, keyless or key-based
// depending on opts.
func Sign(ctx context.Context, s *evidence.Statement, opts SignOptions) (*Signed, error) {
	if s == nil || s.Type != evidence.StatementType || s.PredicateType != evidence.PredicateType {
		return nil, errors.New("attest.Sign: not an Aval in-toto statement")
	}
	payload, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("attest.Sign: encoding statement: %w", err)
	}
	bo := sign.BundleOptions{Context: ctx}
	var keypair sign.Keypair
	if opts.Key != "" {
		if keypair, err = loadKey(opts.Key); err != nil {
			return nil, fmt.Errorf("attest.Sign: %w", err)
		}
	} else {
		if opts.IDToken == "" {
			return nil, errors.New("attest.Sign: keyless signing needs an OIDC token")
		}
		if keypair, err = newEphemeralKeypair(); err != nil {
			return nil, fmt.Errorf("attest.Sign: %w", err)
		}
		// The Rekor entry is always recorded: the short-lived certificate
		// cannot be verified later without its timestamp.
		bo.CertificateProvider = newFulcio()
		bo.CertificateProviderOptions = &sign.CertificateProviderOptions{IDToken: opts.IDToken}
		bo.TransparencyLogs = []sign.Transparency{newRekor()}
	}
	b, err := sign.Bundle(&sign.DSSEData{Data: payload, PayloadType: evidence.PayloadType}, keypair, bo)
	if err != nil {
		return nil, fmt.Errorf("attest.Sign: %w", err)
	}
	if opts.Key == "" {
		if err := checkCert(b.GetVerificationMaterial().GetCertificate().GetRawBytes()); err != nil {
			return nil, fmt.Errorf("attest.Sign: signing certificate: %w", err)
		}
	}
	raw, _ := protojson.Marshal(b) // ponytail: a bundle sigstore-go just built, cannot fail
	d := b.GetDsseEnvelope()
	env := &evidence.Envelope{PayloadType: d.GetPayloadType(), Payload: d.GetPayload()}
	for _, sig := range d.GetSignatures() {
		env.Signatures = append(env.Signatures, evidence.Signature{KeyID: sig.GetKeyid(), Sig: sig.GetSig()})
	}
	return &Signed{Envelope: env, Bundle: raw}, nil
}

// checkCert fails closed on a Fulcio certificate the verifier would reject:
// without an OIDC issuer and subject no policy can match it, so publishing
// the attestation would only produce verification failures downstream.
func checkCert(der []byte) error {
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	sum, err := certificate.SummarizeCertificate(cert) // fails without a subject
	if err != nil {
		return err
	}
	if sum.Issuer == "" {
		return errors.New("certificate has no OIDC issuer")
	}
	return nil
}

// keyPair is a sign.Keypair over a long-lived ECDSA P-256 key.
type keyPair struct {
	priv *ecdsa.PrivateKey
	hint []byte
}

// ponytail: ECDSA P-256 PEM files only; cosign-encrypted keys and KMS URIs
// can be added behind sign.Keypair when someone needs them.
func loadKey(path string) (*keyPair, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading key: %w", err)
	}
	k, err := cryptoutils.UnmarshalPEMToPrivateKey(data, cryptoutils.SkipPassword)
	if err != nil {
		return nil, fmt.Errorf("parsing key %s: %w", path, err)
	}
	priv, ok := k.(*ecdsa.PrivateKey)
	if !ok || priv.Curve != elliptic.P256() {
		return nil, fmt.Errorf("key %s: only ECDSA P-256 keys are supported", path)
	}
	pub, _ := cryptoutils.MarshalPublicKeyToPEM(&priv.PublicKey) // cannot fail for a P-256 key
	kid, _ := evidence.PublicKeyID(string(pub))                  // cannot fail: a PKIX PEM just built
	return &keyPair{priv: priv, hint: []byte(kid)}, nil
}

func (k *keyPair) GetHashAlgorithm() protocommon.HashAlgorithm {
	return protocommon.HashAlgorithm_SHA2_256
}

func (k *keyPair) GetSigningAlgorithm() protocommon.PublicKeyDetails {
	return protocommon.PublicKeyDetails_PKIX_ECDSA_P256_SHA_256
}

func (k *keyPair) GetHint() []byte                { return k.hint }
func (k *keyPair) GetKeyAlgorithm() string        { return "ECDSA" }
func (k *keyPair) GetPublicKey() crypto.PublicKey { return k.priv.Public() }

func (k *keyPair) GetPublicKeyPem() (string, error) {
	b, err := cryptoutils.MarshalPublicKeyToPEM(k.priv.Public())
	return string(b), err
}

func (k *keyPair) SignData(_ context.Context, data []byte) ([]byte, []byte, error) {
	digest := sha256.Sum256(data)
	sig, err := ecdsa.SignASN1(rand.Reader, k.priv, digest[:])
	return sig, digest[:], err
}
