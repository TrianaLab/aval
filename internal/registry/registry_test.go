package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	ggcr "github.com/google/go-containerregistry/pkg/registry"
	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote/credentials"

	"github.com/TrianaLab/aval/pkg/evidence"
)

func TestMain(m *testing.M) {
	// Never read the developer's Docker credentials.
	dir, err := os.MkdirTemp("", "aval-docker")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("DOCKER_CONFIG", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

const (
	fpA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fpB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// serve starts an in-process OCI registry, a stand-in for distribution or
// zot (AGENTS.md test level 2). hook, when set, may answer a
// request itself (returning true) to inject faults.
func serve(t *testing.T, referrersAPI bool, hook func(http.ResponseWriter, *http.Request) bool) string {
	t.Helper()
	h := ggcr.New(ggcr.WithReferrersSupport(referrersAPI), ggcr.Logger(log.New(io.Discard, "", 0)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hook != nil && hook(w, r) {
			return
		}
		if referrersAPI && strings.Contains(r.URL.Path, "/referrers/") {
			fixReferrers(h, w, r)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://") + "/acme/orders-aval"
}

// fixReferrers works around go-containerregistry's in-process registry,
// which reports config.mediaType as each referrer's artifactType instead of
// the manifest's artifactType field (OCI distribution spec 1.1).
func fixReferrers(h http.Handler, w http.ResponseWriter, r *http.Request) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	var idx ocispec.Index
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &idx) != nil {
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
		return
	}
	repo := strings.Split(r.URL.Path, "/referrers/")[0]
	for i, d := range idx.Manifests {
		m := httptest.NewRecorder()
		h.ServeHTTP(m, httptest.NewRequest(http.MethodGet, repo+"/manifests/"+d.Digest.String(), nil))
		var mf ocispec.Manifest
		_ = json.Unmarshal(m.Body.Bytes(), &mf)
		idx.Manifests[i].ArtifactType = mf.ArtifactType
	}
	w.Header().Set("Content-Type", ocispec.MediaTypeImageIndex)
	_ = json.NewEncoder(w).Encode(idx)
}

func att(sig string, bundle []byte) Attestation {
	return Attestation{
		Envelope: &evidence.Envelope{PayloadType: evidence.PayloadType, Payload: []byte(`{"x":1}`), Signatures: []evidence.Signature{{Sig: []byte(sig)}}},
		Bundle:   bundle,
	}
}

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

func mustPush(t *testing.T, repo, fp string, a Attestation) Descriptor {
	t.Helper()
	d, err := Push(context.Background(), repo, fp, a)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func mustFetch(t *testing.T, d Descriptor, sig string, bundle []byte) {
	t.Helper()
	a, err := Fetch(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if string(a.Envelope.Signatures[0].Sig) != sig || !bytes.Equal(a.Bundle, bundle) {
		t.Fatalf("fetched %+v", a)
	}
}

// Both registry flavours: with the OCI 1.1 referrers API, and without it
// (oras maintains the referrers tag schema, as on GHCR).
func TestPushLookupFetchRoundTrip(t *testing.T) {
	for _, api := range []bool{true, false} {
		t.Run(fmt.Sprintf("referrersAPI=%v", api), func(t *testing.T) { roundTrip(t, serve(t, api, nil)) })
	}
}

func roundTrip(t *testing.T, repo string) {
	ctx := context.Background()
	if _, err := Lookup(ctx, repo, fpA); !errors.Is(err, evidence.ErrNotFound) || errors.Is(err, evidence.ErrVerification) {
		t.Fatalf("empty registry: want a miss, got %v", err)
	}
	bundle := []byte(`{"bundle":true}`)
	first := mustPush(t, repo, fpA, att("ci", bundle))
	if first.Repository != repo || !strings.HasPrefix(first.Digest, "sha256:") {
		t.Fatalf("descriptor %+v", first)
	}
	// Pushing the same bytes again is idempotent.
	if again := mustPush(t, repo, fpA, att("ci", bundle)); again != first {
		t.Fatalf("re-push: %+v", again)
	}
	// A later push for the same fingerprint adds a candidate and never
	// replaces the first one. Another fingerprint is independent.
	second := mustPush(t, repo, fpA, att("dev", nil))
	mustPush(t, repo, fpB, att("other", nil))
	want := []Descriptor{first, second}
	slices.SortFunc(want, func(x, y Descriptor) int { return strings.Compare(x.Digest, y.Digest) })
	if got, err := Lookup(ctx, repo, fpA); err != nil || !slices.Equal(got, want) {
		t.Fatalf("Lookup = %+v %v, want %+v", got, err, want)
	}
	mustFetch(t, first, "ci", bundle)
	mustFetch(t, second, "dev", nil)
}

func TestAnchorIsDeterministic(t *testing.T) {
	a1, d1 := anchor(fpA)
	a2, d2 := anchor(fpA)
	_, d3 := anchor(fpB)
	if !bytes.Equal(a1, a2) || d1.Digest != d2.Digest || d1.Digest == d3.Digest {
		t.Fatal("anchor must depend on the fingerprint alone")
	}
}

func TestLookupAnchorWithoutReferrersIsAMiss(t *testing.T) {
	ctx := context.Background()
	repo := serve(t, true, nil)
	r, err := newRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := anchor(fpA)
	if _, err := oras.PushBytes(ctx, r, ocispec.MediaTypeEmptyJSON, emptyJSON.Data); err != nil {
		t.Fatal(err)
	}
	if _, err := oras.TagBytes(ctx, r, ocispec.MediaTypeImageManifest, raw, Tag(fpA)); err != nil {
		t.Fatal(err)
	}
	if _, err := Lookup(ctx, repo, fpA); !errors.Is(err, evidence.ErrNotFound) {
		t.Fatalf("want a miss, got %v", err)
	}
}

// A tag moved to anything but the fingerprint anchor is tampering: a
// verification failure, never a miss and never a hit.
func TestLookupMovedTagIsVerificationFailure(t *testing.T) {
	ctx := context.Background()
	repo := serve(t, true, nil)
	pushed, err := Push(ctx, repo, fpA, att("ci", nil))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := newRepository(repo)
	desc, err := r.Resolve(ctx, pushed.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Tag(ctx, desc, Tag(fpA)); err != nil {
		t.Fatal(err)
	}
	_, err = Lookup(ctx, repo, fpA)
	if !errors.Is(err, evidence.ErrVerification) || errors.Is(err, evidence.ErrNotFound) {
		t.Fatalf("want verification failure, got %v", err)
	}
	// The next push repairs the tag.
	if _, err := Push(ctx, repo, fpA, att("ci", nil)); err != nil {
		t.Fatal(err)
	}
	if got, err := Lookup(ctx, repo, fpA); err != nil || len(got) != 1 || got[0] != pushed {
		t.Fatalf("after repair: %+v %v", got, err)
	}
}

// Referrers entries are registry data: only sha256 image manifests are
// candidates.
func TestLookupSkipsForeignReferrers(t *testing.T) {
	ctx := context.Background()
	_, ad := anchor(fpA)
	var good digest.Digest
	repo := serve(t, true, func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.Contains(r.URL.Path, "/referrers/") {
			return false
		}
		idx := ocispec.Index{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageIndex, Manifests: []ocispec.Descriptor{
			{MediaType: ocispec.MediaTypeImageIndex, Digest: digest.FromString("index"), ArtifactType: evidence.ArtifactType},
			{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.SHA512.FromString("x"), ArtifactType: evidence.ArtifactType},
			{MediaType: ocispec.MediaTypeImageManifest, Digest: "sha256:zz", ArtifactType: evidence.ArtifactType},
			{MediaType: ocispec.MediaTypeImageManifest, Digest: good, ArtifactType: evidence.ArtifactType},
			{MediaType: ocispec.MediaTypeImageManifest, Digest: good, ArtifactType: evidence.ArtifactType},
		}}
		w.Header().Set("Content-Type", ocispec.MediaTypeImageIndex)
		_ = json.NewEncoder(w).Encode(idx)
		return true
	})
	pushed, err := Push(ctx, repo, fpA, att("ci", nil))
	if err != nil {
		t.Fatal(err)
	}
	good = digest.Digest(pushed.Digest)
	got, err := Lookup(ctx, repo, fpA)
	if err != nil || len(got) != 1 || got[0] != pushed {
		t.Fatalf("Lookup = %+v %v (anchor %s)", got, err, ad.Digest)
	}
}

// Referrers are untrusted registry data: too many candidates is an error
// (fail closed, run the test), never a miss, a verification failure or a hit.
func TestLookupCapsCandidates(t *testing.T) {
	ctx := context.Background()
	repo := serve(t, true, func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.Contains(r.URL.Path, "/referrers/") {
			return false
		}
		idx := ocispec.Index{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageIndex}
		for i := range maxCandidates + 1 {
			idx.Manifests = append(idx.Manifests, ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromString(fmt.Sprint(i)), ArtifactType: evidence.ArtifactType})
		}
		w.Header().Set("Content-Type", ocispec.MediaTypeImageIndex)
		_ = json.NewEncoder(w).Encode(idx)
		return true
	})
	if _, err := Push(ctx, repo, fpA, att("ci", nil)); err != nil {
		t.Fatal(err)
	}
	got, err := Lookup(ctx, repo, fpA)
	if err == nil || errors.Is(err, evidence.ErrNotFound) || errors.Is(err, evidence.ErrVerification) || got != nil {
		t.Fatalf("Lookup = %+v %v; want a plain error", got, err)
	}
}

func TestInputValidation(t *testing.T) {
	ctx := context.Background()
	if _, err := Push(ctx, "localhost:5000/x", "sha256:abc", att("ci", nil)); err == nil {
		t.Error("Push must reject a malformed fingerprint")
	}
	if _, err := Push(ctx, "localhost:5000/x", fpA, Attestation{}); err == nil {
		t.Error("Push must reject a missing envelope")
	}
	if _, err := Lookup(ctx, "localhost:5000/x", "sha256:ABC"); err == nil || errors.Is(err, evidence.ErrNotFound) {
		t.Errorf("Lookup must reject a malformed fingerprint, got %v", err)
	}
	for _, repo := range []string{"localhost:5000/x:tag", "localhost:5000/x@" + fpA, "not a repo"} {
		if _, err := Push(ctx, repo, fpA, att("ci", nil)); err == nil {
			t.Errorf("Push(%q) must fail", repo)
		}
		if _, err := Lookup(ctx, repo, fpA); err == nil {
			t.Errorf("Lookup(%q) must fail", repo)
		}
		if _, err := Fetch(ctx, Descriptor{Repository: repo, Digest: fpA}); err == nil {
			t.Errorf("Fetch(%q) must fail", repo)
		}
	}
	if _, err := Fetch(ctx, Descriptor{Repository: "localhost:5000/x", Digest: "sha256:zz"}); err == nil {
		t.Error("Fetch must reject a malformed digest")
	}
}

func TestCredentialStoreError(t *testing.T) {
	orig := newCredentialStore
	t.Cleanup(func() { newCredentialStore = orig })
	newCredentialStore = func() (credentials.Store, error) { return nil, errors.New("bad docker config") }
	if _, err := newRepository("localhost:5000/x"); err == nil || !strings.Contains(err.Error(), "bad docker config") {
		t.Fatalf("got %v", err)
	}
}

func TestIsLoopback(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost:5000": true, "localhost": true, "127.0.0.1:5000": true, "[::1]:5000": true, "127.1.2.3": true,
		"ghcr.io": false, "registry.example:5000": false, "10.0.0.1:5000": false, "localhost.evil.example": false,
	} {
		if got := isLoopback(host); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", host, got, want)
		}
	}
}

// fail injects an error the oras client does not retry (it retries 5xx).
func fail(w http.ResponseWriter) bool {
	http.Error(w, "injected", http.StatusBadRequest)
	return true
}

func TestRegistryErrorsAreNotMisses(t *testing.T) {
	ctx := context.Background()
	a := att("ci", []byte("bundle"))
	env, _ := json.Marshal(a.Envelope)
	digestIs := func(r *http.Request, d digest.Digest) bool {
		return r.Method == http.MethodPut && r.URL.Query().Get("digest") == d.String()
	}
	pushCases := map[string]func(*http.Request) bool{
		"config blob": func(r *http.Request) bool { return digestIs(r, emptyJSON.Digest) },
		"envelope":    func(r *http.Request) bool { return digestIs(r, digest.FromBytes(env)) },
		"bundle":      func(r *http.Request) bool { return digestIs(r, digest.FromString("bundle")) },
		"anchor tag": func(r *http.Request) bool {
			return r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/manifests/"+Tag(fpA))
		},
		"manifest": func(r *http.Request) bool {
			return r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/sha256:")
		},
	}
	for name, match := range pushCases {
		t.Run("push "+name, func(t *testing.T) {
			repo := serve(t, true, func(w http.ResponseWriter, r *http.Request) bool { return match(r) && fail(w) })
			if _, err := Push(ctx, repo, fpA, a); err == nil {
				t.Fatal("want an error")
			}
		})
	}
	lookupCases := map[string]func(*http.Request) bool{
		"resolve": func(r *http.Request) bool {
			return strings.HasSuffix(r.URL.Path, "/manifests/"+Tag(fpA)) && r.Method != http.MethodPut
		},
		"referrers": func(r *http.Request) bool { return strings.Contains(r.URL.Path, "/referrers/") },
	}
	for name, match := range lookupCases {
		t.Run("lookup "+name, func(t *testing.T) {
			on := false
			repo := serve(t, true, func(w http.ResponseWriter, r *http.Request) bool { return on && match(r) && fail(w) })
			if _, err := Push(ctx, repo, fpA, a); err != nil {
				t.Fatal(err)
			}
			on = true
			_, err := Lookup(ctx, repo, fpA)
			if err == nil || errors.Is(err, evidence.ErrNotFound) || errors.Is(err, evidence.ErrVerification) {
				t.Fatalf("want a plain error, got %v", err)
			}
		})
	}
}

// pushManifest stores an arbitrary manifest (and the given blobs) and returns
// its descriptor.
func pushManifest(t *testing.T, repo string, m any, blobs ...[]byte) Descriptor {
	t.Helper()
	ctx := context.Background()
	r, err := newRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range blobs {
		if _, err := oras.PushBytes(ctx, r, "application/octet-stream", b); err != nil {
			t.Fatal(err)
		}
	}
	raw, ok := m.([]byte)
	if !ok {
		raw, _ = json.Marshal(m)
	}
	d, err := oras.PushBytes(ctx, r, ocispec.MediaTypeImageManifest, raw)
	if err != nil {
		t.Fatal(err)
	}
	return Descriptor{Repository: repo, Digest: d.Digest.String()}
}

func manifest(layers ...ocispec.Descriptor) ocispec.Manifest {
	return ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageManifest,
		ArtifactType: evidence.ArtifactType, Config: emptyJSON, Layers: layers,
	}
}

// A manifest that is not a well-formed Aval attestation is a verification
// failure, not a miss and not a hit.
func TestFetchRejectsMalformedAttestations(t *testing.T) {
	ctx := context.Background()
	repo := serve(t, true, nil)
	env := []byte(`{"payloadType":"x","payload":"e30=","signatures":[]}`)
	envD := content.NewDescriptorFromBytes(evidence.MediaTypeDSSE, env)
	bundle := []byte("b")
	bundleD := content.NewDescriptorFromBytes(MediaTypeSigstoreBundle, bundle)
	notJSON := []byte("not json")
	notJSOND := content.NewDescriptorFromBytes(evidence.MediaTypeDSSE, notJSON)
	wrongType := manifest(envD)
	wrongType.ArtifactType = "application/vnd.example"
	big := envD
	big.Size = maxBlobSize + 1
	badDigest := envD
	badDigest.Digest = "sha256:zz"
	cases := map[string]Descriptor{
		"wrong type":        pushManifest(t, repo, wrongType, env),
		"no envelope":       pushManifest(t, repo, manifest(bundleD), bundle),
		"repeated envelope": pushManifest(t, repo, manifest(envD, envD), env),
		"repeated bundle":   pushManifest(t, repo, manifest(envD, bundleD, bundleD), env, bundle),
		"unknown layer":     pushManifest(t, repo, manifest(envD, emptyJSON), env),
		"bad envelope":      pushManifest(t, repo, manifest(notJSOND), notJSON),
		"oversized layer":   pushManifest(t, repo, manifest(big), env),
		"bad layer digest":  pushManifest(t, repo, manifest(badDigest), env),
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Fetch(ctx, d); !errors.Is(err, evidence.ErrVerification) {
				t.Fatalf("want verification failure, got %v", err)
			}
		})
	}
	// The registry may hold bytes it did not validate.
	bad := []byte(`{"mediaType": 1}`)
	badRepo := serve(t, true, func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
		_, _ = w.Write(bad)
		return true
	})
	if _, err := Fetch(ctx, Descriptor{Repository: badRepo, Digest: digest.FromBytes(bad).String()}); !errors.Is(err, evidence.ErrVerification) {
		t.Fatalf("not json: want verification failure, got %v", err)
	}
	// Sanity: the same building blocks, well formed, fetch fine.
	if _, err := Fetch(ctx, pushManifest(t, repo, manifest(envD, bundleD), env, bundle)); err != nil {
		t.Fatal(err)
	}
}

