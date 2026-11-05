package attest

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/TrianaLab/aval/pkg/evidence"
)

func pubPEM(t *testing.T, pub any) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// keySigned signs the test statement with a fresh P-256 key and returns the
// result and the public key PEM.
func keySigned(t *testing.T) (*Signed, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	s, err := Sign(context.Background(), statement(t), SignOptions{Key: writePEM(t, key)})
	if err != nil {
		t.Fatal(err)
	}
	return s, pubPEM(t, &key.PublicKey)
}

func policyOpts(sig evidence.SignatureVerifier, ids ...evidence.Identity) evidence.VerifyOptions {
	return evidence.VerifyOptions{
		ExpectedFingerprint: predicate().Execution.Fingerprint,
		ExpectedTestID:      predicate().Test.ID,
		Identities:          ids,
		MinTrustLevel:       evidence.TrustDeveloper,
		Signature:           sig,
	}
}

func TestVerifyKeyBasedBundle(t *testing.T) {
	s, pub := keySigned(t)
	id := evidence.Identity{PublicKey: pub, TrustLevel: evidence.TrustDeveloper}
	v, err := NewVerifier("", []evidence.Identity{id})
	if err != nil {
		t.Fatal(err)
	}
	got, err := evidence.Verify(context.Background(), s.Envelope, policyOpts(v.For(s.Bundle), id))
	if err != nil {
		t.Fatal(err)
	}
	kid, _ := evidence.PublicKeyID(pub)
	if got.Signer.KeyID != kid || got.Signer.KeyID != s.Signer.KeyID || got.TrustLevel != evidence.TrustDeveloper {
		t.Fatalf("unexpected verification: %+v", got)
	}
}

// False hits on the signature itself: each must fail inside the signature
// check, so evidence.Verify reports ErrVerification.
func TestVerifyRejectsBadKeyBasedBundles(t *testing.T) {
	s, pub := keySigned(t)
	_, otherPub := keySigned(t)
	trusted := evidence.Identity{PublicKey: pub, TrustLevel: evidence.TrustRunner}

	// Payload edited consistently in the bundle and the envelope: the
	// signature no longer covers it.
	b := parseBundle(t, s.Bundle)
	forged := strings.Replace(string(b.GetDsseEnvelope().GetPayload()), "orders-integration", "orders-integratiom", 1)
	b.GetDsseEnvelope().Payload = []byte(forged)
	forgedBundle, _ := protojson.Marshal(b)
	forgedEnv := *s.Envelope
	forgedEnv.Payload = []byte(forged)

	editedEnv := *s.Envelope
	editedEnv.Payload = []byte(strings.Replace(string(s.Envelope.Payload), "passed", "failed", 1))

	cases := []struct {
		name   string
		ids    []evidence.Identity
		env    *evidence.Envelope
		bundle []byte
		want   string
	}{
		{"untrusted key", []evidence.Identity{{PublicKey: otherPub, TrustLevel: evidence.TrustRunner}}, s.Envelope, s.Bundle, "signature:"},
		{"no bundle", []evidence.Identity{trusted}, s.Envelope, nil, "no Sigstore bundle"},
		{"garbage bundle", []evidence.Identity{trusted}, s.Envelope, []byte("{}"), "decoding Sigstore bundle"},
		{"envelope edited after signing", []evidence.Identity{trusted}, &editedEnv, s.Bundle, "different envelope"},
		{"payload forged in bundle and envelope", []evidence.Identity{trusted}, &forgedEnv, forgedBundle, "signature:"},
		{"policy has no keys", []evidence.Identity{{Issuer: ghIssuer, SubjectRegexp: ".*", TrustLevel: evidence.TrustRunner}}, s.Envelope, s.Bundle, "accepts no keys"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTrustedRoot(t, nil)
			v, err := NewVerifier("", tc.ids)
			if err != nil {
				t.Fatal(err)
			}
			_, err = evidence.Verify(context.Background(), tc.env, policyOpts(v.For(tc.bundle), tc.ids...))
			if !errors.Is(err, evidence.ErrVerification) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want ErrVerification mentioning %q", err, tc.want)
			}
		})
	}
}

// useTrustedRoot replaces the public-good trusted root with tm (a virtual
// Sigstore when nil) so no test fetches it over the network.
func useTrustedRoot(t *testing.T, tm root.TrustedMaterial) *ca.VirtualSigstore {
	t.Helper()
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	if tm == nil {
		tm = vs
	}
	old := loadTrustedRoot
	t.Cleanup(func() { loadTrustedRoot = old })
	loadTrustedRoot = func(string) (root.TrustedMaterial, error) { return tm, nil }
	return vs
}

// keylessEntity signs the test statement with a short-lived certificate from
// vs, as Fulcio and Rekor would, and returns it with the matching envelope.
func keylessEntity(t *testing.T, vs *ca.VirtualSigstore, subject, issuer string) (verify.SignedEntity, *evidence.Envelope) {
	t.Helper()
	st := statement(t)
	payload, _ := json.Marshal(st)
	e, err := vs.Attest(subject, issuer, payload)
	if err != nil {
		t.Fatal(err)
	}
	return e, &evidence.Envelope{PayloadType: evidence.PayloadType, Payload: payload}
}

// The virtual Sigstore issues certificates without embedded SCTs; the
// production options must refuse them.
func TestVerifyKeylessRequiresSCT(t *testing.T) {
	vs := useTrustedRoot(t, nil)
	v, _ := NewVerifier("", []evidence.Identity{{Issuer: ghIssuer, SubjectRegexp: ".*", TrustLevel: evidence.TrustRunner}})
	e, env := keylessEntity(t, vs, ciSAN, ghIssuer)
	if _, err := v.verifyEntity(e, env); err == nil || !strings.Contains(err.Error(), "signed certificate timestamp") {
		t.Fatalf("certificate without SCT: %v", err)
	}
}

