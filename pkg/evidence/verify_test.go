package evidence

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
)

const (
	testGH  = "https://token.actions.githubusercontent.com"
	testCI  = "https://github.com/acme/orders/.github/workflows/ci.yaml@refs/heads/main"
	testFP  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	otherFP = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// fakeSig stands in for the Sigstore verifier: it returns a fixed signer, or
// an error as if the signature did not verify.
type fakeSig struct {
	signer Signer
	err    error
}

func (f fakeSig) VerifySignature(context.Context, *Envelope) (Signer, error) { return f.signer, f.err }

func pemKey(t *testing.T) string {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// statementEnvelope wraps validPredicate, edited by edit, in an envelope.
func statementEnvelope(t *testing.T, edit func(*Statement)) *Envelope {
	t.Helper()
	p, err := Parse([]byte(validPredicate))
	if err != nil {
		t.Fatal(err)
	}
	st := Statement{
		Type:          StatementType,
		Subject:       []ResourceDigest{{Name: p.Subject.Name, Digest: p.Subject.Digest}},
		PredicateType: PredicateType,
		Predicate:     json.RawMessage(validPredicate),
	}
	if edit != nil {
		edit(&st)
	}
	raw, _ := json.Marshal(st)
	return &Envelope{PayloadType: PayloadType, Payload: raw, Signatures: []Signature{{Sig: []byte("sig")}}}
}

func baseOpts() VerifyOptions {
	return VerifyOptions{
		ExpectedFingerprint: testFP,
		ExpectedTestID:      "orders-integration",
		Identities:          []Identity{{Issuer: testGH, SubjectRegexp: `https://github\.com/acme/orders/\.github/workflows/ci\.yaml@refs/heads/main`, TrustLevel: TrustRunner}},
		MinTrustLevel:       TrustRunner,
		Signature:           fakeSig{signer: Signer{Issuer: testGH, Subject: testCI}},
	}
}

func withPredicate(from, to string) func(*Statement) {
	return func(s *Statement) {
		s.Predicate = json.RawMessage(strings.Replace(validPredicate, from, to, 1))
	}
}

func TestVerifyAccepts(t *testing.T) {
	v, err := Verify(context.Background(), statementEnvelope(t, nil), baseOpts())
	if err != nil {
		t.Fatal(err)
	}
	// The signer in the result is the real certificate identity, not only
	// the regexp entry that matched it.
	if v.Signer.Subject != testCI || v.Signer.Issuer != testGH || v.Signer.SubjectRegexp == "" ||
		v.TrustLevel != TrustRunner || v.Predicate.Test.ID != "orders-integration" {
		t.Fatalf("unexpected verification: %+v", v)
	}
	// A failed result verifies too: the caller sees Result and never treats
	// it as a pass.
	failed, err := Verify(context.Background(), statementEnvelope(t, withPredicate(`"result": "passed"`, `"result": "failed"`)), baseOpts())
	if err != nil || failed.Predicate.Result != ResultFailed {
		t.Fatalf("failed result: %+v, %v", failed, err)
	}
}

func TestVerifyKeyBasedSigner(t *testing.T) {
	key := pemKey(t)
	kid, err := PublicKeyID(key)
	if err != nil {
		t.Fatal(err)
	}
	o := baseOpts()
	o.MinTrustLevel = TrustDeveloper
	o.Identities = append(o.Identities, Identity{PublicKey: key, TrustLevel: TrustDeveloper})
	o.Signature = fakeSig{signer: Signer{KeyID: kid}}
	v, err := Verify(context.Background(), statementEnvelope(t, nil), o)
	if err != nil || v.Signer.KeyID != kid || v.TrustLevel != TrustDeveloper {
		t.Fatalf("key signer: %+v, %v", v, err)
	}
	// The same key signer does not satisfy a runner-level policy.
	o.MinTrustLevel = TrustRunner
	if _, err := Verify(context.Background(), statementEnvelope(t, nil), o); !errors.Is(err, ErrVerification) {
		t.Fatalf("developer key passed a runner policy: %v", err)
	}
}

// False hits: every row must end in ErrVerification (invariant 4), never in
// a reuse, a miss or a plain error.
func TestVerifyRejects(t *testing.T) {
	key := pemKey(t)
	kid, _ := PublicKeyID(key)
	otherKid, _ := PublicKeyID(pemKey(t))
	cases := []struct {
		name string
		env  func(*testing.T) *Envelope
		opts func(*VerifyOptions)
		want string
	}{
		{name: "replay for another fingerprint", opts: func(o *VerifyOptions) { o.ExpectedFingerprint = otherFP }, want: "fingerprint"},
		{name: "replay for another test id", opts: func(o *VerifyOptions) { o.ExpectedTestID = "payments-integration" }, want: "test id"},
		{name: "no expected fingerprint", opts: func(o *VerifyOptions) { o.ExpectedFingerprint = "" }, want: "required"},
		{name: "no expected test id", opts: func(o *VerifyOptions) { o.ExpectedTestID = "" }, want: "required"},
		{name: "no signature verifier", opts: func(o *VerifyOptions) { o.Signature = nil }, want: "no signature verifier"},
		{name: "bad signature", opts: func(o *VerifyOptions) { o.Signature = fakeSig{err: errors.New("invalid signature")} }, want: "signature: invalid signature"},
		{name: "nil envelope", env: func(*testing.T) *Envelope { return nil }, want: "envelope"},
		{name: "wrong payload type", env: func(t *testing.T) *Envelope {
			e := statementEnvelope(t, nil)
			e.PayloadType = "text/plain"
			return e
		}, want: "envelope"},
		{name: "empty identities accept nothing", opts: func(o *VerifyOptions) { o.Identities = nil }, want: "not accepted"},
		{name: "signer not in identities", opts: func(o *VerifyOptions) {
			o.Signature = fakeSig{signer: Signer{Issuer: testGH, Subject: "https://github.com/evil/orders/.github/workflows/ci.yaml@refs/heads/main"}}
		}, want: "not accepted"},
		{name: "issuer differs", opts: func(o *VerifyOptions) {
			o.Signature = fakeSig{signer: Signer{Issuer: "https://evil.example", Subject: testCI}}
		}, want: "not accepted"},
		{name: "unanchored regexp does not match a longer subject", opts: func(o *VerifyOptions) {
			o.Signature = fakeSig{signer: Signer{Issuer: testGH, Subject: testCI + "-fork"}}
		}, want: "not accepted"},
		{name: "key signer with unknown key", opts: func(o *VerifyOptions) {
			o.Identities = []Identity{{PublicKey: key, TrustLevel: TrustRunner}}
			o.Signature = fakeSig{signer: Signer{KeyID: otherKid}}
		}, want: "not accepted"},
		{name: "key signer cannot match a keyless entry", opts: func(o *VerifyOptions) { o.Signature = fakeSig{signer: Signer{KeyID: kid}} }, want: "not accepted"},
		{name: "signer with key and certificate identity", opts: func(o *VerifyOptions) {
			o.Identities = append(o.Identities, Identity{PublicKey: key, TrustLevel: TrustRunner})
			o.Signature = fakeSig{signer: Signer{Issuer: testGH, Subject: testCI, KeyID: kid}}
		}, want: "not accepted"},
		{name: "keyless signer cannot match a key entry", opts: func(o *VerifyOptions) {
			o.Identities = []Identity{{Issuer: testGH, SubjectRegexp: ".*", PublicKey: key, TrustLevel: TrustRunner}}
		}, want: "not accepted"},
		{name: "level below minimum", opts: func(o *VerifyOptions) { o.Identities[0].TrustLevel = TrustDeveloper }, want: "trust level developer"},
		{name: "unknown level never accepted even with no minimum", opts: func(o *VerifyOptions) {
			o.Identities[0].TrustLevel, o.MinTrustLevel = TrustUnknown, TrustUnknown
		}, want: "trust level unknown"},
		{name: "out of range level never accepted", opts: func(o *VerifyOptions) { o.Identities[0].TrustLevel = TrustLevel(42) }, want: "trust level unknown"},
		{name: "negative level never accepted", opts: func(o *VerifyOptions) {
			o.Identities[0].TrustLevel, o.MinTrustLevel = TrustLevel(-1), TrustLevel(-5)
		}, want: "trust level unknown"},
		// The predicate claims isolated, the policy grants developer: policy wins (invariant 5).
		{name: "predicate trust claim is ignored", env: func(t *testing.T) *Envelope {
			return statementEnvelope(t, withPredicate(`"trustLevel": 2`, `"trustLevel": 3`))
		}, opts: func(o *VerifyOptions) { o.Identities[0].TrustLevel = TrustDeveloper }, want: "trust level developer"},
		{name: "payload is not json", env: func(t *testing.T) *Envelope {
			e := statementEnvelope(t, nil)
			e.Payload = []byte("{")
			return e
		}, want: "decoding statement"},
		{name: "wrong statement type", env: func(t *testing.T) *Envelope {
			return statementEnvelope(t, func(s *Statement) { s.Type = "https://in-toto.io/Statement/v0.1" })
		}, want: "statement: type"},
		{name: "wrong predicate type", env: func(t *testing.T) *Envelope {
			return statementEnvelope(t, func(s *Statement) { s.PredicateType = "https://slsa.dev/provenance/v1" })
		}, want: "statement: type"},
		{name: "invalid predicate", env: func(t *testing.T) *Envelope {
			return statementEnvelope(t, withPredicate(`"id": "orders-integration"`, `"id": ""`))
		}, want: "predicate:"},
		{name: "statement subject differs from predicate", env: func(t *testing.T) *Envelope {
			return statementEnvelope(t, func(s *Statement) { s.Subject[0].Digest = map[string]string{"gitCommit": "ffff"} })
		}, want: "statement subject"},
		{name: "no statement subject", env: func(t *testing.T) *Envelope {
			return statementEnvelope(t, func(s *Statement) { s.Subject = nil })
		}, want: "statement subject"},
		{name: "external-dependencies without opt-in", env: func(t *testing.T) *Envelope {
			return statementEnvelope(t, withPredicate(`"controlled-environment"`, `"external-dependencies"`))
		}, want: "cacheability"},
		// Invariant 7: no policy can make non-deterministic reusable.
		{name: "non-deterministic forced by policy", env: func(t *testing.T) *Envelope {
			return statementEnvelope(t, withPredicate(`"controlled-environment"`, `"non-deterministic"`))
		}, opts: func(o *VerifyOptions) { o.AllowedCacheability = []string{"non-deterministic", "external-dependencies"} }, want: "cacheability"},
		{name: "unknown cacheability forced by policy", env: func(t *testing.T) *Envelope {
			return statementEnvelope(t, withPredicate(`"controlled-environment"`, `"sometimes"`))
		}, opts: func(o *VerifyOptions) { o.AllowedCacheability = []string{"sometimes"} }, want: "cacheability"},
		{name: "empty cacheability", env: func(t *testing.T) *Envelope {
			return statementEnvelope(t, withPredicate(`"controlled-environment"`, `""`))
		}, want: "cacheability"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := statementEnvelope(t, nil)
			if tc.env != nil {
				env = tc.env(t)
			}
			o := baseOpts()
			if tc.opts != nil {
				tc.opts(&o)
			}
			v, err := Verify(context.Background(), env, o)
			var ve *VerificationError
			if v != nil || !errors.As(err, &ve) || !errors.Is(err, ErrVerification) || errors.Is(err, ErrNotFound) {
				t.Fatalf("Verify = %+v, %v; want a VerificationError", v, err)
			}
			if !strings.Contains(ve.Reason, tc.want) {
				t.Fatalf("reason %q does not mention %q", ve.Reason, tc.want)
			}
		})
	}
}

func TestVerifyExternalDependenciesWithOptIn(t *testing.T) {
	o := baseOpts()
	o.AllowedCacheability = []string{"external-dependencies"}
	env := statementEnvelope(t, withPredicate(`"controlled-environment"`, `"external-dependencies"`))
	if _, err := Verify(context.Background(), env, o); err != nil {
		t.Fatal(err)
	}
	hermetic := statementEnvelope(t, withPredicate(`"controlled-environment"`, `"hermetic"`))
	if _, err := Verify(context.Background(), hermetic, baseOpts()); err != nil {
		t.Fatal(err)
	}
}

func TestMatchSignerPicksHighestKeyLevel(t *testing.T) {
	key := pemKey(t)
	kid, _ := PublicKeyID(key)
	ids := []Identity{
		{PublicKey: "garbage", TrustLevel: TrustIsolated},
		{PublicKey: key, TrustLevel: TrustRunner},
		{PublicKey: key, TrustLevel: TrustDeveloper},
	}
	id, ok := MatchSigner(ids, Signer{KeyID: kid})
	if !ok || id.TrustLevel != TrustRunner || id.KeyID != kid || id.Subject != "" {
		t.Fatalf("MatchSigner = %+v, %v", id, ok)
	}
}

func TestPublicKeyID(t *testing.T) {
	a, b := pemKey(t), pemKey(t)
	ida, err := PublicKeyID(a)
	if err != nil {
		t.Fatal(err)
	}
	if idb, _ := PublicKeyID(b); ida == idb {
		t.Fatal("different keys share an id")
	}
	// Surrounding whitespace does not change the id.
	if again, _ := PublicKeyID("\n" + a + "\n"); again != ida {
		t.Fatal("id depends on whitespace")
	}
	for name, in := range map[string]string{
		"empty":       "",
		"not pem":     "ssh-ed25519 AAAA",
		"private key": strings.Replace(a, "PUBLIC KEY", "PRIVATE KEY", 2),
		"garbage der": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("nope")})),
	} {
		if _, err := PublicKeyID(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
