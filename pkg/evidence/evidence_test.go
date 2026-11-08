package evidence

import (
	"errors"
	"strings"
	"testing"
)

const validPredicate = `{
  "schemaVersion": "v2",
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
		"old schema v1":   {`"schemaVersion": "v2"`, `"schemaVersion": "v1"`, "schemaVersion"},
		"future schema":   {`"schemaVersion": "v2"`, `"schemaVersion": "v3"`, "schemaVersion"},
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

// execution.outputs is optional: a predicate without it parses, one with it
// round-trips, and every path is relative, clean and listed once, so a
// signed predicate cannot point a restore outside the contract directory.
func TestParseOutputs(t *testing.T) {
	const d = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	with := func(outputs string) string {
		return strings.Replace(validPredicate, `"exitCode": 0`, `"exitCode": 0, "outputs": `+outputs, 1)
	}
	file := func(path string) string {
		return `{"path": "` + path + `", "digest": "` + d + `", "size": 3}`
	}
	p, err := Parse([]byte(with(`{"manifest": "` + d + `", "files": [` + file("coverage.xml") + `, ` + file("reports/junit.xml") + `]}`)))
	if err != nil {
		t.Fatal(err)
	}
	if o := p.Execution.Outputs; o == nil || o.Manifest != d || len(o.Files) != 2 || o.Files[1] != (OutputFile{Path: "reports/junit.xml", Digest: d, Size: 3}) {
		t.Fatalf("outputs = %+v", p.Execution.Outputs)
	}
	if p, _ := Parse([]byte(validPredicate)); p.Execution.Outputs != nil {
		t.Fatal("omitted outputs must stay nil")
	}
	for name, outputs := range map[string]string{
		"bad manifest":  `{"manifest": "sha256:x", "files": [` + file("a") + `]}`,
		"no files":      `{"manifest": "` + d + `", "files": []}`,
		"traversal":     `{"manifest": "` + d + `", "files": [` + file("../etc/passwd") + `]}`,
		"inner ..":      `{"manifest": "` + d + `", "files": [` + file("a/../../b") + `]}`,
		"absolute":      `{"manifest": "` + d + `", "files": [` + file("/etc/passwd") + `]}`,
		"dot":           `{"manifest": "` + d + `", "files": [` + file(".") + `]}`,
		"backslash":     `{"manifest": "` + d + `", "files": [` + file(`..\\\\x`) + `]}`,
		"control":       `{"manifest": "` + d + `", "files": [` + file(`a\nb`) + `]}`,
		"duplicate":     `{"manifest": "` + d + `", "files": [` + file("a") + `, ` + file("a") + `]}`,
		"bad digest":    `{"manifest": "` + d + `", "files": [{"path": "a", "digest": "md5:1", "size": 1}]}`,
		"negative size": `{"manifest": "` + d + `", "files": [{"path": "a", "digest": "` + d + `", "size": -1}]}`,
	} {
		if _, err := Parse([]byte(with(outputs))); err == nil || !strings.Contains(err.Error(), "execution.outputs") {
			t.Errorf("%s: got %v", name, err)
		}
	}
}
