// Package registry publishes and locates execution attestations in an OCI
// registry using ORAS. There is no Aval server or database: the registry is
// the only index.
//
// Layout (see docs/artifact-format.md): for each fingerprint, a deterministic
// anchor manifest tagged "sha256-<fingerprint hex>"; every execution with that
// fingerprint is an OCI 1.1 artifact manifest whose subject is the anchor, so
// the referrers API lists all of them. Pushes only add referrers: a later push
// never replaces an earlier attestation, and the trust policy picks among the
// candidates. Everything is consumed by digest.
package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"

	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"

	"github.com/TrianaLab/aval/pkg/evidence"
)

const (
	// MediaTypeSigstoreBundle is the media type of the optional Sigstore
	// bundle layer (Sigstore standard).
	MediaTypeSigstoreBundle = "application/vnd.dev.sigstore.bundle.v0.3+json"
	// AnnotationFingerprint records the fingerprint on the anchor and on
	// every attestation manifest. Informational: Verify checks the
	// fingerprint inside the signed predicate.
	AnnotationFingerprint = "io.trianalab.aval.fingerprint"
	// maxBlobSize caps every manifest and layer read from the registry,
	// which is untrusted input.
	maxBlobSize = 4 << 20
)

var fingerprintRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Descriptor points at a manifest by digest.
type Descriptor struct {
	// Repository is e.g. "ghcr.io/acme/orders-aval".
	Repository string
	// Digest is the manifest digest, "sha256:<hex>".
	Digest string
}

// Reference returns the immutable "repo@digest" form.
func (d Descriptor) Reference() string { return d.Repository + "@" + d.Digest }

// Attestation is what one execution stores in the registry.
type Attestation struct {
	// Envelope is the signed DSSE envelope. Required.
	Envelope *evidence.Envelope
	// Bundle is the Sigstore bundle JSON with the verification material
	// (certificate or key hint, transparency log entry). Optional.
	Bundle []byte
}

// Tag returns the lookup tag for a fingerprint ("sha256:<hex>").
func Tag(fingerprint string) string { return strings.Replace(fingerprint, ":", "-", 1) }

// Seam for tests: the credential store read by newRepository.
var newCredentialStore = func() (credentials.Store, error) {
	return credentials.NewStoreFromDocker(credentials.StoreOptions{})
}

// newRepository opens a repository ("host/name", no tag or digest) with the
// Docker credential configuration. Loopback registries use plain HTTP, the
// same default Docker and containerd apply.
func newRepository(repo string) (*remote.Repository, error) {
	r, err := remote.NewRepository(repo)
	if err != nil {
		return nil, err
	}
	if r.Reference.Reference != "" {
		return nil, fmt.Errorf("repository %q must not carry a tag or digest", repo)
	}
	store, err := newCredentialStore()
	if err != nil {
		return nil, fmt.Errorf("loading registry credentials: %w", err)
	}
	r.Client = &auth.Client{Client: auth.DefaultClient.Client, Cache: auth.NewCache(), Credential: credentials.Credential(store)}
	r.PlainHTTP = isLoopback(r.Reference.Host())
	return r, nil
}

func isLoopback(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

// emptyJSON is the OCI empty descriptor, used as config and as the anchor's
// only layer.
var emptyJSON = ocispec.DescriptorEmptyJSON

// anchor returns the deterministic anchor manifest for a fingerprint and its
// descriptor. Its digest depends on the fingerprint alone.
func anchor(fingerprint string) ([]byte, ocispec.Descriptor) {
	m := ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: evidence.ArtifactTypeFingerprint,
		Config:       emptyJSON,
		Layers:       []ocispec.Descriptor{emptyJSON},
		Annotations:  map[string]string{AnnotationFingerprint: fingerprint},
	}
	raw, _ := json.Marshal(m) // ponytail: plain structs and strings, cannot fail
	return raw, content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, raw)
}

func checkFingerprint(fingerprint string) error {
	if !fingerprintRe.MatchString(fingerprint) {
		return fmt.Errorf("fingerprint %q is not sha256:<64 hex>", fingerprint)
	}
	return nil
}

