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
	"io"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

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
	// maxCandidates caps the attestations listed for one fingerprint, so
	// anyone with push access cannot make verify fetch an unbounded number
	// of manifests. Over the cap lookup fails with a plain error (outcome
	// error: run the test).
	maxCandidates = 64
	// maxReferrerPages caps the referrers pages one listing requests. oras
	// follows Link headers without a limit and skips empty pages, so a
	// buggy or hostile registry could otherwise keep a lookup looping.
	maxReferrerPages = 64
	// pushConfirmAttempts bounds how often Push re-puts its manifest and
	// lists the referrers before giving up on seeing it there.
	pushConfirmAttempts = 3
)

// pushConfirmDelay is the wait between push confirmation attempts; a
// variable so tests do not sleep.
var pushConfirmDelay = time.Second

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

// One credential store and one auth (token) cache per process, shared by
// every Lookup, Fetch and Push. A failure to load the credentials is not
// cached, so a later call retries.
var (
	clientMu sync.Mutex
	client   remote.Client
)

func sharedClient() (remote.Client, error) {
	clientMu.Lock()
	defer clientMu.Unlock()
	if client == nil {
		store, err := newCredentialStore()
		if err != nil {
			return nil, fmt.Errorf("loading registry credentials: %w", err)
		}
		client = pagedClient{&auth.Client{Client: auth.DefaultClient.Client, Cache: auth.NewCache(), Credential: credentials.Credential(store)}}
	}
	return client, nil
}

// pageCount is the context key of the referrers page counter of one listing.
type pageCount struct{}

// pagedClient fails a referrers request once the listing in its context has
// requested maxReferrerPages pages.
type pagedClient struct{ remote.Client }

func (c pagedClient) Do(req *http.Request) (*http.Response, error) {
	if n, ok := req.Context().Value(pageCount{}).(*int); ok && strings.Contains(req.URL.Path, "/referrers/") {
		if *n++; *n > maxReferrerPages {
			return nil, fmt.Errorf("more than %d referrers pages", maxReferrerPages)
		}
	}
	return c.Client.Do(req)
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
	if r.Client, err = sharedClient(); err != nil {
		return nil, err
	}
	r.PlainHTTP = isLoopback(r.Reference.Host())
	// ponytail: in referrers-tag fallback mode a dangling old referrers index
	// is harmless; deleting it needs DELETE rights that CI tokens often lack.
	r.SkipReferrersGC = true
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
// becomes one more referrer. Push returns only once the new digest is listed
// among the anchor's referrers: a concurrent push can drop it from a
// registry's referrers tag index, so Push puts the manifest again and
// re-checks, a few times, before it fails.
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
	var listed error
	for attempt := range pushConfirmAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ocispec.Descriptor{}, fmt.Errorf("confirming manifest %s: %w (last listing: %v)", d.Digest, ctx.Err(), listed)
			case <-time.After(pushConfirmDelay):
			}
		}
		if err := r.Push(ctx, d, bytes.NewReader(raw)); err != nil && !errors.Is(err, errdef.ErrAlreadyExists) {
			return ocispec.Descriptor{}, fmt.Errorf("pushing manifest: %w", err)
		}
		var ds []string
		if ds, listed = lookup(ctx, r, fingerprint); listed == nil && slices.Contains(ds, d.Digest.String()) {
			return d, nil
		}
	}
	return ocispec.Descriptor{}, fmt.Errorf("manifest %s is not listed among the referrers of %s after %d attempts (last listing: %v)", d.Digest, Tag(fingerprint), pushConfirmAttempts, listed)
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
	err = r.Referrers(context.WithValue(ctx, pageCount{}, new(int)), want, evidence.ArtifactType, func(refs []ocispec.Descriptor) error {
		for _, d := range refs {
			// Registry data: every entry with our artifactType is a
			// candidate. Fetch rejects anything that is not a sha256 image
			// manifest as malformed, so it reaches the report as a
			// verification failure instead of vanishing into a miss.
			ds = append(ds, d.Digest.String())
		}
		if len(ds) > maxCandidates {
			return fmt.Errorf("more than %d attestations for fingerprint %s", maxCandidates, fingerprint)
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

// Fetch downloads the attestation for a descriptor, by digest. A digest that
// is not sha256, or a manifest that is oversized, does not match its digest
// or is not a well-formed Aval attestation, is an error wrapping
// evidence.ErrVerification. A manifest the registry reports as not found (a
// listed attestation that was deleted) is an error wrapping
// evidence.ErrNotFound: the candidate is absent.
func Fetch(ctx context.Context, d Descriptor) (*Attestation, error) {
	dg, err := digest.Parse(d.Digest)
	if err == nil && dg.Algorithm() != digest.SHA256 {
		err = fmt.Errorf("algorithm %s", dg.Algorithm())
	}
	if err != nil {
		return nil, fmt.Errorf("registry.Fetch %s: %w", d.Reference(), malformed("digest: %v", err))
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
	raw, err := fetchManifest(ctx, r, dg)
	if err != nil {
		return nil, err
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

// fetchManifest GETs the manifest bytes by digest and checks size and digest
// itself. oras reports a body that does not match the digest as an untyped
// error, and a mismatch is a tampered candidate (verification failure), not a
// transport error. The body is read whatever its media type, so an index
// reaches the attestation checks and is rejected there.
func fetchManifest(ctx context.Context, r *remote.Repository, dg digest.Digest) ([]byte, error) {
	scheme := "https"
	if r.PlainHTTP {
		scheme = "http"
	}
	url := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, r.Reference.Host(), r.Reference.Repository, dg)
	// ponytail: host, repository and digest were validated by newRepository
	// and digest.Parse, so the request cannot fail to build.
	req, _ := http.NewRequestWithContext(auth.AppendRepositoryScope(ctx, r.Reference, auth.ActionPull), http.MethodGet, url, nil)
	req.Header.Set("Accept", strings.Join([]string{ocispec.MediaTypeImageManifest, ocispec.MediaTypeImageIndex}, ", "))
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching manifest: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("fetching manifest %s: %s: %w", dg, resp.Status, evidence.ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching manifest %s: %s", dg, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBlobSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading manifest %s: %w", dg, err)
	}
	if len(raw) > maxBlobSize {
		return nil, malformed("manifest %s larger than %d bytes", dg, maxBlobSize)
	}
	if got := digest.FromBytes(raw); got != dg {
		return nil, malformed("manifest %s: content has digest %s", dg, got)
	}
	return raw, nil
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
