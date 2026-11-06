package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
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
// run of one test from acme/orders and one run of a second test with no
// repository, all with --record; a malformed record sits next to them. aval
// stats (text and JSON) reports the totals, groups them by test and by
// repository (unknown when the record has none) and counts the malformed
// record as skipped.
func TestStatsStory(t *testing.T) {
	srv := httptest.NewServer(ggcr.New(ggcr.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/stats-aval"

	keys := t.TempDir()
	key, pub := keyPair(t, keys, "dev")
	trusted := writePolicy(t, keys, "trusted.yaml", pub)
	dir := writeProject(t, map[string]string{"aval.yaml": contract, "aval.other.yaml": strings.Replace(contract, "name: story", "name: story-2", 1), "src/a.txt": "a\n"})
	file := filepath.Join(dir, "aval.yaml")
	result := filepath.Join(dir, ".aval", "result.json")

	run := []string{"run", "-f", file, "--registry", repo, "--result", result, "--record", "--repository", "acme/orders"}
	for _, args := range [][]string{
		run, // miss: the test runs
		{"push", "-f", file, "--registry", repo, "--result", result, "--key", key},
		slices.Concat(run, []string{"--policy", trusted}),                                                                                           // reused
		slices.Concat(run, []string{"--policy", trusted, "--shadow"}),                                                                               // verified but run
		{"run", "-f", filepath.Join(dir, "aval.other.yaml"), "--registry", repo, "--result", filepath.Join(dir, ".aval", "other.json"), "--record"}, // no repository
	} {
		if stdout, stderr, code := avalEnv(t, args...); code != 0 || strings.Contains(stderr, "warning") {
			t.Fatalf("%v: exit %d\nstdout %s\nstderr %s", args, code, stdout, stderr)
		}
	}
	pushMalformedRecord(t, repo, "story")

	for by, wants := range map[string][]string{
		"test":       {"\nstory: 3 runs, 1 reused", "\nstory-2: 1 runs, 0 reused", "\ntotal: 4 runs, 1 reused", "\nskipped 1 run records"},
		"repository": {"\nacme/orders: 3 runs, 1 reused", "\nunknown: 1 runs, 0 reused", "\ntotal: 4 runs, 1 reused", "\nskipped 1 run records"},
	} {
		stdout, stderr, code := avalEnv(t, "stats", "--registry", repo, "--by", by)
		for _, want := range wants {
			if code != 0 || !strings.Contains(stdout, want) {
				t.Fatalf("stats --by %s: exit %d, want %q\nstdout %s\nstderr %s", by, code, want, stdout, stderr)
			}
		}
	}

	checkStatsJSON(t, repo)
}

// checkStatsJSON decodes aval stats -o json --by repository strictly and
// checks the totals, the grouping by test and by repository and the skipped
// record.
func checkStatsJSON(t *testing.T, repo string) {
	t.Helper()
	stdout, stderr, code := avalEnv(t, "stats", "--registry", repo, "-o", "json", "--by", "repository")
	if code != 0 {
		t.Fatalf("stats -o json: exit %d\nstderr %s", code, stderr)
	}
	dec := json.NewDecoder(strings.NewReader(stdout))
	dec.DisallowUnknownFields()
	var r evidence.StatsReport
	if err := dec.Decode(&r); err != nil {
		t.Fatal(err)
	}
	type group struct {
		name         string
		runs, reused int
	}
	groups := func(names []string, stats []evidence.RunStats) []group {
		var out []group
		for i, n := range names {
			out = append(out, group{n, stats[i].Runs, stats[i].Reused})
		}
		return out
	}
	var tests, repos []string
	var testStats, repoStats []evidence.RunStats
	for _, g := range r.Tests {
		tests, testStats = append(tests, g.TestID), append(testStats, g.Stats)
	}
	for _, g := range r.Repositories {
		repos, repoStats = append(repos, g.Repository), append(repoStats, g.Stats)
	}
	m := r.Total.Misses
	got := fmt.Sprint(r.Version, r.Total.Runs, r.Total.Reused, m.NoTrustedResult, m.VerifiedNotReused, r.Skipped, r.Truncated,
		groups(tests, testStats), groups(repos, repoStats))
	want := fmt.Sprint(evidence.StatsReportVersion, 4, 1, 2, 1, 1, "",
		[]group{{"story", 3, 1}, {"story-2", 1, 0}}, []group{{"acme/orders", 3, 1}, {evidence.StatsUnknownRepository, 1, 0}})
	if got != want {
		t.Fatalf("stats report = %s\nwant %s", got, want)
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
