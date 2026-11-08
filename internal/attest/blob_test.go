package attest

import (
	"errors"
	"strings"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

func TestVerifyBlob(t *testing.T) {
	withoutSCT(t)
	vs := useTrustedRoot(t, nil)
	const sub = "https://github.com/TrianaLab/aval/.github/workflows/release.yaml@refs/tags/v1.2.3"
	blob := []byte("abc  aval_1.2.3_linux_amd64.tar.gz\n")
	e, err := vs.Sign(sub, ghIssuer, blob)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyBlob("", e, blob, ghIssuer, sub); err != nil {
		t.Fatalf("genuine signature: %v", err)
	}
	other, _ := ca.NewVirtualSigstore()
	foreign, _ := other.Sign(sub, ghIssuer, blob)
	for name, tc := range map[string]struct {
		e                     verify.SignedEntity
		blob                  []byte
		issuer, subject, want string
	}{
		"other tag":      {e, blob, ghIssuer, strings.Replace(sub, "v1.2.3", "v1.2.4", 1), "SAN"},
		"other issuer":   {e, blob, "https://accounts.google.com", sub, "issuer"},
		"other artifact": {e, []byte("tampered"), ghIssuer, sub, ""},
		"empty subject":  {e, blob, ghIssuer, "", "required"},
		"empty issuer":   {e, blob, "", sub, "required"},
		"untrusted CA":   {foreign, blob, ghIssuer, sub, ""},
	} {
		if err := verifyBlob("", tc.e, tc.blob, tc.issuer, tc.subject); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error mentioning %q", name, err, tc.want)
		}
	}
}

func TestVerifyBlobFailsClosed(t *testing.T) {
	if err := VerifyBlob("", []byte("{"), nil, ghIssuer, "s"); err == nil || !strings.Contains(err.Error(), "decoding Sigstore bundle") {
		t.Fatalf("garbage bundle: %v", err)
	}
	// A bundle that decodes still goes through verification.
	useTrustedRoot(t, nil)
	if err := VerifyBlob("", certBundle, nil, ghIssuer, "s"); err == nil || strings.Contains(err.Error(), "decoding") {
		t.Fatalf("decodable bundle: %v", err)
	}
	old := loadTrustedRoot
	t.Cleanup(func() { loadTrustedRoot = old })
	loadTrustedRoot = func(string) (root.TrustedMaterial, error) { return nil, errors.New("offline") }
	if err := verifyBlob("", nil, nil, ghIssuer, "s"); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("no trusted root: %v", err)
	}
	useTrustedRoot(t, nil)
	oldOpts := keylessOptions
	t.Cleanup(func() { keylessOptions = oldOpts })
	keylessOptions = []verify.VerifierOption{func(*verify.VerifierConfig) error { return errors.New("bad option") }}
	if err := verifyBlob("", nil, nil, ghIssuer, "s"); err == nil || !strings.Contains(err.Error(), "bad option") {
		t.Fatalf("bad verifier: %v", err)
	}
}
