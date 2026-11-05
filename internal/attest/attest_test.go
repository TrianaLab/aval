package attest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/TrianaLab/aval/pkg/evidence"
)

func predicate() *evidence.Predicate {
	start := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	return &evidence.Predicate{
		SchemaVersion: evidence.SchemaVersion,
		Subject:       evidence.Subject{Name: "git+https://github.com/acme/orders", Digest: map[string]string{"gitCommit": "0123abcd"}},
		Test:          evidence.Test{ID: "orders-integration", Cacheability: "hermetic"},
		Result:        evidence.ResultPassed,
		Execution:     evidence.Execution{Fingerprint: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		Runner:        evidence.Runner{Identity: "ci", TrustLevel: evidence.TrustRunner},
		StartedAt:     start,
		FinishedAt:    start.Add(time.Minute),
	}
}

func TestNewStatement(t *testing.T) {
	p := predicate()
	s, err := NewStatement(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Type != evidence.StatementType || s.PredicateType != evidence.PredicateType {
		t.Fatalf("unexpected statement types: %+v", s)
	}
	if len(s.Subject) != 1 || s.Subject[0].Name != p.Subject.Name || s.Subject[0].Digest["gitCommit"] != "0123abcd" {
		t.Fatalf("statement subject must be the tested revision, got %+v", s.Subject)
	}
	var back evidence.Predicate
	if err := json.Unmarshal(s.Predicate, &back); err != nil || back.Test.ID != p.Test.ID {
		t.Fatalf("predicate does not round-trip: %v %+v", err, back)
	}
}

func TestNewStatementRejectsBadPredicates(t *testing.T) {
	invalid := predicate()
	invalid.Test.ID = ""
	if _, err := NewStatement(invalid); err == nil {
		t.Error("invalid predicate must be rejected")
	}
	// Valid per Validate, but time.Time cannot encode years past 9999.
	unencodable := predicate()
	unencodable.StartedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	unencodable.FinishedAt = unencodable.StartedAt
	if _, err := NewStatement(unencodable); err == nil {
		t.Error("unencodable predicate must be rejected")
	}
}

// Milestone-2 stub: until signing is implemented it must never return an envelope.
func TestSignIsNotImplemented(t *testing.T) {
	env, err := Sign(context.Background(), &evidence.Statement{}, SignOptions{})
	if env != nil || !errors.Is(err, evidence.ErrNotImplemented) {
		t.Fatalf("Sign = %v, %v; want nil, ErrNotImplemented", env, err)
	}
}
