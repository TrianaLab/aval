package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TrianaLab/aval/internal/attest"
	"github.com/TrianaLab/aval/internal/policy"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// A key from aval keygen signs through internal/attest, verifies against a
// policy file that trusts its .pub, and the key id keygen printed is the
// signer key id verification reports.
func TestKeygenSignVerify(t *testing.T) {
	dir := t.TempDir()
	kid, err := attest.WriteKeyPair(filepath.Join(dir, "aval"), false)
	if err != nil {
		t.Fatal(err)
	}
	pol := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(pol, []byte("apiVersion: aval.trianalab.io/v1\nkind: Policy\nminTrustLevel: developer\nidentities:\n  - publicKey: aval.pub\n    trustLevel: developer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := policy.Load(pol)
	if err != nil {
		t.Fatal(err)
	}
	fp := "sha256:" + strings.Repeat("5a", 32)
	start := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	st, err := attest.NewStatement(&evidence.Predicate{
		SchemaVersion: evidence.SchemaVersion,
		Subject:       evidence.Subject{Name: "git+https://github.com/acme/orders", Digest: map[string]string{"gitCommit": "0123abcd"}},
		Test:          evidence.Test{ID: "orders-integration", Cacheability: "hermetic"},
		Result:        evidence.ResultPassed,
		Execution:     evidence.Execution{Fingerprint: fp},
		Runner:        evidence.Runner{Identity: "dev"},
		StartedAt:     start,
		FinishedAt:    start.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := attest.Sign(ctx, st, attest.SignOptions{Key: filepath.Join(dir, "aval.key")})
	if err != nil {
		t.Fatal(err)
	}
	v, err := attest.NewVerifier(p.TrustedRoot, p.Identities)
	if err != nil {
		t.Fatal(err)
	}
	opts := p.VerifyOptions("orders-integration", fp)
	if opts.Signature, err = v.For(s.Bundle); err != nil {
		t.Fatal(err)
	}
	got, err := evidence.Verify(ctx, s.Envelope, opts)
	if err != nil || got.Signer.KeyID != kid {
		t.Fatalf("verify: signer %+v, printed key id %s, err %v", got.Signer, kid, err)
	}
}