// Push uploads the attestation as an artifact that refers to the
// fingerprint's anchor and returns its digest. It never moves or replaces an
// earlier attestation: pushing the same bytes again is a no-op, anything else
// becomes one more referrer.
func Push(ctx context.Context, repo, fingerprint string, a Attestation) (Descriptor, error) {
	if err := checkFingerprint(fingerprint); err != nil {
		return Descriptor{}, fmt.Errorf("registry.Push: %w", err)
	}
	if a.Envelope == nil {
		return Descriptor{}, errors.New("registry.Push: no envelope")
	}
	r, err := newRepository(repo)
	if err != nil {
		return Descriptor{}, fmt.Errorf("registry.Push: %w", err)
	}
	d, err := push(ctx, r, fingerprint, a)
	if err != nil {
		return Descriptor{}, fmt.Errorf("registry.Push %s: %w", repo, err)
	}
	return Descriptor{Repository: repo, Digest: d.Digest.String()}, nil
}

func push(ctx context.Context, r *remote.Repository, fingerprint string, a Attestation) (ocispec.Descriptor, error) {
	if _, err := pushBlob(ctx, r, ocispec.MediaTypeEmptyJSON, emptyJSON.Data); err != nil {
		return ocispec.Descriptor{}, err
	}
	env, _ := json.Marshal(a.Envelope) // ponytail: strings and byte slices, cannot fail
	envDesc, err := pushBlob(ctx, r, evidence.MediaTypeDSSE, env)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	layers := []ocispec.Descriptor{envDesc}
	if len(a.Bundle) > 0 {
		bundleDesc, err := pushBlob(ctx, r, MediaTypeSigstoreBundle, a.Bundle)
		if err != nil {
			return ocispec.Descriptor{}, err
		}
		layers = append(layers, bundleDesc)
	}
	anchorRaw, anchorDesc := anchor(fingerprint)
	// Re-tagging the deterministic anchor is idempotent and repairs a
	// moved tag.
	if _, err := oras.TagBytes(ctx, r, ocispec.MediaTypeImageManifest, anchorRaw, Tag(fingerprint)); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("tagging anchor: %w", err)
	}
	m := ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: evidence.ArtifactType,
		Config:       emptyJSON,
		Layers:       layers,
		Subject:      &ocispec.Descriptor{MediaType: anchorDesc.MediaType, Digest: anchorDesc.Digest, Size: anchorDesc.Size},
		Annotations:  map[string]string{AnnotationFingerprint: fingerprint},
	}
	raw, _ := json.Marshal(m) // ponytail: plain structs and strings, cannot fail
	d := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, raw)
	if err := r.Push(ctx, d, bytes.NewReader(raw)); err != nil && !errors.Is(err, errdef.ErrAlreadyExists) {
		return ocispec.Descriptor{}, fmt.Errorf("pushing manifest: %w", err)
	}
	return d, nil
}

func pushBlob(ctx context.Context, r *remote.Repository, mediaType string, data []byte) (ocispec.Descriptor, error) {
	d := content.NewDescriptorFromBytes(mediaType, data)
	if err := r.Push(ctx, d, bytes.NewReader(data)); err != nil && !errors.Is(err, errdef.ErrAlreadyExists) {
		return ocispec.Descriptor{}, fmt.Errorf("pushing %s: %w", mediaType, err)
	}
	return ocispec.Descriptor{MediaType: d.MediaType, Digest: d.Digest, Size: d.Size}, nil
}

// Lookup returns every attestation manifest for the fingerprint, by digest,
// sorted by digest. It resolves the fingerprint tag, checks that it points at
// the expected anchor, and lists the anchor's referrers. It returns an error
// wrapping evidence.ErrNotFound on a miss and evidence.ErrVerification when
// the tag points anywhere else. Callers verify each candidate and let the
// trust policy pick; they fetch by digest, never by tag.
func Lookup(ctx context.Context, repo, fingerprint string) ([]Descriptor, error) {
	if err := checkFingerprint(fingerprint); err != nil {
		return nil, fmt.Errorf("registry.Lookup: %w", err)
	}
	r, err := newRepository(repo)
	if err != nil {
		return nil, fmt.Errorf("registry.Lookup: %w", err)
	}
	ds, err := lookup(ctx, r, fingerprint)
	if err != nil {
		return nil, fmt.Errorf("registry.Lookup %s: %w", repo, err)
	}
	out := make([]Descriptor, 0, len(ds))
	for _, d := range ds {
		out = append(out, Descriptor{Repository: repo, Digest: d})
	}
	return out, nil
}

