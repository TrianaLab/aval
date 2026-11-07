package registry

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/TrianaLab/aval/pkg/evidence"
)

// Read bounds of ReadRecords, besides maxReferrerPages per listing. Registry
// data is untrusted, and pruning old records is up to the operator
// (docs/artifact-format.md "Pruning run records").
const (
	// maxRecordSize caps one run record layer; a real record is well under
	// 1 KiB.
	maxRecordSize = 64 << 10
)

// maxRecords caps the referrer descriptors one ReadRecords call lists
// (records and anything the registry returns despite the artifactType
// filter); a variable so tests do not push a thousand records.
var maxRecords = 1000

// errBound stops a listing at a read bound, or once it is past what it needs.
var errBound = errors.New("read bound")

// RecordSet is what ReadRecords read.
type RecordSet struct {
	// Records are the well-formed, consistent records read.
	Records []evidence.RunRecord
	// Skipped counts records listed but not returned: unreadable,
	// oversized, malformed, inconsistent (evidence.ParseRunRecord), or for
	// another test than their stats anchor.
	Skipped int
	// Truncated names the read bound that was hit, empty when none: the
	// records cover only what was listed before it.
	Truncated string
}

// ReadRecords lists and reads the run records of the given tests, or of
// every test with a stats anchor (a "stats-" tag) when testIDs is empty. It
// writes nothing. A record that cannot be read or parsed is skipped and
// counted, never an error; a cancelled or expired ctx is an error, so a
// deadline never passes as skipped records. Listing stops after maxReferrerPages pages per
// listing or maxRecords records in total, and the set says so; only a
// listing that fails for another reason is an error.
func ReadRecords(ctx context.Context, repo string, testIDs []string) (RecordSet, error) {
	r, err := newRepository(repo)
	if err != nil {
		return RecordSet{}, fmt.Errorf("registry.ReadRecords: %w", err)
	}
	var set RecordSet
	if len(testIDs) == 0 {
		if testIDs, err = statsTests(ctx, r, &set); err != nil {
			return RecordSet{}, fmt.Errorf("registry.ReadRecords %s: listing tags: %w", repo, err)
		}
	}
	listed := 0
	for _, id := range testIDs {
		if listed > maxRecords {
			break
		}
		_, anchorDesc := statsAnchor(id)
		var ds []digest.Digest
		err := r.Referrers(context.WithValue(ctx, pageCount{}, new(int)), anchorDesc, evidence.ArtifactTypeRun, func(refs []ocispec.Descriptor) error {
			for _, d := range refs {
				if listed++; listed > maxRecords {
					set.Truncated = cmp.Or(set.Truncated, fmt.Sprintf("more than %d run records", maxRecords))
					return errBound
				}
				ds = append(ds, d.Digest)
			}
			return nil
		})
		if err := bounded(err, &set, StatsTag(id)); err != nil {
			return RecordSet{}, fmt.Errorf("registry.ReadRecords %s: listing the records of %s: %w", repo, StatsTag(id), err)
		}
		for _, d := range ds {
			rec, err := readRecord(ctx, r, d, anchorDesc.Digest)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return RecordSet{}, fmt.Errorf("registry.ReadRecords %s: reading the records of %s: %w", repo, StatsTag(id), ctxErr)
			}
			if err != nil || rec.TestID != id {
				set.Skipped++
			} else {
				set.Records = append(set.Records, *rec)
			}
		}
	}
	return set, nil
}

// bounded returns err unless it is a read bound, which it notes in set.
func bounded(err error, set *RecordSet, listing string) error {
	switch {
	case errors.Is(err, errBound):
		return nil
	case errors.Is(err, errPages):
		set.Truncated = cmp.Or(set.Truncated, fmt.Sprintf("more than %d pages listing %s", maxReferrerPages, listing))
		return nil
	}
	return err
}

// statsTests lists the test ids of the repository's stats anchors, sorted.
// Tags list in lexical order (OCI distribution spec), so the listing starts
// after "stats", which sorts just before every stats-<id>, and stops at the
// first tag past the prefix: sha256-* tags never use up the page bound. A
// registry that ignores last still works, with the page bound as the limit.
func statsTests(ctx context.Context, r *remote.Repository, set *RecordSet) ([]string, error) {
	var ids []string
	prefix := StatsTag("")
	err := r.Tags(context.WithValue(ctx, pageCount{}, new(int)), strings.TrimSuffix(prefix, "-"), func(tags []string) error {
		for _, t := range tags {
			if id, ok := strings.CutPrefix(t, prefix); ok && id != "" {
				ids = append(ids, id)
			} else if !ok && t > prefix {
				return errBound // past every stats-<id> tag
			}
		}
		return nil
	})
	if err := bounded(err, set, "tags"); err != nil {
		return nil, err
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// readRecord fetches one record manifest by digest and parses its record.
// The manifest must be a run record whose subject is the stats anchor.
func readRecord(ctx context.Context, r *remote.Repository, dg, anchor digest.Digest) (*evidence.RunRecord, error) {
	if err := dg.Validate(); err != nil || dg.Algorithm() != digest.SHA256 {
		return nil, fmt.Errorf("digest %q is not sha256", dg)
	}
	raw, err := fetchManifest(ctx, r, dg)
	if err != nil {
		return nil, err
	}
	var m ocispec.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m.MediaType != ocispec.MediaTypeImageManifest || m.ArtifactType != evidence.ArtifactTypeRun ||
		len(m.Layers) != 1 || m.Layers[0].MediaType != evidence.MediaTypeRun || m.Subject == nil || m.Subject.Digest != anchor {
		return nil, errors.New("not a run record manifest")
	}
	if m.Layers[0].Size > maxRecordSize {
		return nil, fmt.Errorf("record larger than %d bytes", maxRecordSize)
	}
	body, err := fetchBlob(ctx, r, m.Layers[0])
	if err != nil {
		return nil, err
	}
	return evidence.ParseRunRecord(body)
}
