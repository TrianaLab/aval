package policy

import (
	"errors"
	"testing"

	"github.com/TrianaLab/aval/pkg/evidence"
)

func TestVerifyOptions(t *testing.T) {
	ids := []evidence.Identity{{Issuer: "https://issuer.example", SubjectRegexp: "^ci$", TrustLevel: evidence.TrustRunner}}
	p := &Policy{Identities: ids, MinTrustLevel: evidence.TrustRunner, AllowedCacheability: []string{"external-dependencies"}}
	o := p.VerifyOptions("orders-integration", "sha256:abc")
	if o.ExpectedTestID != "orders-integration" || o.ExpectedFingerprint != "sha256:abc" ||
		len(o.Identities) != 1 || o.Identities[0] != ids[0] || o.MinTrustLevel != evidence.TrustRunner ||
		len(o.AllowedCacheability) != 1 || o.AllowedCacheability[0] != "external-dependencies" {
		t.Fatalf("VerifyOptions did not carry the policy: %+v", o)
	}
}

// Milestone-2 stub: until the format exists no policy may load, so nothing is trusted.
func TestLoadIsNotImplemented(t *testing.T) {
	p, err := Load("policy.yaml")
	if p != nil || !errors.Is(err, evidence.ErrNotImplemented) {
		t.Fatalf("Load = %v, %v; want nil, ErrNotImplemented", p, err)
	}
}
