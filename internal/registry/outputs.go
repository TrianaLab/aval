package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/TrianaLab/aval/pkg/evidence"
)

// Caps on the outputs of one execution (contract outputs), enforced when
// they are published and before anything is read when they are restored.
const (
	MaxOutputFiles = 256
	MaxOutputSize  = 16 << 20 // per file
	MaxOutputTotal = 64 << 20
)

// Output is one output file: its slash-separated path relative to the
// contract directory, and its content.
type Output struct {
	Path string
	Data []byte
}

// PushOutputs uploads files as one outputs manifest (artifactType
// evidence.ArtifactTypeOutputs, one layer per file) whose subject is the
// fingerprint's anchor, and returns what the predicate signs. Lookup lists
// only execution manifests, so an outputs manifest is never a candidate and
// does not count toward the candidate cap. The caller enforces the caps.
func PushOutputs(ctx context.Context, repo, fingerprint string, files []Output) (*evidence.Outputs, error) {
	if err := checkFingerprint(fingerprint); err != nil {
		return nil, err
	}
	r, err := newRepository(repo)
	if err != nil {
		return nil, err
	}
	out := &evidence.Outputs{}
	var layers []ocispec.Descriptor
	for _, f := range files {
		d, err := pushBlob(ctx, r, evidence.MediaTypeOutput, f.Data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", repo, err)
		}
		d.Annotations = map[string]string{ocispec.AnnotationTitle: f.Path}
		layers = append(layers, d)
		out.Files = append(out.Files, evidence.OutputFile{Path: f.Path, Digest: d.Digest.String(), Size: d.Size})
	}
	if _, err := pushBlob(ctx, r, ocispec.MediaTypeEmptyJSON, emptyJSON.Data); err != nil {
		return nil, fmt.Errorf("%s: %w", repo, err)
	}
	anchorRaw, anchorDesc := anchor(fingerprint)
	if _, err := oras.TagBytes(ctx, r, ocispec.MediaTypeImageManifest, anchorRaw, Tag(fingerprint)); err != nil {
		return nil, fmt.Errorf("%s: tagging anchor: %w", repo, err)
	}
	m, _ := json.Marshal(ocispec.Manifest{ // ponytail: plain structs and strings, cannot fail
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: evidence.ArtifactTypeOutputs,
		Config:       emptyJSON,
		Layers:       layers,
		Subject:      &ocispec.Descriptor{MediaType: anchorDesc.MediaType, Digest: anchorDesc.Digest, Size: anchorDesc.Size},
		Annotations:  map[string]string{AnnotationFingerprint: fingerprint},
	})
	d, err := oras.PushBytes(ctx, r, ocispec.MediaTypeImageManifest, m)
	if err != nil {
		return nil, fmt.Errorf("%s: pushing outputs manifest: %w", repo, err)
	}
	out.Manifest = d.Digest.String()
	return out, nil
}

// FetchOutputs downloads the outputs a verified predicate lists, by digest,
// in its order. The caps are checked on want before anything is read. A
// missing manifest or blob, a digest or size mismatch, or a layer list that
// differs from want is an error wrapping evidence.ErrVerification, never a
// miss: the signed result promised these files. Transport failures are plain
// errors.
func FetchOutputs(ctx context.Context, repo string, want *evidence.Outputs) ([]Output, error) {
	if err := checkCaps(want.Files); err != nil {
		return nil, err
	}
	dg, err := digest.Parse(want.Manifest)
	if err != nil {
		return nil, badOutputs("manifest digest: %v", err)
	}
	r, err := newRepository(repo)
	if err != nil {
		return nil, err
	}
	raw, err := fetchManifest(ctx, r, dg)
	if errors.Is(err, evidence.ErrNotFound) {
		return nil, badOutputs("manifest %s is missing", dg)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: outputs: %w", repo, err)
	}
	var m ocispec.Manifest
	if err := json.Unmarshal(raw, &m); err != nil || m.ArtifactType != evidence.ArtifactTypeOutputs || len(m.Layers) != len(want.Files) {
		return nil, badOutputs("manifest %s is not the outputs manifest the result lists (%d files)", dg, len(want.Files))
	}
	out := make([]Output, len(m.Layers))
	for i, l := range m.Layers {
		data, err := fetchOutput(ctx, r, l, want.Files[i])
		if err != nil {
			return nil, err
		}
		out[i] = Output{Path: want.Files[i].Path, Data: data}
	}
	return out, nil
}

// fetchOutput reads the layer l, which must be the output f.
func fetchOutput(ctx context.Context, r *remote.Repository, l ocispec.Descriptor, f evidence.OutputFile) ([]byte, error) {
	if l.MediaType != evidence.MediaTypeOutput || l.Annotations[ocispec.AnnotationTitle] != f.Path || l.Digest.String() != f.Digest || l.Size != f.Size {
		return nil, badOutputs("layer %s does not match %s", l.Digest, f.Path)
	}
	data, err := fetchBlob(ctx, r, l, MaxOutputSize)
	switch {
	case errors.Is(err, errdef.ErrNotFound):
		return nil, badOutputs("%s (%s) is missing", f.Path, f.Digest)
	case errors.Is(err, evidence.ErrVerification):
		return nil, badOutputs("%s: %v", f.Path, err)
	case err != nil:
		return nil, fmt.Errorf("%s: outputs: %w", r.Reference, err)
	}
	return data, nil
}

// checkCaps rejects a file list over the output caps.
func checkCaps(files []evidence.OutputFile) error {
	var total int64
	for _, f := range files {
		total += f.Size
		if f.Size > MaxOutputSize {
			return badOutputs("%s: %d bytes, over the %d byte cap", f.Path, f.Size, MaxOutputSize)
		}
	}
	if len(files) > MaxOutputFiles || total > MaxOutputTotal {
		return badOutputs("%d files, %d bytes: over the cap of %d files, %d bytes", len(files), total, MaxOutputFiles, MaxOutputTotal)
	}
	return nil
}

func badOutputs(format string, args ...any) error {
	return &evidence.VerificationError{Reason: "outputs: " + fmt.Sprintf(format, args...)}
}
