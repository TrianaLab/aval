package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"

	"github.com/TrianaLab/aval/pkg/evidence"
)

var outputFiles = []Output{{Path: "coverage.xml", Data: []byte("<coverage/>")}, {Path: "reports/junit.xml", Data: []byte("<testsuites/>")}}

func mustPushOutputs(t *testing.T, repo string, files []Output) *evidence.Outputs {
	t.Helper()
	o, err := PushOutputs(context.Background(), repo, fpA, files)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// Outputs round-trip byte for byte in both registry flavours, and an outputs
// manifest is never a lookup candidate: alone it is a miss, beside an
// attestation only the attestation is listed.
func TestOutputsRoundTrip(t *testing.T) {
	for _, api := range []bool{true, false} {
		t.Run(fmt.Sprintf("referrersAPI=%v", api), func(t *testing.T) {
			ctx := context.Background()
			repo := serve(t, api, nil)
			o := mustPushOutputs(t, repo, outputFiles)
			if len(o.Files) != 2 || o.Files[1].Path != "reports/junit.xml" || o.Files[0].Size != int64(len("<coverage/>")) {
				t.Fatalf("outputs %+v", o)
			}
			if _, err := Lookup(ctx, repo, fpA); !errors.Is(err, evidence.ErrNotFound) {
				t.Fatalf("outputs manifest alone: want a miss, got %v", err)
			}
			d := mustPush(t, repo, fpA, att("ci", nil))
			if got, err := Lookup(ctx, repo, fpA); err != nil || !slices.Equal(got, []Descriptor{d}) {
				t.Fatalf("Lookup = %+v %v, want only the attestation", got, err)
			}
			got, err := FetchOutputs(ctx, repo, o)
			if err != nil || !slices.EqualFunc(got, outputFiles, func(a, b Output) bool { return a.Path == b.Path && string(a.Data) == string(b.Data) }) {
				t.Fatalf("FetchOutputs = %+v %v", got, err)
			}
		})
	}
}

// Outputs manifests do not count toward the candidate cap: a fingerprint
// with exactly maxCandidates attestations and an outputs manifest still
// lists them all.
func TestOutputsDoNotCountTowardCap(t *testing.T) {
	repo := serve(t, true, func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.Contains(r.URL.Path, "/referrers/") {
			return false
		}
		idx := ocispec.Index{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageIndex}
		for i := range maxCandidates + 8 {
			at := evidence.ArtifactType
			if i >= maxCandidates {
				at = evidence.ArtifactTypeOutputs
			}
			idx.Manifests = append(idx.Manifests, ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromString(fmt.Sprint(i)), ArtifactType: at})
		}
		w.Header().Set("Content-Type", ocispec.MediaTypeImageIndex)
		_ = json.NewEncoder(w).Encode(idx)
		return true
	})
	mustPush(t, repo, fpA, att("ci", nil))
	if got, err := Lookup(context.Background(), repo, fpA); err != nil || len(got) != maxCandidates {
		t.Fatalf("Lookup = %d candidates, %v", len(got), err)
	}
}