// on answers the GET requests whose path ends in suffix with respond.
func on(suffix string, respond func(http.ResponseWriter) bool) func(http.ResponseWriter, *http.Request) bool {
	return func(w http.ResponseWriter, r *http.Request) bool {
		return r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, suffix) && respond(w)
	}
}

// body answers without Content-Length or digest header, so only
// content.ReadAll can catch a bad body.
func body(b string) func(http.ResponseWriter) bool {
	return func(w http.ResponseWriter) bool {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte(b))
		return true
	}
}

func TestFetchTransportErrors(t *testing.T) {
	env := []byte(`{"payloadType":"x","payload":"e30=","signatures":[]}`)
	envD := content.NewDescriptorFromBytes(evidence.MediaTypeDSSE, env)
	bundle := []byte("bundle-bytes")
	bundleD := content.NewDescriptorFromBytes(MediaTypeSigstoreBundle, bundle)
	m := manifest(envD, bundleD)
	raw, _ := json.Marshal(m)
	cases := map[string]struct {
		hook   func(http.ResponseWriter, *http.Request) bool
		verErr bool
	}{
		"missing manifest":      {on("/manifests/"+digest.FromBytes(raw).String(), fail), false},
		"missing envelope blob": {on("/blobs/"+envD.Digest.String(), fail), false},
		"missing bundle blob":   {on("/blobs/"+bundleD.Digest.String(), fail), false},
		"tampered blob":         {on("/blobs/"+bundleD.Digest.String(), body("BUNDLE-BYTES")), true},
		"truncated blob":        {on("/blobs/"+bundleD.Digest.String(), body("bun")), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			armed := false
			repo := serve(t, true, func(w http.ResponseWriter, r *http.Request) bool { return armed && tc.hook(w, r) })
			d := pushManifest(t, repo, m, env, bundle)
			armed = true
			_, err := Fetch(context.Background(), d)
			if err == nil || errors.Is(err, evidence.ErrNotFound) || errors.Is(err, evidence.ErrVerification) != tc.verErr {
				t.Fatalf("got %v, want verification failure = %v", err, tc.verErr)
			}
		})
	}
}
