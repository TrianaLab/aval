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

// craftRecord pushes a manifest that hangs off testID's stats anchor with
// one layer of mediaType holding body (the blob is pushed only when push is
// set), and lets edit change the manifest first.
func craftRecord(t *testing.T, repo, testID, mediaType string, body []byte, edit func(*ocispec.Manifest)) {
	t.Helper()
	_, anchorDesc := statsAnchor(testID)
	m := manifest(content.NewDescriptorFromBytes(mediaType, body))
	m.ArtifactType = evidence.ArtifactTypeRun
	m.Subject = &ocispec.Descriptor{MediaType: anchorDesc.MediaType, Digest: anchorDesc.Digest, Size: anchorDesc.Size}
	if edit != nil {
		edit(&m)
	}
	pushManifest(t, repo, m, body)
}

// distinctRecord is runRecord(id) with a distinct ciRunUrl, so equal ids
// still make distinct record manifests.
func distinctRecord(id string, n int) evidence.RunRecord {
	r := runRecord(id)
	r.CIRunURL = fmt.Sprint("https://ci.example/", n)
	return r
}

func recordJSON(rec evidence.RunRecord) []byte {
	b, _ := json.Marshal(rec)
	return b
}

// ReadRecords returns the well-formed records of every test with a stats
// anchor, or of the tests asked for, and skips and counts everything else,
// whether or not the registry has the referrers API.
func TestReadRecords(t *testing.T) {
	for _, api := range []bool{true, false} {
		t.Run(fmt.Sprintf("referrersAPI=%v", api), func(t *testing.T) {
			ctx := context.Background()
			repo := serve(t, api, nil)
			for i, id := range []string{"unit", "unit", "e2e"} {
				if _, err := Record(ctx, repo, distinctRecord(id, i)); err != nil {
					t.Fatal(err)
				}
			}
			inconsistent := runRecord("unit")
			inconsistent.Reused = true // on a miss: a reuse that never happened
			craftRecord(t, repo, "unit", evidence.MediaTypeRun, []byte(`{not json`), nil)
			craftRecord(t, repo, "unit", evidence.MediaTypeRun, recordJSON(inconsistent), nil)
			craftRecord(t, repo, "unit", evidence.MediaTypeRun, recordJSON(runRecord("e2e")), nil) // another test's record
			craftRecord(t, repo, "unit", "application/json", recordJSON(runRecord("unit")), nil)   // wrong layer type
			craftRecord(t, repo, "unit", evidence.MediaTypeRun, recordJSON(runRecord("unit")), func(m *ocispec.Manifest) {
				m.Layers[0].Size = maxRecordSize + 1 // oversized, never fetched
			})
			craftRecord(t, repo, "unit", evidence.MediaTypeRun, recordJSON(runRecord("unit")), func(m *ocispec.Manifest) {
				m.Layers = append(m.Layers, m.Layers[0]) // two layers
			})
			craftRecord(t, repo, "unit", evidence.MediaTypeRun, []byte(`{}`), func(m *ocispec.Manifest) {
				m.Layers[0].Digest = digest.FromString("absent") // unreadable
			})
			// An execution and a fingerprint anchor are neither records nor tests.
			mustPush(t, repo, fpA, att("ci", nil))

			set, err := ReadRecords(ctx, repo, nil)
			if err != nil || set.Skipped != 7 || set.Truncated != "" {
				t.Fatalf("ReadRecords = %d records, %d skipped, %q, %v", len(set.Records), set.Skipped, set.Truncated, err)
			}
			var ids []string
			for _, r := range set.Records {
				ids = append(ids, r.TestID)
			}
			if slices.Sort(ids); !slices.Equal(ids, []string{"e2e", "unit", "unit"}) {
				t.Fatalf("records for %v", ids)
			}
			if set, err := ReadRecords(ctx, repo, []string{"e2e"}); err != nil || len(set.Records) != 1 || set.Skipped != 0 {
				t.Fatalf("--test e2e: %+v %v", set, err)
			}
			if set, err := ReadRecords(ctx, repo, []string{"absent"}); err != nil || len(set.Records) != 0 || set.Skipped != 0 {
				t.Fatalf("a test without records: %+v %v", set, err)
			}
		})
	}
}

// The total record count is bounded: past it, ReadRecords stops and says so.
func TestReadRecordsCapsRecords(t *testing.T) {
	old := maxRecords
	maxRecords = 2
	t.Cleanup(func() { maxRecords = old })
	ctx := context.Background()
	repo := serve(t, true, nil)
	for i, id := range []string{"a", "b", "b"} {
		if _, err := Record(ctx, repo, distinctRecord(id, i)); err != nil {
			t.Fatal(err)
		}
	}
	set, err := ReadRecords(ctx, repo, nil)
	if err != nil || len(set.Records) != 2 || set.Truncated != "more than 2 run records" {
		t.Fatalf("ReadRecords = %d records, %q, %v", len(set.Records), set.Truncated, err)
	}
	// Once truncated, later tests are not listed at all.
	set, err = ReadRecords(ctx, repo, []string{"b", "a", "c"})
	if err != nil || len(set.Records) != 2 || set.Records[0].TestID != "b" || set.Truncated == "" {
		t.Fatalf("ReadRecords(b, a, c) = %+v, %v", set, err)
	}
}

