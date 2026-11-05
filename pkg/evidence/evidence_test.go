package evidence

import (
	"errors"
	"strings"
	"testing"
)

const validPredicate = `{
  "schemaVersion": "v1",
  "subject": {"name": "git+https://github.com/acme/orders", "digest": {"gitCommit": "0123abcd"}},
  "test": {"id": "orders-integration", "cacheability": "controlled-environment"},
  "result": "passed",
  "execution": {
    "fingerprint": "sha256:` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `",
    "fingerprintVersion": "aval-fingerprint/v1",
    "environment": {"os": "linux", "arch": "amd64", "toolchain.go": "1.25"},
    "exitCode": 0
  },
  "runner": {"identity": "https://github.com/acme/orders/.github/workflows/ci.yaml@refs/heads/main", "trustLevel": 2},
  "startedAt": "2026-11-05T10:00:00Z",
  "finishedAt": "2026-11-05T10:04:00Z",
  "annotations": {"com.example.team": "orders"}
}`

func TestParse(t *testing.T) {
	p, err := Parse([]byte(validPredicate))
	if err != nil {
		t.Fatal(err)
	}
	if p.Runner.TrustLevel != TrustRunner || p.Annotations["com.example.team"] != "orders" {
		t.Fatalf("unexpected predicate: %+v", p)
	}
	bad := strings.Replace(validPredicate, `"result": "passed"`, `"result": "skipped"`, 1)
	if _, err := Parse([]byte(bad)); err == nil {
		t.Fatal("unknown result must be rejected")
	}
}

func TestVerificationErrorIs(t *testing.T) {
	var err error = &VerificationError{Reason: "untrusted signer"}
	if !errors.Is(err, ErrVerification) || errors.Is(err, ErrNotFound) {
		t.Fatal("VerificationError must wrap ErrVerification only")
	}
}