func lookup(ctx context.Context, r *remote.Repository, fingerprint string) ([]string, error) {
	_, want := anchor(fingerprint)
	got, err := r.Resolve(ctx, Tag(fingerprint))
	if errors.Is(err, errdef.ErrNotFound) {
		return nil, evidence.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", Tag(fingerprint), err)
	}
	if got.Digest != want.Digest {
		return nil, &evidence.VerificationError{Reason: fmt.Sprintf("tag %s points at %s, not the fingerprint anchor %s", Tag(fingerprint), got.Digest, want.Digest)}
	}
	var ds []string
	err = r.Referrers(ctx, want, evidence.ArtifactType, func(refs []ocispec.Descriptor) error {
		for _, d := range refs {
			// The referrers list is registry data; Fetch checks the
			// manifest itself.
			if d.MediaType == ocispec.MediaTypeImageManifest && d.Digest.Validate() == nil && d.Digest.Algorithm() == digest.SHA256 {
				ds = append(ds, d.Digest.String())
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing referrers: %w", err)
	}
	if len(ds) == 0 {
		return nil, evidence.ErrNotFound
	}
	slices.Sort(ds)
	return slices.Compact(ds), nil
}

// Fetch downloads the attestation for a descriptor, by digest. A manifest
// that is not a well-formed Aval attestation is an error wrapping
// evidence.ErrVerification.
func Fetch(ctx context.Context, d Descriptor) (*Attestation, error) {
	dg, err := digest.Parse(d.Digest)
	if err != nil {
		return nil, fmt.Errorf("registry.Fetch: %w", err)
	}
	r, err := newRepository(d.Repository)
	if err != nil {
		return nil, fmt.Errorf("registry.Fetch: %w", err)
	}
	a, err := fetch(ctx, r, dg)
	if err != nil {
		return nil, fmt.Errorf("registry.Fetch %s: %w", d.Reference(), err)
	}
	return a, nil
}

func fetch(ctx context.Context, r *remote.Repository, dg digest.Digest) (*Attestation, error) {
	// FetchBytes by digest checks the content against it.
	_, raw, err := oras.FetchBytes(ctx, r.Manifests(), dg.String(), oras.FetchBytesOptions{MaxBytes: maxBlobSize})
	if err != nil {
		return nil, fmt.Errorf("fetching manifest: %w", err)
	}
	var m ocispec.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, malformed("manifest: %v", err)
	}
	if m.MediaType != ocispec.MediaTypeImageManifest || m.ArtifactType != evidence.ArtifactType {
		return nil, malformed("not an Aval attestation (mediaType %q, artifactType %q)", m.MediaType, m.ArtifactType)
	}
	a := &Attestation{}
	for _, l := range m.Layers {
		switch {
		case l.MediaType == evidence.MediaTypeDSSE && a.Envelope == nil:
			raw, err := fetchBlob(ctx, r, l)
			if err != nil {
				return nil, err
			}
			a.Envelope = &evidence.Envelope{}
			if err := json.Unmarshal(raw, a.Envelope); err != nil {
				return nil, malformed("envelope: %v", err)
			}
		case l.MediaType == MediaTypeSigstoreBundle && a.Bundle == nil:
			if a.Bundle, err = fetchBlob(ctx, r, l); err != nil {
				return nil, err
			}
		default:
			return nil, malformed("unexpected or repeated layer %q", l.MediaType)
		}
	}
	if a.Envelope == nil {
		return nil, malformed("no DSSE envelope layer")
	}
	return a, nil
}

func fetchBlob(ctx context.Context, r *remote.Repository, l ocispec.Descriptor) ([]byte, error) {
	if l.Size < 0 || l.Size > maxBlobSize {
		return nil, malformed("layer %s size %d out of range", l.Digest, l.Size)
	}
	if err := l.Digest.Validate(); err != nil {
		return nil, malformed("layer digest: %v", err)
	}
	rc, err := r.Blobs().Fetch(ctx, l)
	if err != nil {
		return nil, fmt.Errorf("fetching layer %s: %w", l.Digest, err)
	}
	defer func() { _ = rc.Close() }()
	// ReadAll checks size and digest against the descriptor.
	raw, err := content.ReadAll(rc, l)
	if errors.Is(err, content.ErrMismatchedDigest) || errors.Is(err, content.ErrTrailingData) {
		return nil, malformed("layer %s: %v", l.Digest, err)
	}
	if err != nil {
		return nil, fmt.Errorf("reading layer %s: %w", l.Digest, err)
	}
	return raw, nil
}

func malformed(format string, args ...any) error {
	return &evidence.VerificationError{Reason: "malformed attestation: " + fmt.Sprintf(format, args...)}
}
