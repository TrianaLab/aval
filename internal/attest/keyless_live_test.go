//go:build sigstore_live

// Live keyless signing against Sigstore public good, run only by the
// sigstore-keyless CI job (`go test -tags sigstore_live`). It needs a GitHub
// Actions OIDC token (id-token: write), so it is not part of `make ci`; every
// other test uses in-process fakes.
package attest

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/TrianaLab/aval/pkg/evidence"
)

// githubIDToken fetches a GitHub Actions OIDC token for Fulcio. Vendor
// specific on purpose: it lives in the test, not in attest.
func githubIDToken(t *testing.T) string {
	t.Helper()
	reqURL, reqTok := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"), os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	if reqURL == "" || reqTok == "" {
		t.Fatal("no GitHub Actions OIDC token: run in a job with `permissions: id-token: write`")
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, reqURL+"&audience=sigstore", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+reqTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Value == "" {
		t.Fatalf("OIDC token response (%s): %v", resp.Status, err)
	}
	return body.Value
}

func TestKeylessSigningLive(t *testing.T) {
	out, err := Sign(context.Background(), statement(t), SignOptions{IDToken: githubIDToken(t)})
	if err != nil {
		t.Fatal(err)
	}
	const issuer = "https://token.actions.githubusercontent.com"
	if out.Signer.Issuer != issuer || out.Signer.Subject == "" {
		t.Fatalf("signer must be the workflow identity: %+v", out.Signer)
	}
	t.Logf("signed as %s (%s)", out.Signer.Subject, out.Signer.Issuer)

	// The bundle verifies against the public-good trusted root: Fulcio
	// chain, SCT, Rekor inclusion and the workflow identity.
	tr, err := root.FetchTrustedRoot()
	if err != nil {
		t.Fatal(err)
	}
	v, err := verify.NewVerifier(tr, verify.WithSignedCertificateTimestamps(1), verify.WithTransparencyLog(1), verify.WithIntegratedTimestamps(1))
	if err != nil {
		t.Fatal(err)
	}
	var b bundle.Bundle
	if err := b.UnmarshalJSON(out.Bundle); err != nil {
		t.Fatal(err)
	}
	id, err := verify.NewShortCertificateIdentity(issuer, "", "", `^https://github\.com/TrianaLab/aval/`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(&b, verify.NewPolicy(verify.WithoutArtifactUnsafe(), verify.WithCertificateIdentity(id))); err != nil {
		t.Fatalf("bundle does not verify: %v", err)
	}

	// The envelope round-trips through evidence.Parse.
	var st evidence.Statement
	if err := json.Unmarshal(out.Envelope.Payload, &st); err != nil {
		t.Fatal(err)
	}
	if _, err := evidence.Parse(st.Predicate); err != nil {
		t.Fatal(err)
	}
}
