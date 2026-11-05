package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/TrianaLab/aval/pkg/evidence"
)

func TestReferenceIsByDigest(t *testing.T) {
	d := Descriptor{Repository: "ghcr.io/acme/orders-aval", Digest: "sha256:abc"}
	if got := d.Reference(); got != "ghcr.io/acme/orders-aval@sha256:abc" {
		t.Fatalf("Reference() = %q", got)
	}
}

func TestTag(t *testing.T) {
	if got := Tag("sha256:abc"); got != "sha256-abc" {
		t.Fatalf("Tag() = %q", got)
	}
}

// Milestone-2 stubs: until implemented they must never report a hit.
func TestStubsAreNotImplemented(t *testing.T) {
	ctx := context.Background()
	_, pushErr := Push(ctx, "repo", "sha256:abc", &evidence.Envelope{})
	_, lookupErr := Lookup(ctx, "repo", "sha256:abc")
	env, fetchErr := Fetch(ctx, Descriptor{})
	for name, err := range map[string]error{"Push": pushErr, "Lookup": lookupErr, "Fetch": fetchErr} {
		if !errors.Is(err, evidence.ErrNotImplemented) {
			t.Errorf("%s: got %v, want ErrNotImplemented", name, err)
		}
	}
	if env != nil {
		t.Error("Fetch must not return an envelope")
	}
}
