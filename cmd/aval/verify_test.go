package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TrianaLab/aval/internal/attest"
	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// devKey writes a P-256 key pair under dir and returns the private key path
// and the public key PEM.
func devKey(t *testing.T, dir, name string) (string, []byte) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	priv, _ := x509.MarshalPKCS8PrivateKey(k)
	pub, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	keyFile := filepath.Join(dir, name+".key")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}), 0o600); err != nil {
		t.Fatal(err)
	}
	return keyFile, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})
}

// signedFor signs a passing result for the contract's test id and
// fingerprint, edited by edit, with keyFile.
func signedFor(t *testing.T, contractFile, keyFile string, edit func(*evidence.Predicate)) *registry.Attestation {
	t.Helper()
	c, fp, err := loadAndFingerprint(contractFile)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	p := &evidence.Predicate{
		SchemaVersion: evidence.SchemaVersion,
		Subject:       evidence.Subject{Name: "git+https://example.com/repo", Digest: map[string]string{"gitCommit": "abc"}},
		Test:          evidence.Test{ID: c.Metadata.Name, Cacheability: string(c.Cacheability)},
		Result:        evidence.ResultPassed,
		Execution:     evidence.Execution{Fingerprint: fp.Digest, FingerprintVersion: "aval-fingerprint/v1"},
		Runner:        evidence.Runner{Identity: "dev", TrustLevel: evidence.TrustIsolated},
		StartedAt:     start,
		FinishedAt:    start.Add(time.Second),
	}
	if edit != nil {
		edit(p)
	}
	st, err := attest.NewStatement(p)
	if err != nil {
		t.Fatal(err)
	}
	s, err := attest.Sign(context.Background(), st, attest.SignOptions{Key: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	return &registry.Attestation{Envelope: s.Envelope, Bundle: s.Bundle}
}

// serve makes lookup return one candidate that fetches as a.
func serve(t *testing.T, a *registry.Attestation) {
	t.Helper()
	lk, f := lookup, fetch
	t.Cleanup(func() { lookup, fetch = lk, f })
	lookup = func(_ context.Context, repo, _ string) ([]registry.Descriptor, error) {
		return []registry.Descriptor{{Repository: repo, Digest: "sha256:aa"}}, nil
	}
	fetch = func(context.Context, registry.Descriptor) (*registry.Attestation, error) { return a, nil }
}

// End to end through the real policy file, attest.Verifier and
// evidence.Verify; only the registry is faked. Every rejection must reach the
// report as verification-failed with exit 5 (invariant 4).
func TestVerifyWithRealPolicy(t *testing.T) {
	dir := t.TempDir()
	keyFile, pub := devKey(t, dir, "dev")
	otherKey, _ := devKey(t, dir, "other")
	if err := os.WriteFile(filepath.Join(dir, "dev.pub"), pub, 0o600); err != nil {
		t.Fatal(err)
	}
	pol := filepath.Join(dir, "policy.yaml")
	writePol := func(min string) {
		body := "apiVersion: aval.trianalab.io/v1\nkind: Policy\nminTrustLevel: " + min +
			"\nidentities:\n  - publicKey: dev.pub\n    trustLevel: developer\n"
		if err := os.WriteFile(pol, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	hermetic := writeContract(t, "hermetic")
	other := writeContract(t, "hermetic")
	if err := os.WriteFile(filepath.Join(filepath.Dir(other), "in.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	tampered := signedFor(t, hermetic, keyFile, nil)
	env := *tampered.Envelope
	env.Payload = []byte(strings.Replace(string(env.Payload), `"passed"`, `"failed"`, 1))
	tampered.Envelope = &env
	noBundle := signedFor(t, hermetic, keyFile, nil)
	noBundle.Bundle = nil

	cases := []struct {
		name, min string
		att       *registry.Attestation
		code      int
		reason    string
	}{
		{"trusted developer key", "developer", signedFor(t, hermetic, keyFile, nil), ExitOK, ""},
		// A listed signer below minTrustLevel is routine non-reuse: a miss,
		// never a security event, and never reused (issue #65).
		{"developer key under a runner policy", "runner", signedFor(t, hermetic, keyFile, nil), ExitMiss, "trust level developer, policy requires at least runner"},
		{"developer key under an isolated policy", "isolated", signedFor(t, hermetic, keyFile, nil), ExitMiss, "policy requires at least isolated"},
	}
	// Every forgery stays a verification failure whatever the minimum level:
	// below the minimum, a false claim is not downgraded to a miss.
	for _, min := range []string{"developer", "runner"} {
		cases = append(cases, []struct {
			name, min string
			att       *registry.Attestation
			code      int
			reason    string
		}{
			{"untrusted key/" + min, min, signedFor(t, hermetic, otherKey, nil), ExitVerificationFailed, "signature"},
			{"envelope tampered/" + min, min, tampered, ExitVerificationFailed, "different envelope"},
			{"no bundle/" + min, min, noBundle, ExitVerificationFailed, "no Sigstore bundle"},
			{"replay for another fingerprint/" + min, min, signedFor(t, other, keyFile, nil), ExitVerificationFailed, "fingerprint"},
			{"replay for another test id/" + min, min, signedFor(t, hermetic, keyFile, func(p *evidence.Predicate) { p.Test.ID = "other" }), ExitVerificationFailed, "test id"},
			{"signed cacheability differs/" + min, min, signedFor(t, hermetic, keyFile, func(p *evidence.Predicate) { p.Test.Cacheability = "controlled-environment" }), ExitVerificationFailed, "cacheability"},
		}...)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writePol(tc.min)
			serve(t, tc.att)
			code, stdout, _ := runCLI("verify", "-f", hermetic, "--registry", "r", "--policy", pol, "-o", "json")
			if code != tc.code {
				t.Fatalf("exit %d, want %d; report %s", code, tc.code, stdout)
			}
			checkReport(t, stdout, tc.reason, pub)
		})
	}
}

// checkReport asserts a verified report names the developer key, a miss
// lists the below-minimum candidate as a miss, and any other report is a
// visible verification failure mentioning reason.
func checkReport(t *testing.T, stdout, reason string, pub []byte) {
	t.Helper()
	var r evidence.Report
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Reason, reason) {
		t.Fatalf("reason %q does not mention %q", r.Reason, reason)
	}
	if r.Outcome == evidence.OutcomeVerified {
		kid, _ := evidence.PublicKeyID(string(pub))
		if r.Signer == nil || r.Signer.KeyID != kid || r.Signer.TrustLevel != evidence.TrustDeveloper {
			t.Fatalf("verified report: %s", stdout)
		}
		return
	}
	if r.Outcome == evidence.OutcomeMiss {
		if r.Signer != nil || r.Predicate != nil || len(r.Rejected) != 1 || r.Rejected[0].Outcome != evidence.OutcomeMiss {
			t.Fatalf("below-minimum candidate not reported as a miss: %s", stdout)
		}
		return
	}
	if r.Outcome != evidence.OutcomeVerificationFailed || len(r.Rejected) != 1 || r.Rejected[0].Outcome != evidence.OutcomeVerificationFailed {
		t.Fatalf("failure not visible in the report: %s", stdout)
	}
}

// A policy whose trusted root cannot be loaded decides nothing about a
// keyless candidate: outcome error, never verification-failed. The root is
// loaded only once such a candidate shows up.
func TestVerifyTrustedRootError(t *testing.T) {
	dir := t.TempDir()
	pol := filepath.Join(dir, "policy.yaml")
	body := "apiVersion: aval.trianalab.io/v1\nkind: Policy\nminTrustLevel: runner\ntrustedRoot: missing.json\n" +
		"identities:\n  - issuer: https://issuer.example\n    subjectRegexp: ci\n    trustLevel: runner\n"
	if err := os.WriteFile(pol, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// A well-formed keyless bundle: a self-signed certificate.
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	serve(t, &registry.Attestation{
		Envelope: &evidence.Envelope{PayloadType: evidence.PayloadType, Payload: []byte("{}")},
		Bundle: []byte(`{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json","verificationMaterial":{"certificate":{"rawBytes":"` +
			base64.StdEncoding.EncodeToString(der) + `"}},"dsseEnvelope":{"payload":"e30=","payloadType":"application/vnd.in-toto+json","signatures":[{"sig":"AA=="}]}}`),
	})
	code, stdout, _ := runCLI("verify", "-f", writeContract(t, "hermetic"), "--registry", "r", "--policy", pol, "-o", "json")
	if code != ExitError || !strings.Contains(stdout, `"outcome": "error"`) || !strings.Contains(stdout, "trusted root") {
		t.Fatalf("exit %d, report %s", code, stdout)
	}
}

// A trust policy the Sigstore verifier cannot be built from decides nothing:
// outcome error, and the registry is never asked.
func TestVerifyVerifierError(t *testing.T) {
	fakeCandidates(t, nil, nil)
	noLookup(t)
	nv := newVerifier
	t.Cleanup(func() { newVerifier = nv })
	newVerifier = func(string, []evidence.Identity) (*attest.Verifier, error) { return nil, errors.New("bad key") }
	code, stdout, _ := runCLI("verify", "-f", writeContract(t, "hermetic"), "--registry", "r", "--policy", "p.yaml", "-o", "json")
	if code != ExitError || !strings.Contains(stdout, `"outcome": "error"`) || !strings.Contains(stdout, "bad key") {
		t.Fatalf("exit %d, report %s", code, stdout)
	}
}
