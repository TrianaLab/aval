package attest

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protorekor "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/sign"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/TrianaLab/aval/pkg/evidence"
)

func predicate() *evidence.Predicate {
	start := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	return &evidence.Predicate{
		SchemaVersion: evidence.SchemaVersion,
		Subject:       evidence.Subject{Name: "git+https://github.com/acme/orders", Digest: map[string]string{"gitCommit": "0123abcd"}},
		Test:          evidence.Test{ID: "orders-integration", Cacheability: "hermetic"},
		Result:        evidence.ResultPassed,
		Execution:     evidence.Execution{Fingerprint: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		Runner:        evidence.Runner{Identity: "ci"},
		StartedAt:     start,
		FinishedAt:    start.Add(time.Minute),
	}
}

func TestNewStatement(t *testing.T) {
	p := predicate()
	s, err := NewStatement(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Type != evidence.StatementType || s.PredicateType != evidence.PredicateType {
		t.Fatalf("unexpected statement types: %+v", s)
	}
	if len(s.Subject) != 1 || s.Subject[0].Name != p.Subject.Name || s.Subject[0].Digest["gitCommit"] != "0123abcd" {
		t.Fatalf("statement subject must be the tested revision, got %+v", s.Subject)
	}
	var back evidence.Predicate
	if err := json.Unmarshal(s.Predicate, &back); err != nil || back.Test.ID != p.Test.ID {
		t.Fatalf("predicate does not round-trip: %v %+v", err, back)
	}
}

func TestNewStatementRejectsBadPredicates(t *testing.T) {
	invalid := predicate()
	invalid.Test.ID = ""
	if _, err := NewStatement(invalid); err == nil {
		t.Error("invalid predicate must be rejected")
	}
	// Valid per Validate, but time.Time cannot encode years past 9999.
	unencodable := predicate()
	unencodable.StartedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	unencodable.FinishedAt = unencodable.StartedAt
	if _, err := NewStatement(unencodable); err == nil {
		t.Error("unencodable predicate must be rejected")
	}
}

const (
	ghIssuer = "https://token.actions.githubusercontent.com"
	ciSAN    = "https://github.com/acme/orders/.github/workflows/ci.yaml@refs/heads/main"
)

// fakeFulcio certifies the keypair's public key for a fixed identity,
// standing in for Fulcio so no test talks to the network.
type fakeFulcio struct {
	issuer string // OIDC issuer extension
	err    error
	gotTok string
}

func (f *fakeFulcio) GetCertificate(_ context.Context, kp sign.Keypair, o *sign.CertificateProviderOptions) ([]byte, error) {
	f.gotTok = o.IDToken
	if f.err != nil {
		return nil, f.err
	}
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	san, _ := url.Parse(ciSAN)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(10 * time.Minute),
		URIs:         []*url.URL{san},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}
	v, _ := asn1.MarshalWithParams(f.issuer, "utf8")
	tmpl.ExtraExtensions = []pkix.Extension{{Id: certificate.OIDIssuerV2, Value: v}}
	return x509.CreateCertificate(rand.Reader, tmpl, tmpl, kp.GetPublicKey(), caKey)
}

// fakeRekor records that a transparency log entry was requested.
type fakeRekor struct{ calls int }

func (r *fakeRekor) GetTransparencyLogEntry(_ context.Context, _ []byte, b *protobundle.Bundle) error {
	r.calls++
	b.VerificationMaterial.TlogEntries = append(b.VerificationMaterial.TlogEntries, &protorekor.TransparencyLogEntry{LogIndex: 42})
	return nil
}

type seams struct {
	fulcio *fakeFulcio
	rekor  *fakeRekor
}

func useFakes(t *testing.T) *seams {
	t.Helper()
	s := &seams{fulcio: &fakeFulcio{issuer: ghIssuer}, rekor: &fakeRekor{}}
	oldF, oldR := newFulcio, newRekor
	newFulcio = func() sign.CertificateProvider { return s.fulcio }
	newRekor = func() sign.Transparency { return s.rekor }
	t.Cleanup(func() { newFulcio, newRekor = oldF, oldR })
	return s
}

// keySigner returns the signer the production verifier finds in a
// key-based bundle, with pub (PEM) the only accepted key.
func keySigner(t *testing.T, out *Signed, pub string) evidence.Signer {
	t.Helper()
	v, err := NewVerifier("", []evidence.Identity{{PublicKey: pub, TrustLevel: evidence.TrustDeveloper}})
	if err != nil {
		t.Fatal(err)
	}
	sig, err := v.For(out.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := sig.VerifySignature(context.Background(), out.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// certSigner reads the identity in a keyless bundle's certificate. The fake
// Fulcio and Rekor do not chain to a trusted root, so the verifier cannot
// be used here; verify_test.go covers keyless verification.
func certSigner(t *testing.T, out *Signed) evidence.Signer {
	t.Helper()
	cert, err := x509.ParseCertificate(parseBundle(t, out.Bundle).GetVerificationMaterial().GetCertificate().GetRawBytes())
	if err != nil {
		t.Fatal(err)
	}
	sum, err := certificate.SummarizeCertificate(cert)
	if err != nil {
		t.Fatal(err)
	}
	return evidence.Signer{Issuer: sum.Issuer, Subject: sum.SubjectAlternativeName}
}

func statement(t *testing.T) *evidence.Statement {
	t.Helper()
	s, err := NewStatement(predicate())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func writePEM(t *testing.T, key any) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// checkEnvelope verifies the DSSE signature with pub and that the payload
// round-trips through evidence.Parse back to the signed predicate.
func checkEnvelope(t *testing.T, env *evidence.Envelope, pub *ecdsa.PublicKey) {
	t.Helper()
	// The envelope also survives its own JSON encoding (base64 payload/sig).
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var back evidence.Envelope
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.PayloadType != evidence.PayloadType || len(back.Signatures) != 1 {
		t.Fatalf("unexpected envelope: %+v", back)
	}
	pae := fmt.Sprintf("DSSEv1 %d %s %d %s", len(back.PayloadType), back.PayloadType, len(back.Payload), back.Payload)
	digest := sha256.Sum256([]byte(pae))
	if !ecdsa.VerifyASN1(pub, digest[:], back.Signatures[0].Sig) {
		t.Fatal("DSSE signature does not verify with the signing key")
	}
	var st evidence.Statement
	if err := json.Unmarshal(back.Payload, &st); err != nil {
		t.Fatal(err)
	}
	if st.Type != evidence.StatementType || st.PredicateType != evidence.PredicateType {
		t.Fatalf("payload is not an Aval statement: %+v", st)
	}
	p, err := evidence.Parse(st.Predicate)
	if err != nil {
		t.Fatalf("predicate does not round-trip through evidence.Parse: %v", err)
	}
	if p.Test.ID != predicate().Test.ID || p.Execution.Fingerprint != predicate().Execution.Fingerprint {
		t.Fatalf("round-tripped predicate differs: %+v", p)
	}
}

func parseBundle(t *testing.T, raw []byte) *protobundle.Bundle {
	t.Helper()
	var b protobundle.Bundle
	if err := protojson.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	return &b
}

func TestSignKeyBased(t *testing.T) {
	s := useFakes(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	out, err := Sign(context.Background(), statement(t), SignOptions{Key: writePEM(t, key)})
	if err != nil {
		t.Fatal(err)
	}
	checkEnvelope(t, out.Envelope, &key.PublicKey)
	pub := pubPEM(t, &key.PublicKey)
	kid, _ := evidence.PublicKeyID(pub)
	if got := keySigner(t, out, pub); got != (evidence.Signer{KeyID: kid}) {
		t.Fatalf("key-based signer must be the key: %+v", got)
	}
	b := parseBundle(t, out.Bundle)
	if b.GetVerificationMaterial().GetPublicKey().GetHint() != kid {
		t.Fatal("bundle must carry the key's evidence.PublicKeyID as hint")
	}
	if s.rekor.calls != 0 || s.fulcio.gotTok != "" || len(b.GetVerificationMaterial().GetTlogEntries()) != 0 {
		t.Fatal("key-based signing must not contact Fulcio or Rekor")
	}
}

func TestSignKeyless(t *testing.T) {
	s := useFakes(t)
	out, err := Sign(context.Background(), statement(t), SignOptions{IDToken: "oidc-token"})
	if err != nil {
		t.Fatal(err)
	}
	if s.rekor.calls != 1 || s.fulcio.gotTok != "oidc-token" {
		t.Fatalf("keyless must use Fulcio and Rekor with the token: %+v", s)
	}
	if got := certSigner(t, out); got != (evidence.Signer{Issuer: ghIssuer, Subject: ciSAN}) {
		t.Fatalf("signer must come from the certificate: %+v", got)
	}
	b := parseBundle(t, out.Bundle)
	cert, err := x509.ParseCertificate(b.GetVerificationMaterial().GetCertificate().GetRawBytes())
	if err != nil {
		t.Fatal(err)
	}
	checkEnvelope(t, out.Envelope, cert.PublicKey.(*ecdsa.PublicKey))
	if len(b.GetVerificationMaterial().GetTlogEntries()) != 1 {
		t.Fatal("keyless bundle must carry the Rekor entry")
	}
}

// The trust level comes from who signed, matched against the policy. The
// predicate claims isolated (3) in every case below; that claim is ignored.
func TestTrustLevelComesFromSignerNotPredicate(t *testing.T) {
	useFakes(t)
	st := predicate()
	st.Runner = evidence.Runner{Identity: ciSAN, Issuer: ghIssuer, TrustLevel: evidence.TrustIsolated} //nolint:staticcheck // a claim that must be ignored
	stmt, err := NewStatement(st)
	if err != nil {
		t.Fatal(err)
	}
	keyless, err := Sign(context.Background(), stmt, SignOptions{IDToken: "t"})
	if err != nil {
		t.Fatal(err)
	}
	dev := []evidence.Identity{{Issuer: ghIssuer, SubjectRegexp: `https://github\.com/acme/.*`, TrustLevel: evidence.TrustDeveloper}}
	if id, ok := evidence.MatchSigner(dev, certSigner(t, keyless)); !ok || id.TrustLevel != evidence.TrustDeveloper || id.SubjectRegexp != dev[0].SubjectRegexp {
		t.Fatalf("got %+v, want developer from the policy", id)
	}
	other := []evidence.Identity{{Issuer: "https://other.example", SubjectRegexp: `.*`, TrustLevel: evidence.TrustIsolated}}
	if id, ok := evidence.MatchSigner(other, certSigner(t, keyless)); ok {
		t.Fatalf("unmatched signer got %+v", id)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	keyed, err := Sign(context.Background(), stmt, SignOptions{Key: writePEM(t, key)})
	if err != nil {
		t.Fatal(err)
	}
	wildcard := []evidence.Identity{{Issuer: ghIssuer, SubjectRegexp: `.*`, TrustLevel: evidence.TrustIsolated}}
	if id, ok := evidence.MatchSigner(wildcard, keySigner(t, keyed, pubPEM(t, &key.PublicKey))); ok {
		t.Fatalf("key-based signer matched a keyless identity: %+v", id)
	}
}

func TestSignRejects(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage.pem")
	_ = os.WriteFile(garbage, []byte("not a key"), 0o600)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	_, ed, _ := ed25519.GenerateKey(rand.Reader)
	badRaw := statement(t)
	badRaw.Predicate = json.RawMessage(`{`)
	wrongType := statement(t)
	wrongType.PredicateType = "https://example.com/other"

	cases := []struct {
		name string
		st   *evidence.Statement
		opts SignOptions
		set  func(*seams)
	}{
		{"nil statement", nil, SignOptions{IDToken: "t"}, nil},
		{"foreign predicate type", wrongType, SignOptions{IDToken: "t"}, nil},
		{"unencodable statement", badRaw, SignOptions{IDToken: "t"}, nil},
		{"keyless without token", statement(t), SignOptions{}, nil},
		{"missing key file", statement(t), SignOptions{Key: filepath.Join(dir, "nope.pem")}, nil},
		{"unparsable key", statement(t), SignOptions{Key: garbage}, nil},
		{"non-P-256 ECDSA key", statement(t), SignOptions{Key: writePEM(t, p384)}, nil},
		{"non-ECDSA key", statement(t), SignOptions{Key: writePEM(t, ed)}, nil},
		{"Fulcio error", statement(t), SignOptions{IDToken: "t"}, func(s *seams) { s.fulcio.err = errors.New("fulcio down") }},
	}
	for _, c := range cases {
		s := useFakes(t)
		if c.set != nil {
			c.set(s)
		}
		if out, err := Sign(ctx, c.st, c.opts); err == nil || out != nil {
			t.Errorf("%s: got %v, %v; want an error", c.name, out, err)
		}
	}
}

func TestSignSurfacesInternalFailures(t *testing.T) {
	useFakes(t)
	old := newEphemeralKeypair
	t.Cleanup(func() { newEphemeralKeypair = old })

	newEphemeralKeypair = func() (sign.Keypair, error) { return nil, errors.New("no entropy") }
	if _, err := Sign(context.Background(), statement(t), SignOptions{IDToken: "t"}); err == nil {
		t.Error("keypair failure must surface")
	}
}

// The production seams build real sigstore-go clients without contacting
// anything until they are used.
func TestDefaultSeams(t *testing.T) {
	if newFulcio() == nil || newRekor() == nil {
		t.Fatal("default clients must be constructed")
	}
	if kp, err := newEphemeralKeypair(); err != nil || kp == nil {
		t.Fatalf("ephemeral keypair: %v", err)
	}
}

// Metadata sigstore-go reads from a key (Fulcio requests, verifiers).
func TestKeyPairMetadata(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	kp, err := loadKey(writePEM(t, key))
	if err != nil {
		t.Fatal(err)
	}
	if kp.GetSigningAlgorithm() != protocommon.PublicKeyDetails_PKIX_ECDSA_P256_SHA_256 || kp.GetKeyAlgorithm() != "ECDSA" {
		t.Fatalf("unexpected algorithm: %v %v", kp.GetSigningAlgorithm(), kp.GetKeyAlgorithm())
	}
	if !key.PublicKey.Equal(kp.GetPublicKey()) {
		t.Fatal("public key differs from the loaded key")
	}
}
