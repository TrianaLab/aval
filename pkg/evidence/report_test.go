package evidence

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// The verify report changes additively only (AGENTS.md invariant 10): the
// golden file shows any change to the marshalled shape as a diff.
func TestReportGolden(t *testing.T) {
	r := Report{
		Version:     ReportVersion,
		Outcome:     OutcomeVerified,
		TestID:      "orders-integration",
		Fingerprint: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Reference:   "ghcr.io/acme/orders-aval@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Result:      ResultPassed,
		Signer:      &Identity{Issuer: "https://token.actions.githubusercontent.com", SubjectRegexp: "^https://github.com/acme/orders/", TrustLevel: TrustRunner, Subject: "https://github.com/acme/orders/.github/workflows/ci.yaml@refs/heads/main"},
		Rejected: []Rejection{{
			Reference: "ghcr.io/acme/orders-aval@sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			Outcome:   OutcomeVerificationFailed,
			Reason:    "verification failed: signer not allowed by policy",
		}},
	}
	got, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/report.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != strings.TrimSpace(string(want)) {
		t.Fatalf("report shape changed:\n%s", got)
	}
	var back Report
	if err := json.Unmarshal(want, &back); err != nil || !reflect.DeepEqual(back, r) {
		t.Fatalf("round trip: %+v %v", back, err)
	}
}

// A report without rejected candidates keeps its pre-existing shape, and an
// older document without the field still parses.
func TestReportWithoutRejected(t *testing.T) {
	old := `{"version":"` + ReportVersion + `","outcome":"miss","testId":"t","fingerprint":"sha256:aa","reason":"no attestation"}`
	var r Report
	if err := json.Unmarshal([]byte(old), &r); err != nil || r.Rejected != nil {
		t.Fatalf("%+v %v", r, err)
	}
	got, err := json.Marshal(r)
	if err != nil || string(got) != old {
		t.Fatalf("got %s %v", got, err)
	}
}
