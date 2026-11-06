package acceptance

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	ggcr "github.com/google/go-containerregistry/pkg/registry"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/TrianaLab/aval/pkg/evidence"
)

// Issue #88, level 4: the real binary records a miss, a reuse and a shadow
// run with --record, a malformed record sits next to them, and aval stats
// (text and JSON) reports the totals, groups them by test and counts the
// malformed record as skipped.
func TestStatsStory(t *testing.T) {
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/stats-aval"

	keys := t.TempDir()
	key, pub := keyPair(t, keys, "dev")
	trusted := writePolicy(t, keys, "trusted.yaml", pub)
	dir := writeProject(t, map[string]string{"aval.yaml": contract, "src/a.txt": "a\n"})
	file := filepath.Join(dir, "aval.yaml")
	result := filepath.Join(dir, ".aval", "result.json")

	run := []string{"run", "-f", file, "--registry", repo, "--result", result, "--record"}
	for _, args := range [][]string{
		run, // miss: the test runs
		{"push", "-f", file, "--registry", repo, "--result", result, "--key", key},
		slices.Concat(run, []string{"--policy", trusted}),             // reused
		slices.Concat(run, []string{"--policy", trusted, "--shadow"}), // verified but run
	} {
		if stdout, stderr, code := avalEnv(t, args...); code != 0 || strings.Contains(stderr, "warning") {
			t.Fatalf("%v: exit %d\nstdout %s\nstderr %s", args, code, stdout, stderr)
		}
	}
	pushMalformedRecord(t, repo, "story")

	stdout, stderr, code := avalEnv(t, "stats", "--registry", repo)
	for _, want := range []string{"\nstory: 3 runs, 1 reused", "\nskipped 1 run records"} {
		if code != 0 || !strings.Contains(stdout, want) {
			t.Fatalf("stats: exit %d, want %q\nstdout %s\nstderr %s", code, want, stdout, stderr)
		}
	}

	checkStatsJSON(t, repo)
}

// checkStatsJSON decodes aval stats -o json strictly and checks the totals,
// the grouping by test and the skipped record.
func checkStatsJSON(t *testing.T, repo string) {
	t.Helper()
	stdout, stderr, code := avalEnv(t, "stats", "--registry", repo, "-o", "json")
	if code != 0 {
		t.Fatalf("stats -o json: exit %d\nstderr %s", code, stderr)
	}
	dec := json.NewDecoder(strings.NewReader(stdout))
	dec.DisallowUnknownFields()
	var r evidence.StatsReport
	if err := dec.Decode(&r); err != nil {
		t.Fatal(err)
	}
	m := r.Total.Misses
	if r.Version != evidence.StatsReportVersion || r.Total.Runs != 3 || r.Total.Reused != 1 || m.NoTrustedResult != 1 || m.VerifiedNotReused != 1 ||
		len(r.Tests) != 1 || r.Tests[0].TestID != "story" || r.Tests[0].Stats.Runs != 3 || r.Skipped != 1 || r.Truncated != "" {
		t.Fatalf("stats report = %+v", r)
	}
}

// pushMalformedRecord pushes a run record manifest whose layer is not JSON,
// referring to the test's stats anchor.
func pushMalformedRecord(t *testing.T, repo, testID string) {
	t.Helper()
	ctx := context.Background()
	r, err := remote.NewRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	r.PlainHTTP = true
	anchor, err := r.Resolve(ctx, "stats-"+testID)
	if err != nil {
		t.Fatal(err)
	}
	layer, err := oras.PushBytes(ctx, r, evidence.MediaTypeRun, []byte("{not json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oras.PackManifest(ctx, r, oras.PackManifestVersion1_1, evidence.ArtifactTypeRun, oras.PackManifestOptions{
		Subject: &anchor,
		Layers:  []ocispec.Descriptor{layer},
	}); err != nil {
		t.Fatal(err)
	}
}
