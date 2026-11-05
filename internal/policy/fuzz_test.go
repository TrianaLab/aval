package policy

import (
	"testing"

	"github.com/TrianaLab/aval/pkg/evidence"
)

// FuzzParse: policy files are user input; Parse must never panic, and an
// accepted policy never allows non-deterministic results or grants a level
// outside developer..isolated.
func FuzzParse(f *testing.F) {
	f.Add([]byte(validPolicy))
	f.Add([]byte("apiVersion: aval.trianalab.io/v1\nkind: Policy\nminTrustLevel: developer\nidentities:\n  - issuer: x\n    subjectRegexp: a)|(b\n    trustLevel: isolated\n"))
	f.Add([]byte("allowedCacheability: [non-deterministic]"))
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := Parse(data, dir)
		if err != nil {
			return
		}
		for _, c := range p.AllowedCacheability {
			if c == "non-deterministic" {
				t.Fatal("accepted non-deterministic")
			}
		}
		for _, id := range append(p.Identities, evidence.Identity{TrustLevel: p.MinTrustLevel}) {
			if id.TrustLevel < evidence.TrustDeveloper || id.TrustLevel > evidence.TrustIsolated {
				t.Fatalf("level %d accepted", id.TrustLevel)
			}
		}
	})
}
