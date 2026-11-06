package consumer

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/TrianaLab/aval/pkg/evidence"
)

const fp = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func newKey(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return priv, string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func predicate(cacheability string) evidence.Predicate {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return evidence.Predicate{
		SchemaVersion: evidence.SchemaVersion,
		Subject:       evidence.Subject{Name: "git+https://example.com/app", Digest: map[string]string{"gitCommit": "abc"}},
		Test:          evidence.Test{ID: "app-integration", Cacheability: cacheability},
		Result:        evidence.ResultPassed,
		Execution:     evidence.Execution{Fingerprint: fp, FingerprintVersion: "aval-fingerprint/v1"},
		Runner:        evidence.Runner{Identity: "ci", TrustLevel: evidence.TrustRunner},
		StartedAt:     start,
		FinishedAt:    start.Add(time.Minute),
	}
}

// envelope signs an in-toto statement carrying p, as a producer would.
func envelope(t *testing.T, key ed25519.PrivateKey, p evidence.Predicate) []byte {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evidence.Parse(raw); err != nil {
		t.Fatal(err)
	}
	st, _ := json.Marshal(evidence.Statement{
		Type:          evidence.StatementType,
		Subject:       []evidence.ResourceDigest{{Name: p.Subject.Name, Digest: p.Subject.Digest}},
		PredicateType: evidence.PredicateType,
		Predicate:     raw,
	})
	env, _ := json.Marshal(evidence.Envelope{
		PayloadType: evidence.PayloadType,
		Payload:     st,
		Signatures:  []evidence.Signature{{Sig: ed25519.Sign(key, PAE(evidence.PayloadType, st))}},
	})
	return env
}

func options(t *testing.T, trusted string) evidence.VerifyOptions {
	t.Helper()
	sig, err := NewEd25519Verifier(trusted)
	if err != nil {
		t.Fatal(err)
	}
	return evidence.VerifyOptions{
		ExpectedFingerprint: fp,
		ExpectedTestID:      "app-integration",
		Identities:          []evidence.Identity{{PublicKey: trusted, TrustLevel: evidence.TrustRunner}},
		MinTrustLevel:       evidence.TrustRunner,
		Signature:           sig,
	}
}

func TestCheckAcceptsTrustedResult(t *testing.T) {
	key, pub := newKey(t)
	p, err := Check(context.Background(), envelope(t, key, predicate("hermetic")), options(t, pub))
	if err != nil || p.Result != evidence.ResultPassed {
		t.Fatalf("got %v, %v", p, err)
	}
}

func TestCheckMissIsNotVerificationFailure(t *testing.T) {
	_, pub := newKey(t)
	_, err := Check(context.Background(), nil, options(t, pub))
	if !errors.Is(err, evidence.ErrNotFound) || errors.Is(err, evidence.ErrVerification) {
		t.Fatalf("got %v, want a miss", err)
	}
}

func TestCheckRejectsUntrustedResults(t *testing.T) {
	key, pub := newKey(t)
	other, _ := newKey(t)
	good := envelope(t, key, predicate("hermetic"))
	cases := map[string][]byte{
		"unknown signer":    envelope(t, other, predicate("hermetic")),
		"non-deterministic": envelope(t, key, predicate("non-deterministic")),
		"tampered payload":  []byte(strings.Replace(string(good), `"payload":"`, `"payload":"AA`, 1)),
		"not json":          []byte("{"),
	}
	for name, env := range cases {
		if _, err := Check(context.Background(), env, options(t, pub)); !errors.Is(err, evidence.ErrVerification) {
			t.Errorf("%s: got %v, want a verification failure", name, err)
		}
	}
	opts := options(t, pub)
	opts.ExpectedFingerprint = "sha256:" + strings.Repeat("b", 64)
	if _, err := Check(context.Background(), good, opts); !errors.Is(err, evidence.ErrVerification) {
		t.Errorf("other fingerprint: got %v", err)
	}
}

func TestNewEd25519VerifierRejectsOtherKeys(t *testing.T) {
	if _, err := NewEd25519Verifier("not pem"); err == nil {
		t.Error("accepted garbage")
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&ec.PublicKey)
	if _, err := NewEd25519Verifier(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))); err == nil {
		t.Error("accepted an ECDSA key")
	}
}

func TestReuse(t *testing.T) {
	report := func(edit func(*evidence.Report)) []byte {
		r := evidence.Report{Version: evidence.ReportVersion, Outcome: evidence.OutcomeVerified, TestID: "app-integration", Fingerprint: fp, Result: evidence.ResultPassed}
		edit(&r)
		b, _ := json.Marshal(r)
		return b
	}
	cases := []struct {
		name     string
		report   []byte
		reuse    bool
		security bool
		err      bool
	}{
		{"verified passed", report(func(*evidence.Report) {}), true, false, false},
		{"verified failed", report(func(r *evidence.Report) { r.Result = evidence.ResultFailed }), false, false, false},
		{"miss", report(func(r *evidence.Report) { r.Outcome, r.Result = evidence.OutcomeMiss, "" }), false, false, false},
		{"error", report(func(r *evidence.Report) { r.Outcome = evidence.OutcomeError }), false, false, false},
		{"verification failed", report(func(r *evidence.Report) { r.Outcome = evidence.OutcomeVerificationFailed }), false, true, true},
		{"rejected candidate", report(func(r *evidence.Report) {
			r.Rejected = []evidence.Rejection{{Reference: "r@sha256:1", Outcome: evidence.OutcomeVerificationFailed, Reason: "bad sig"}}
		}), false, true, true},
		{"unknown version", report(func(r *evidence.Report) { r.Version = "aval.trianalab.io/verify-report/v9" }), false, false, true},
		{"not json", []byte("{"), false, false, true},
	}
	for _, c := range cases {
		reuse, err := Reuse(c.report)
		if reuse != c.reuse || (err != nil) != c.err || errors.Is(err, evidence.ErrVerification) != c.security {
			t.Errorf("%s: got %v, %v", c.name, reuse, err)
		}
	}
}