// withoutSCT drops the SCT requirement, which the virtual Sigstore cannot
// meet; Rekor and timestamp requirements stay.
func withoutSCT(t *testing.T) {
	t.Helper()
	old := keylessOptions
	t.Cleanup(func() { keylessOptions = old })
	keylessOptions = []verify.VerifierOption{verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1)}
}

func TestVerifyKeyless(t *testing.T) {
	withoutSCT(t)
	vs := useTrustedRoot(t, nil)
	ids := []evidence.Identity{{Issuer: ghIssuer, SubjectRegexp: `https://github\.com/acme/orders/.*`, TrustLevel: evidence.TrustRunner}}
	v, err := NewVerifier("", ids)
	if err != nil {
		t.Fatal(err)
	}
	e, env := keylessEntity(t, vs, ciSAN, ghIssuer)
	got, err := evidence.Verify(context.Background(), env, policyOpts(entitySig{v, e}, ids...))
	if err != nil {
		t.Fatal(err)
	}
	// The real certificate identity, not only the regexp that matched it.
	if got.Signer.Issuer != ghIssuer || got.Signer.Subject != ciSAN || got.Signer.SubjectRegexp != ids[0].SubjectRegexp {
		t.Fatalf("unexpected signer: %+v", got.Signer)
	}

	// A certificate from another CA does not chain to the trusted root.
	foreign, _ := ca.NewVirtualSigstore()
	fe, fenv := keylessEntity(t, foreign, ciSAN, ghIssuer)
	if _, err := v.verifyEntity(fe, fenv); err == nil {
		t.Fatal("certificate from an untrusted CA verified")
	}
	// A keyless signature against a key-only policy.
	_, pub := keySigned(t)
	keyOnly, _ := NewVerifier("", []evidence.Identity{{PublicKey: pub, TrustLevel: evidence.TrustRunner}})
	if _, err := keyOnly.verifyEntity(e, env); err == nil || !strings.Contains(err.Error(), "no keyless") {
		t.Fatalf("keyless signature against key-only policy: %v", err)
	}
	// A message signature (no DSSE envelope) is not an attestation.
	msg, _ := vs.Sign(ciSAN, ghIssuer, env.Payload)
	if _, err := v.verifyEntity(msg, env); err == nil || !strings.Contains(err.Error(), "DSSE") {
		t.Fatalf("message signature: %v", err)
	}
	// A certificate without an OIDC issuer cannot be matched by policy.
	noIss, nenv := keylessEntity(t, vs, ciSAN, "")
	if _, err := v.verifyEntity(noIss, nenv); err == nil {
		t.Fatal("certificate without issuer verified")
	}
}

type entitySig struct {
	v *Verifier
	e verify.SignedEntity
}

func (s entitySig) VerifySignature(_ context.Context, env *evidence.Envelope) (evidence.Signer, error) {
	return s.v.verifyEntity(s.e, env)
}

// brokenEntity fails to expose its content.
type brokenEntity struct {
	verify.SignedEntity
	sigErr, vcErr error
}

func (b brokenEntity) SignatureContent() (verify.SignatureContent, error) {
	if b.sigErr != nil {
		return nil, b.sigErr
	}
	return b.SignedEntity.SignatureContent()
}

func (b brokenEntity) VerificationContent() (verify.VerificationContent, error) {
	return nil, b.vcErr
}

func TestVerifyEntityContentErrors(t *testing.T) {
	withoutSCT(t)
	vs := useTrustedRoot(t, nil)
	v, _ := NewVerifier("", []evidence.Identity{{Issuer: ghIssuer, SubjectRegexp: ".*", TrustLevel: evidence.TrustRunner}})
	e, env := keylessEntity(t, vs, ciSAN, ghIssuer)
	for _, b := range []brokenEntity{{SignedEntity: e, sigErr: errors.New("no sig")}, {SignedEntity: e, vcErr: errors.New("no vc")}} {
		if _, err := v.verifyEntity(b, env); err == nil {
			t.Fatalf("%+v: content error swallowed", b)
		}
	}
}

func TestNewVerifierErrors(t *testing.T) {
	x, _ := ecdh.X25519().GenerateKey(rand.Reader)
	cases := map[string]struct {
		root string
		ids  []evidence.Identity
	}{
		"bad public key":         {"", []evidence.Identity{{PublicKey: "nope"}}},
		"unsupported key type":   {"", []evidence.Identity{{PublicKey: pubPEM(t, x.PublicKey())}}},
		"missing trusted root":   {filepath.Join(t.TempDir(), "missing.json"), []evidence.Identity{{Issuer: ghIssuer, SubjectRegexp: ".*"}}},
		"trusted root not fetch": {"", []evidence.Identity{{Issuer: ghIssuer, SubjectRegexp: ".*"}}},
	}
	old := fetchPublicGood
	t.Cleanup(func() { fetchPublicGood = old })
	fetchPublicGood = func() (*root.TrustedRoot, error) { return nil, errors.New("offline") }
	for name, tc := range cases {
		if _, err := NewVerifier(tc.root, tc.ids); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// No keyless identity: the trusted root is never needed, nor fetched.
	if v, err := NewVerifier("", nil); err != nil || v.keyless != nil || v.keyed != nil {
		t.Fatalf("empty policy: %+v, %v", v, err)
	}
}