// stats-* tags sort after the sha256-* tags of every fingerprint: the tag
// listing starts after them (last=stats) and stops past the prefix, so many
// sha256-* tags never push the stats tags beyond the page bound.
func TestReadRecordsSkipsFingerprintTags(t *testing.T) {
	ctx := context.Background()
	tags := []string{StatsTag("unit"), "v1"}
	for i := range 2 * maxReferrerPages {
		tags = append(tags, fmt.Sprintf("sha256-%04d", i))
	}
	slices.Sort(tags)
	pages := 0
	// One tag per page with a next link, honouring last as the spec says.
	repo := serve(t, true, func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/tags/list") {
			return false
		}
		pages++
		rest := tags
		if last := r.URL.Query().Get("last"); last != "" {
			rest = tags[len(tags):]
			if i := slices.IndexFunc(tags, func(t string) bool { return t > last }); i >= 0 {
				rest = tags[i:]
			}
		}
		if len(rest) > 1 {
			w.Header().Set("Link", fmt.Sprintf(`<%s?last=%s>; rel="next"`, r.URL.Path, rest[0]))
		}
		_ = json.NewEncoder(w).Encode(map[string][]string{"tags": rest[:min(1, len(rest))]})
		return true
	})
	if _, err := Record(ctx, repo, runRecord("unit")); err != nil {
		t.Fatal(err)
	}
	set, err := ReadRecords(ctx, repo, nil)
	if err != nil || set.Truncated != "" || len(set.Records) != 1 || pages != 2 {
		t.Fatalf("ReadRecords = %+v, %v after %d tag pages", set, err, pages)
	}
}

// A registry that keeps answering pages with a next link ends the tag or
// referrers listing after maxReferrerPages requests, as a truncation.
func TestReadRecordsCapsPages(t *testing.T) {
	for _, listing := range []string{"/tags/list", "/referrers/"} {
		t.Run(listing, func(t *testing.T) {
			ctx := context.Background()
			pages, armed := 0, false
			repo := serve(t, true, func(w http.ResponseWriter, r *http.Request) bool {
				if !armed || !strings.Contains(r.URL.Path, listing) {
					return false
				}
				pages++
				w.Header().Set("Link", fmt.Sprintf(`<%s?last=%d>; rel="next"`, r.URL.Path, pages))
				if listing == "/tags/list" {
					_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {StatsTag("unit")}})
					return true
				}
				w.Header().Set("Content-Type", ocispec.MediaTypeImageIndex)
				_ = json.NewEncoder(w).Encode(ocispec.Index{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageIndex, Manifests: []ocispec.Descriptor{}})
				return true
			})
			if _, err := Record(ctx, repo, runRecord("unit")); err != nil {
				t.Fatal(err)
			}
			armed = true
			set, err := ReadRecords(ctx, repo, nil)
			if err != nil || !strings.Contains(set.Truncated, fmt.Sprintf("more than %d pages", maxReferrerPages)) || pages > maxReferrerPages {
				t.Fatalf("ReadRecords = %+v, %v after %d pages", set, err, pages)
			}
			// The tests found before the tag bound are still read.
			if listing == "/tags/list" && len(set.Records) != 1 {
				t.Fatalf("ReadRecords read %d records, want the 1 of stats-unit", len(set.Records))
			}
		})
	}
}

// A deadline that passes while records are fetched is an error, never
// records counted as skipped.
func TestReadRecordsContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	armed := false
	repo := serve(t, true, func(w http.ResponseWriter, r *http.Request) bool {
		if armed && strings.Contains(r.URL.Path, "/manifests/") {
			cancel() // after the listing, on the first record fetch
		}
		return false
	})
	for i := range 2 {
		if _, err := Record(ctx, repo, distinctRecord("unit", i)); err != nil {
			t.Fatal(err)
		}
	}
	armed = true
	set, err := ReadRecords(ctx, repo, []string{"unit"})
	if !errors.Is(err, context.Canceled) || set.Skipped != 0 {
		t.Fatalf("ReadRecords = %+v, %v; want context.Canceled and nothing skipped", set, err)
	}
}

// A listing that fails for any other reason is an error, never empty
// statistics.
func TestReadRecordsErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := ReadRecords(ctx, "localhost:5000/x:tag", nil); err == nil {
		t.Error("ReadRecords must reject a repository with a tag")
	}
	for listing, ids := range map[string][]string{"/tags/list": nil, "/referrers/": {"unit"}} {
		repo := serve(t, true, func(w http.ResponseWriter, r *http.Request) bool {
			if strings.Contains(r.URL.Path, listing) {
				http.Error(w, "denied", http.StatusForbidden)
				return true
			}
			return false
		})
		if _, err := ReadRecords(ctx, repo, ids); err == nil {
			t.Errorf("%s fails: want an error", listing)
		}
	}
}

// readRecord fetches only a sha256 digest, and an absent or non-JSON
// manifest is an error (ReadRecords counts it as skipped).
func TestReadRecordErrors(t *testing.T) {
	notJSON := "not json"
	r, _ := newRepository(serve(t, true, on("/manifests/"+digest.FromString(notJSON).String(), body(notJSON))))
	for _, d := range []digest.Digest{"sha512:" + digest.Digest(strings.Repeat("a", 128)), "nonsense", digest.FromString("absent"), digest.FromString(notJSON)} {
		if _, err := readRecord(context.Background(), r, d, ""); err == nil {
			t.Errorf("%s: accepted", d)
		}
	}
}
