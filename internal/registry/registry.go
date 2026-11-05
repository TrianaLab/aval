// Package registry publishes and locates execution attestations in an OCI
// registry using ORAS. There is no Aval server or database: the registry is
// the only index.
//
// Layout (see docs/artifact-format.md): one OCI 1.1 artifact manifest per
// execution, artifactType evidence.ArtifactType, tagged
// "sha256-<fingerprint hex>" for lookup and always consumed by digest.
package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/TrianaLab/aval/pkg/evidence"
)

// Descriptor points at a manifest by digest.
type Descriptor struct {
	// Repository is e.g. "ghcr.io/acme/orders-aval".
	Repository string
	// Digest is the manifest digest, "sha256:<hex>".
	Digest string
}

// Reference returns the immutable "repo@digest" form.
func (d Descriptor) Reference() string { return d.Repository + "@" + d.Digest }

// Tag returns the lookup tag for a fingerprint ("sha256:<hex>").
func Tag(fingerprint string) string { return strings.Replace(fingerprint, ":", "-", 1) }

// Push uploads the signed envelope as an artifact tagged with the
// fingerprint and returns its digest.
//
// TODO(milestone-2): implement with oras-go (oras.land/oras-go/v2), setting
// the manifest subject to the tested artifact when it lives in the same
// registry, so OCI 1.1 referrers link evidence to it.
func Push(ctx context.Context, repo, fingerprint string, env *evidence.Envelope) (Descriptor, error) {
	return Descriptor{}, fmt.Errorf("registry.Push: %w", evidence.ErrNotImplemented)
}

// Lookup resolves the fingerprint tag to a digest. It returns an error
// wrapping evidence.ErrNotFound on a miss. Callers must then fetch by digest,
// never by tag, so the tag cannot be swapped between resolve and verify.
//
// TODO(milestone-2): implement with oras-go; if several attestations share a
// fingerprint, list referrers and let policy pick.
func Lookup(ctx context.Context, repo, fingerprint string) (Descriptor, error) {
	return Descriptor{}, fmt.Errorf("registry.Lookup: %w", evidence.ErrNotImplemented)
}

// Fetch downloads the envelope for a descriptor, by digest.
//
// TODO(milestone-2): implement with oras-go.
func Fetch(ctx context.Context, d Descriptor) (*evidence.Envelope, error) {
	return nil, fmt.Errorf("registry.Fetch: %w", evidence.ErrNotImplemented)
}
