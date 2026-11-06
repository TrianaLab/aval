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
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/TrianaLab/aval/pkg/evidence"
)

func runRecord(id string) evidence.RunRecord {
	return evidence.RunRecord{Version: evidence.RunRecordVersion, TestID: id, Fingerprint: fpA, Outcome: evidence.OutcomeMiss, Result: evidence.ResultPassed}
}

// A record hangs off the test's stats anchor, carries the record JSON, and
// can be read back through the stats anchor's referrers.
func TestRecordLayout(t *testing.T) {
	for _, api := range []bool{true, false} {
		t.Run(fmt.Sprintf("referrersAPI=%v", api), func(t *testing.T) {
			ctx := context.Background()
			repo := serve(t, api, nil)
			d, err := Record(ctx, repo, runRecord("unit"))
			if err != nil || d.Repository != repo {
				t.Fatalf("Record = %+v %v", d, err)
			}
			r, _ := newRepository(repo)
			_, want := statsAnchor("unit")
			if got, err := r.Resolve(ctx, StatsTag("unit")); err != nil || got.Digest != want.Digest {
				t.Fatalf("stats tag resolves to %v %v, want %s", got.Digest, err, want.Digest)
			}
			var refs []string
			if err := r.Referrers(ctx, want, evidence.ArtifactTypeRun, func(ds []ocispec.Descriptor) error {
				for _, x := range ds {
					refs = append(refs, x.Digest.String())
				}
				return nil
			}); err != nil || !slices.Equal(refs, []string{d.Digest}) {
				t.Fatalf("stats referrers %v %v, want [%s]", refs, err, d.Digest)
			}
			if rec := readBack(t, r, digest.Digest(d.Digest), want.Digest); rec.TestID != "unit" || rec.Outcome != evidence.OutcomeMiss {
				t.Fatalf("record %+v", rec)
			}
		})
	}
}

// readBack fetches a record manifest by digest, checks its shape and subject,
// and parses its layer.
func readBack(t *testing.T, r *remote.Repository, d, subject digest.Digest) *evidence.RunRecord {
	t.Helper()
	ctx := context.Background()
	raw, err := fetchManifest(ctx, r, d)
	if err != nil {
		t.Fatal(err)
	}
	var m ocispec.Manifest
	_ = json.Unmarshal(raw, &m)
	if m.ArtifactType != evidence.ArtifactTypeRun || len(m.Layers) != 1 || m.Layers[0].MediaType != evidence.MediaTypeRun || m.Subject.Digest != subject {
		t.Fatalf("record manifest %s", raw)
	}
	body, err := content.FetchAll(ctx, r.Blobs(), m.Layers[0])
	if err != nil {
		t.Fatal(err)
	}
	rec, err := evidence.ParseRunRecord(body)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestStatsAnchorIsDeterministicAndApart(t *testing.T) {
	a1, d1 := statsAnchor("unit")
	a2, d2 := statsAnchor("unit")
	_, d3 := statsAnchor("e2e")
	_, fp := anchor("unit")
	if string(a1) != string(a2) || d1.Digest != d2.Digest || d1.Digest == d3.Digest || d1.Digest == fp.Digest {
		t.Fatal("the stats anchor must depend on the test id alone and differ from a fingerprint anchor")
	}
	if StatsTag("unit") != "stats-unit" || strings.HasPrefix(StatsTag("sha256"), "sha256-") {
		t.Fatalf("StatsTag = %q", StatsTag("unit"))
	}
}

// False hit (#87): records never reach run or verify. Neither real records
// nor a crafted record that names the fingerprint anchor as its subject is a
// candidate, and they do not count toward the 64-candidate cap.
func TestRecordsAreNeverCandidates(t *testing.T) {
	for _, api := range []bool{true, false} {
		t.Run(fmt.Sprintf("referrersAPI=%v", api), func(t *testing.T) {
			ctx := context.Background()
			repo := serve(t, api, nil)
			// Only records: every lookup is a miss.
			if _, err := Record(ctx, repo, runRecord("unit")); err != nil {
				t.Fatal(err)
			}
			_, fpAnchor := anchor(fpA)
			crafted := manifest(content.NewDescriptorFromBytes(evidence.MediaTypeRun, []byte(`{}`)))
			crafted.ArtifactType = evidence.ArtifactTypeRun
			crafted.Subject = &ocispec.Descriptor{MediaType: fpAnchor.MediaType, Digest: fpAnchor.Digest, Size: fpAnchor.Size}
			raw, _ := anchor(fpA)
			pushManifest(t, repo, raw, emptyJSON.Data)
			r, _ := newRepository(repo)
			if err := r.Tag(ctx, fpAnchor, Tag(fpA)); err != nil {
				t.Fatal(err)
			}
			rec := pushManifest(t, repo, crafted, []byte(`{}`))
			if _, err := Lookup(ctx, repo, fpA); !errors.Is(err, evidence.ErrNotFound) || errors.Is(err, evidence.ErrVerification) {
				t.Fatalf("only records: want a miss, got %v", err)
			}
			// A full candidate list plus records stays within the cap.
			var want []Descriptor
			for i := range maxCandidates {
				want = append(want, mustPush(t, repo, fpA, att(fmt.Sprint(i), nil)))
				if _, err := Record(ctx, repo, runRecord("unit")); err != nil {
					t.Fatal(err)
				}
			}
			slices.SortFunc(want, func(x, y Descriptor) int { return strings.Compare(x.Digest, y.Digest) })
			got, err := Lookup(ctx, repo, fpA)
			if err != nil || !slices.Equal(got, want) || slices.Contains(got, rec) {
				t.Fatalf("Lookup = %d candidates, %v; want the %d executions only", len(got), err, len(want))
			}
		})
	}
}

func TestRecordErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := Record(ctx, "localhost:5000/x:tag", runRecord("unit")); err == nil || strings.HasPrefix(err.Error(), "registry.") {
		t.Errorf("Record must reject a repository with a tag without a package prefix: %v", err)
	}
	recRaw, _ := json.Marshal(runRecord("unit"))
	digestIs := func(r *http.Request, d digest.Digest) bool {
		return r.Method == http.MethodPut && r.URL.Query().Get("digest") == d.String()
	}
	cases := map[string]struct {
		rec   evidence.RunRecord
		match func(*http.Request) bool
	}{
		"config blob": {runRecord("unit"), func(r *http.Request) bool { return digestIs(r, emptyJSON.Digest) }},
		"record blob": {runRecord("unit"), func(r *http.Request) bool { return digestIs(r, digest.FromBytes(recRaw)) }},
		"stats tag": {runRecord("unit"), func(r *http.Request) bool {
			return r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/manifests/"+StatsTag("unit"))
		}},
		"manifest": {runRecord("unit"), func(r *http.Request) bool {
			return r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/sha256:")
		}},
		"invalid test id": {runRecord("Not/A:tag"), func(*http.Request) bool { return false }},
		"write denied":    {runRecord("unit"), func(r *http.Request) bool { return r.Method != http.MethodGet && r.Method != http.MethodHead }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repo := serve(t, true, func(w http.ResponseWriter, r *http.Request) bool {
				if tc.match(r) {
					http.Error(w, "denied", http.StatusForbidden)
					return true
				}
				return false
			})
			if _, err := Record(ctx, repo, tc.rec); err == nil || strings.HasPrefix(err.Error(), "registry.") {
				t.Fatalf("want an error without a package prefix, got %v", err)
			}
		})
	}
}
