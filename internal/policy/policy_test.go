package policy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/pkg/evidence"
)

func TestVerifyOptions(t *testing.T) {
	ids := []evidence.Identity{{Issuer: "https://issuer.example", SubjectRegexp: "^ci$", TrustLevel: evidence.TrustRunner}}
	p := &Policy{Identities: ids, MinTrustLevel: evidence.TrustRunner, AllowedCacheability: []string{"external-dependencies"}}
	o := p.VerifyOptions("orders-integration", "sha256:abc")
	if o.ExpectedTestID != "orders-integration" || o.ExpectedFingerprint != "sha256:abc" ||
		len(o.Identities) != 1 || o.Identities[0] != ids[0] || o.MinTrustLevel != evidence.TrustRunner ||
		len(o.AllowedCacheability) != 1 || o.AllowedCacheability[0] != "external-dependencies" || o.Signature != nil {
		t.Fatalf("VerifyOptions did not carry the policy: %+v", o)
	}
}

const validPolicy = `apiVersion: aval.trianalab.io/v1
kind: Policy
trustedRoot: sigstore/trusted_root.json
minTrustLevel: runner
identities:
  - issuer: https://token.actions.githubusercontent.com
    subjectRegexp: https://github\.com/acme/orders/\.github/workflows/ci\.yaml@refs/heads/main
    trustLevel: runner
  - publicKey: keys/dev.pub
    trustLevel: developer
allowedCacheability: [external-dependencies]
`

// writePolicy writes a policy and the developer key it references.
func writePolicy(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err := os.MkdirAll(filepath.Join(dir, "keys"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keys", "dev.pub"), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keys", "bad.pub"), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keys", "big.pub"), make([]byte, maxKeySize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	path := writePolicy(t, validPolicy)
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if p.MinTrustLevel != evidence.TrustRunner || p.TrustedRoot != filepath.Join(dir, "sigstore", "trusted_root.json") ||
		len(p.Identities) != 2 || len(p.AllowedCacheability) != 1 {
		t.Fatalf("unexpected policy: %+v", p)
	}
	ci, dev := p.Identities[0], p.Identities[1]
	if ci.Issuer != "https://token.actions.githubusercontent.com" || ci.TrustLevel != evidence.TrustRunner || ci.PublicKey != "" {
		t.Fatalf("keyless identity: %+v", ci)
	}
	if _, err := evidence.PublicKeyID(dev.PublicKey); err != nil || dev.TrustLevel != evidence.TrustDeveloper || dev.Issuer != "" {
		t.Fatalf("key identity: %+v %v", dev, err)
	}
	// Absolute paths are kept as they are; no trustedRoot means public-good.
	abs := strings.Replace(validPolicy, "keys/dev.pub", filepath.Join(dir, "keys", "dev.pub"), 1)
	abs = strings.Replace(abs, "trustedRoot: sigstore/trusted_root.json\n", "", 1)
	p, err = Load(writePolicy(t, abs))
	if err != nil || p.TrustedRoot != "" {
		t.Fatalf("absolute key path: %+v %v", p, err)
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("missing file accepted")
	}
	cases := map[string]struct{ from, to, want string }{
		"not yaml":                    {"kind: Policy", "kind: [", "decoding policy"},
		"unknown field":               {"kind: Policy", "kind: Policy\nidentitys: []", "decoding policy"},
		"two documents":               {"kind: Policy", "kind: Policy\n---\nkind: Policy", "multiple YAML documents"},
		"wrong apiVersion":            {"aval.trianalab.io/v1", "aval.trianalab.io/v2", "apiVersion"},
		"pre-freeze apiVersion":       {"aval.trianalab.io/v1", "aval.dev/v1alpha1", "apiVersion"},
		"wrong kind":                  {"kind: Policy", "kind: Test", "kind"},
		"no minTrustLevel":            {"minTrustLevel: runner\n", "", "minTrustLevel"},
		"numeric minTrustLevel":       {"minTrustLevel: runner", "minTrustLevel: 2", "minTrustLevel"},
		"unknown minTrustLevel":       {"minTrustLevel: runner", "minTrustLevel: unknown", "minTrustLevel"},
		"identity without level":      {"    trustLevel: developer\n", "", "trustLevel"},
		"identity with unknown level": {"trustLevel: developer", "trustLevel: admin", "trustLevel"},
		"key and issuer":              {"  - publicKey: keys/dev.pub", "  - publicKey: keys/dev.pub\n    issuer: https://x", "cannot be combined"},
		"missing key file":            {"keys/dev.pub", "keys/none.pub", "publicKey"},
		"bad key file":                {"keys/dev.pub", "keys/bad.pub", "publicKey keys/bad.pub"},
		"key file too large":          {"keys/dev.pub", "keys/big.pub", "larger than"},
		"key path is a directory":     {"keys/dev.pub", "keys", "publicKey"},
		"no issuer":                   {"  - issuer: https://token.actions.githubusercontent.com\n    subjectRegexp:", "  - subjectRegexp:", "issuer and subjectRegexp"},
		"no subjectRegexp":            {"    subjectRegexp: https://github", "    x: https://github", "decoding policy"},
		"invalid regexp":              {`ci\.yaml@refs/heads/main`, `ci(`, "subjectRegexp"},
		"regexp escaping the anchors": {`https://github\.com/acme/orders/\.github/workflows/ci\.yaml@refs/heads/main`, `a)|(b`, "subjectRegexp"},
		"regexp too deep when anchored": {`https://github\.com/acme/orders/\.github/workflows/ci\.yaml@refs/heads/main`,
			strings.Repeat("(", 999) + "a" + strings.Repeat(")", 999), "subjectRegexp"},
		// Invariant 7: the file itself is rejected, not only the attestation.
		"non-deterministic allowed": {"[external-dependencies]", "[non-deterministic]", "never reusable"},
		"unknown cacheability":      {"[external-dependencies]", "[sometimes]", "unknown class"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			body := strings.Replace(validPolicy, tc.from, tc.to, 1)
			if body == validPolicy {
				t.Fatalf("case does not change the policy: %q", tc.from)
			}
			p, err := Load(writePolicy(t, body))
			if p != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load = %+v, %v; want error mentioning %q", p, err, tc.want)
			}
		})
	}
	empty := strings.Replace(validPolicy, validPolicy[strings.Index(validPolicy, "identities:"):strings.Index(validPolicy, "allowedCacheability")], "identities: []\n", 1)
	if _, err := Load(writePolicy(t, empty)); err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Fatalf("empty identities: %v", err)
	}
}
