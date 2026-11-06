package integration

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ggcr "github.com/google/go-containerregistry/pkg/registry"

	"github.com/TrianaLab/aval/internal/attest"
	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// signed signs an execution with a key generated at test time and returns
// it with the public key PEM.
func signed(t *testing.T, fp, identity string) (*attest.Signed, string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	keyFile := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	st, err := attest.NewStatement(&evidence.Predicate{
		SchemaVersion: evidence.SchemaVersion,
		Subject:       evidence.Subject{Name: "git+https://github.com/acme/orders", Digest: map[string]string{"gitCommit": "0123abcd"}},
		Test:          evidence.Test{ID: "orders-integration", Cacheability: "hermetic"},
		Result:        evidence.ResultPassed,
		Execution:     evidence.Execution{Fingerprint: fp},
		Runner:        evidence.Runner{Identity: identity},
		StartedAt:     start,
		FinishedAt:    start.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := attest.Sign(context.Background(), st, attest.SignOptions{Key: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	return s, string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
}

// Sign, push to an in-process OCI registry (no referrers API, like GHCR:
// oras keeps the referrers tag schema), look up by fingerprint and fetch by
// digest. A second execution with the same fingerprint is one more
// candidate and never replaces the first.
func TestSignPushLookupFetch(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/orders-aval"
	ctx := context.Background()
	fp := "sha256:" + strings.Repeat("3f", 32)

	if _, err := registry.Lookup(ctx, repo, fp); !errors.Is(err, evidence.ErrNotFound) {
		t.Fatalf("before any push: want a miss, got %v", err)
	}
	ci, _ := signed(t, fp, "ci")
	dev, _ := signed(t, fp, "dev")
	pushed := map[string]*attest.Signed{}
	for _, s := range []*attest.Signed{ci, dev} {
		d, err := registry.Push(ctx, repo, fp, registry.Attestation{Envelope: s.Envelope, Bundle: s.Bundle})
		if err != nil {
			t.Fatal(err)
		}
		pushed[d.Digest] = s
	}
	cands, err := registry.Lookup(ctx, repo, fp)
	if err != nil || len(cands) != 2 {
		t.Fatalf("Lookup = %+v %v, want both executions", cands, err)
	}
	for _, d := range cands {
		a, err := registry.Fetch(ctx, d)
		if err != nil {
			t.Fatal(err)
		}
		want := pushed[d.Digest]
		if want == nil || string(a.Envelope.Payload) != string(want.Envelope.Payload) ||
			string(a.Envelope.Signatures[0].Sig) != string(want.Envelope.Signatures[0].Sig) || string(a.Bundle) != string(want.Bundle) {
			t.Fatalf("%s does not round-trip", d.Reference())
		}
	}
	if _, err := registry.Lookup(ctx, repo, "sha256:"+strings.Repeat("00", 32)); !errors.Is(err, evidence.ErrNotFound) {
		t.Fatalf("other fingerprint: want a miss, got %v", err)
	}
}

// What goes through the registry still verifies against the policy key, and
// an attestation signed by a key the policy does not list is a verification
// failure, not a miss.
func TestPushFetchVerify(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/orders-aval"
	ctx := context.Background()
	fp := "sha256:" + strings.Repeat("4e", 32)

	trusted, pub := signed(t, fp, "dev")
	untrusted, _ := signed(t, fp, "intruder")
	ids := []evidence.Identity{{PublicKey: pub, TrustLevel: evidence.TrustDeveloper}}
	v, err := attest.NewVerifier("", ids)
	if err != nil {
		t.Fatal(err)
	}
	opts := evidence.VerifyOptions{ExpectedFingerprint: fp, ExpectedTestID: "orders-integration", Identities: ids, MinTrustLevel: evidence.TrustDeveloper}
	for _, s := range []*attest.Signed{trusted, untrusted} {
		d, err := registry.Push(ctx, repo, fp, registry.Attestation{Envelope: s.Envelope, Bundle: s.Bundle})
		if err != nil {
			t.Fatal(err)
		}
		a, err := registry.Fetch(ctx, d)
		if err != nil {
			t.Fatal(err)
		}
		if opts.Signature, err = v.For(a.Bundle); err != nil {
			t.Fatal(err)
		}
		got, err := evidence.Verify(ctx, a.Envelope, opts)
		switch s {
		case trusted:
			if err != nil || got.Signer.KeyID != trusted.Signer.KeyID {
				t.Fatalf("trusted attestation: %+v %v", got, err)
			}
		default:
			if !errors.Is(err, evidence.ErrVerification) {
				t.Fatalf("untrusted attestation: want ErrVerification, got %v", err)
			}
		}
	}
}