// Every way the stored outputs can differ from what the signed predicate
// lists is a verification failure, never a miss or a restore.
func TestFetchOutputsTampering(t *testing.T) {
	ctx := context.Background()
	coverage := content.NewDescriptorFromBytes(evidence.MediaTypeOutput, outputFiles[0].Data)
	cases := map[string]func(t *testing.T, repo string, o *evidence.Outputs, hook *func(http.ResponseWriter, *http.Request) bool){
		"changed blob": func(_ *testing.T, _ string, _ *evidence.Outputs, hook *func(http.ResponseWriter, *http.Request) bool) {
			*hook = on("/blobs/"+coverage.Digest.String(), body("<COVERAGE/>"))
		},
		"deleted blob": func(_ *testing.T, _ string, _ *evidence.Outputs, hook *func(http.ResponseWriter, *http.Request) bool) {
			*hook = on("/blobs/"+coverage.Digest.String(), func(w http.ResponseWriter) bool { w.WriteHeader(http.StatusNotFound); return true })
		},
		"deleted manifest": func(_ *testing.T, _ string, o *evidence.Outputs, hook *func(http.ResponseWriter, *http.Request) bool) {
			*hook = on("/manifests/"+o.Manifest, func(w http.ResponseWriter) bool { w.WriteHeader(http.StatusNotFound); return true })
		},
		"swapped manifest": func(t *testing.T, repo string, o *evidence.Outputs, _ *func(http.ResponseWriter, *http.Request) bool) {
			o.Manifest = mustPushOutputs(t, repo, []Output{{Path: "coverage.xml", Data: []byte("x")}, outputFiles[1]}).Manifest
		},
		"missing layer": func(t *testing.T, repo string, o *evidence.Outputs, _ *func(http.ResponseWriter, *http.Request) bool) {
			o.Manifest = mustPushOutputs(t, repo, outputFiles[:1]).Manifest
		},
		"extra layer": func(t *testing.T, repo string, o *evidence.Outputs, _ *func(http.ResponseWriter, *http.Request) bool) {
			o.Manifest = mustPushOutputs(t, repo, append(slices.Clone(outputFiles), Output{Path: "extra", Data: []byte("x")})).Manifest
		},
		"renamed layer": func(_ *testing.T, _ string, o *evidence.Outputs, _ *func(http.ResponseWriter, *http.Request) bool) {
			o.Files[0].Path = "other.xml"
		},
		"not an outputs manifest": func(t *testing.T, repo string, o *evidence.Outputs, _ *func(http.ResponseWriter, *http.Request) bool) {
			o.Manifest = mustPush(t, repo, fpA, att("ci", nil)).Digest
		},
		"bad manifest digest": func(_ *testing.T, _ string, o *evidence.Outputs, _ *func(http.ResponseWriter, *http.Request) bool) {
			o.Manifest = "sha256:x"
		},
		"file over the cap": func(_ *testing.T, _ string, o *evidence.Outputs, _ *func(http.ResponseWriter, *http.Request) bool) {
			o.Files[0].Size = MaxOutputSize + 1
		},
		"total over the cap": func(_ *testing.T, _ string, o *evidence.Outputs, _ *func(http.ResponseWriter, *http.Request) bool) {
			o.Files[0].Size, o.Files[1].Size = MaxOutputSize, MaxOutputTotal-MaxOutputSize+1
		},
		"too many files": func(_ *testing.T, _ string, o *evidence.Outputs, _ *func(http.ResponseWriter, *http.Request) bool) {
			o.Files = make([]evidence.OutputFile, MaxOutputFiles+1)
		},
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			var hook func(http.ResponseWriter, *http.Request) bool
			repo := serve(t, true, func(w http.ResponseWriter, r *http.Request) bool { return hook != nil && hook(w, r) })
			o := mustPushOutputs(t, repo, outputFiles)
			tamper(t, repo, o, &hook)
			got, err := FetchOutputs(ctx, repo, o)
			if !errors.Is(err, evidence.ErrVerification) || errors.Is(err, evidence.ErrNotFound) || got != nil {
				t.Fatalf("got %v %v, want a verification failure", got, err)
			}
		})
	}
}

// A registry that fails is an error (the test runs), not a verification
// failure.
func TestFetchOutputsTransportErrors(t *testing.T) {
	ctx := context.Background()
	coverage := content.NewDescriptorFromBytes(evidence.MediaTypeOutput, outputFiles[0].Data)
	for name, suffix := range map[string]string{"manifest": "/manifests/", "blob": "/blobs/" + coverage.Digest.String()} {
		t.Run(name, func(t *testing.T) {
			armed := false
			repo := serve(t, true, func(w http.ResponseWriter, r *http.Request) bool {
				return armed && r.Method == http.MethodGet && strings.Contains(r.URL.Path, suffix) && fail(w)
			})
			o := mustPushOutputs(t, repo, outputFiles)
			armed = true
			if _, err := FetchOutputs(ctx, repo, o); err == nil || errors.Is(err, evidence.ErrVerification) || errors.Is(err, evidence.ErrNotFound) {
				t.Fatalf("want a plain error, got %v", err)
			}
		})
	}
	if _, err := FetchOutputs(ctx, "Bad Repo", &evidence.Outputs{Manifest: fpA}); err == nil || errors.Is(err, evidence.ErrVerification) {
		t.Fatalf("bad repository: %v", err)
	}
}

func TestPushOutputsErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := PushOutputs(ctx, "reg.example/r", "sha256:x", outputFiles); err == nil {
		t.Fatal("bad fingerprint accepted")
	}
	if _, err := PushOutputs(ctx, "Bad Repo", fpA, outputFiles); err == nil {
		t.Fatal("bad repository accepted")
	}
	for name, path := range map[string]string{"blob": "/blobs/uploads/", "config": emptyJSON.Digest.Encoded(), "anchor": "/manifests/" + Tag(fpA), "manifest": "/manifests/sha256:"} {
		t.Run(name, func(t *testing.T) {
			repo := serve(t, true, func(w http.ResponseWriter, r *http.Request) bool {
				return (r.Method == http.MethodPost || r.Method == http.MethodPut) && strings.Contains(r.URL.String(), path) && fail(w)
			})
			if _, err := PushOutputs(ctx, repo, fpA, outputFiles); err == nil {
				t.Fatal("push error swallowed")
			}
		})
	}
}
