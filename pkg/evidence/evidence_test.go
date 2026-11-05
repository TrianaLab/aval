package evidence

import (
	"context"
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

func TestParseRejectsInvalidPredicates(t *testing.T) {
	cases := map[string]struct{ from, to, wantErr string }{
		"not json":        {"{", "[", "decoding predicate"},
		"schema version":  {`"schemaVersion": "v1"`, `"schemaVersion": "v2"`, "schemaVersion"},
		"no subject":      {`"name": "git+https://github.com/acme/orders", `, `"name": "", `, "subject"},
		"no test id":      {`"id": "orders-integration"`, `"id": ""`, "test.id"},
		"bad fingerprint": {`"fingerprint": "sha256:`, `"fingerprint": "md5:`, "execution.fingerprint"},
		"no identity":     {`"identity": "https://`, `"identity": "", "x": "`, "runner.identity"},
		"inverted times":  {`"finishedAt": "2026-11-05T10:04:00Z"`, `"finishedAt": "2026-11-05T09:00:00Z"`, "startedAt/finishedAt"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(strings.Replace(validPredicate, tc.from, tc.to, 1)))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestTrustLevelString(t *testing.T) {
	want := map[TrustLevel]string{
		TrustUnknown: "unknown", TrustDeveloper: "developer", TrustRunner: "runner",
		TrustIsolated: "isolated", TrustLevel(42): "unknown",
	}
	for l, s := range want {
		if got := l.String(); got != s {
			t.Errorf("TrustLevel(%d).String() = %q, want %q", int(l), got, s)
		}
	}
}

func TestVerificationErrorMessage(t *testing.T) {
	err := &VerificationError{Reason: "untrusted signer"}
	if got := err.Error(); got != "attestation verification failed: untrusted signer" {
		t.Fatalf("Error() = %q", got)
	}
}

// Milestone-2 stub: until Verify is implemented it must never report success.
func TestVerifyIsNotImplemented(t *testing.T) {
	v, err := Verify(context.Background(), &Envelope{}, VerifyOptions{})
	if v != nil || !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Verify = %v, %v; want nil, ErrNotImplemented", v, err)
	}
}

func TestMatchIdentity(t *testing.T) {
	const gh = "https://token.actions.githubusercontent.com"
	const ci = "https://github.com/acme/orders/.github/workflows/ci.yaml@refs/heads/main"
	ids := []Identity{
		{Issuer: gh, SubjectRegexp: `https://github\.com/acme/orders/.*`, TrustLevel: TrustDeveloper},
		{Issuer: gh, SubjectRegexp: `https://github\.com/acme/orders/\.github/workflows/ci\.yaml@refs/heads/main`, TrustLevel: TrustRunner},
		{Issuer: "https://accounts.google.com", SubjectRegexp: `.*`, TrustLevel: TrustIsolated},
	}
	cases := []struct {
		name, issuer, subject string
		want                  TrustLevel
		ok                    bool
	}{
		{"highest matching level wins", gh, ci, TrustRunner, true},
		{"lower entry still matches other workflows", gh, "https://github.com/acme/orders/.github/workflows/dev.yaml@refs/heads/x", TrustDeveloper, true},
		// False hits: none of these may be granted a level.
		{"regexp is anchored at the start", gh, "https://evil.example/https://github.com/acme/orders/x", TrustUnknown, false},
		{"regexp is anchored at the end", gh, ci + "-fork", TrustDeveloper, true},
		{"other issuer, same subject", "https://evil.example", ci, TrustUnknown, false},
		{"empty issuer never matches", "", ci, TrustUnknown, false},
		{"issuer must be equal, not a prefix", gh + "/", ci, TrustUnknown, false},
	}
	for _, c := range cases {
		id, ok := MatchIdentity(ids, c.issuer, c.subject)
		if ok != c.ok || id.TrustLevel != c.want {
			t.Errorf("%s: got %v %v, want %v %v", c.name, id.TrustLevel, ok, c.want, c.ok)
		}
	}
	// The runner-level entry is anchored: a suffix only matches the wildcard
	// developer entry, never the runner one.
	if id, _ := MatchIdentity(ids[1:2], gh, ci+"-fork"); id.TrustLevel != TrustUnknown {
		t.Errorf("suffix matched an anchored identity: %+v", id)
	}
	bad := []Identity{
		{Issuer: gh, SubjectRegexp: `(`, TrustLevel: TrustIsolated},
		// Unbalanced: wrapping it naively as ^(?:a)|(b)$ would match "a..." unanchored.
		{Issuer: gh, SubjectRegexp: `https://x)|(y`, TrustLevel: TrustIsolated},
		// Compiles alone, but wrapping exceeds the nesting limit: must not panic.
		{Issuer: gh, SubjectRegexp: strings.Repeat("(", 999) + "a" + strings.Repeat(")", 999), TrustLevel: TrustIsolated},
	}
	if id, ok := MatchIdentity(bad, gh, "https://x-anything"); ok {
		t.Errorf("invalid regexp must match nothing, got %+v", id)
	}
	if _, ok := MatchIdentity(nil, gh, ci); ok {
		t.Error("no identities must accept nothing")
	}
}
