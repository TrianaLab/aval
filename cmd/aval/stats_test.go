package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/pkg/evidence"
)

// fakeRecords replaces reading run records; it keeps the test ids asked for.
func fakeRecords(t *testing.T, set registry.RecordSet, err error) *[][]string {
	t.Helper()
	r, n := readRecords, now
	t.Cleanup(func() { readRecords, now = r, n })
	now = func() time.Time { return recordedAt }
	var asked [][]string
	readRecords = func(_ context.Context, _ string, ids []string) (registry.RecordSet, error) {
		asked = append(asked, ids)
		return set, err
	}
	return &asked
}

func rec(id string, outcome evidence.Outcome, reused bool, result, verified evidence.Result, ran, saved int64, at time.Time) evidence.RunRecord {
	return evidence.RunRecord{Version: evidence.RunRecordVersion, TestID: id, Outcome: outcome, Reused: reused, Result: result,
		VerifiedResult: verified, TestDurationMs: ran, SavedDurationMs: saved, RecordedAt: at}
}

// The JSON output of a known set of records is the golden stats report.
func TestStatsJSONIsTheGolden(t *testing.T) {
	const id, d = "orders-integration", 1184000
	in, old := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	p, f, e := evidence.ResultPassed, evidence.ResultFailed, evidence.Result("")
	records := []evidence.RunRecord{rec(id, evidence.OutcomeMiss, false, p, e, d, 0, old)} // before --since: left out
	for range 4 {
		records = append(records, rec(id, evidence.OutcomeVerified, true, p, p, 0, d, in))
	}
	for range 3 {
		records = append(records, rec(id, evidence.OutcomeMiss, false, p, e, d, 0, in))
	}
	records = append(records,
		rec(id, evidence.OutcomeError, false, p, e, d, 0, in),
		rec(id, evidence.OutcomeVerified, false, f, p, d, 0, in),           // shadow false hit
		rec(id, evidence.OutcomeVerificationFailed, false, p, e, d, 0, in), // no repository: unknown
	)
	for i := range records[:len(records)-1] {
		records[i].Repository = "acme/orders"
	}
	fakeRecords(t, registry.RecordSet{Records: records, Skipped: 2, Truncated: "more than 1000 run records"}, nil)
	code, stdout, stderr := runCLI("stats", "--registry", "ghcr.io/acme/orders-aval", "--since", "2026-10-30", "--by", "repository", "-o", "json")
	golden, err := os.ReadFile("../../pkg/evidence/testdata/stats-report-v1.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if code != ExitOK || stdout != string(golden) {
		t.Fatalf("exit %d, stdout:\n%s\nwant:\n%s", code, stdout, golden)
	}
	if !strings.Contains(stderr, "warning: stopped reading run records: more than 1000 run records") || !strings.Contains(stderr, "Pruning run records") {
		t.Fatalf("stderr %q: want the truncation warning", stderr)
	}
}

func TestStatsText(t *testing.T) {
	at := recordedAt
	asked := fakeRecords(t, registry.RecordSet{Records: []evidence.RunRecord{
		rec("unit", evidence.OutcomeVerified, true, evidence.ResultPassed, evidence.ResultPassed, 0, 90_000, at),
		rec("e2e\nINJECTED", evidence.OutcomeMiss, false, evidence.ResultPassed, "", 61_400, 0, at),
		rec("e2e\nINJECTED", evidence.OutcomeVerified, false, evidence.ResultPassed, evidence.ResultPassed, -5, -5, at), // a clock jump counts 0
	}, Skipped: 3}, nil)
	code, stdout, stderr := runCLI("stats", "--registry", "registry.example/acme/stats", "--test", "unit", "--since", "24h")
	want := `run records in registry.example/acme/stats since 2026-11-05T12:30:00Z
e2e\nINJECTED: 2 runs, 0 reused (hit rate 0.0%), test time 1m1s, saved 0s
  not reused: 1 no trusted result, 0 error, 1 verified but run (shadow)
  verification failures: 0, shadow false hits: 0
unit: 1 runs, 1 reused (hit rate 100.0%), test time 0s, saved 1m30s
  not reused: 0 no trusted result, 0 error, 0 verified but run (shadow)
  verification failures: 0, shadow false hits: 0
total: 3 runs, 1 reused (hit rate 33.3%), test time 1m1s, saved 1m30s
  not reused: 1 no trusted result, 0 error, 1 verified but run (shadow)
  verification failures: 0, shadow false hits: 0
skipped 3 run records: unreadable, oversized, malformed, inconsistent or for another test
`
	if code != ExitOK || stdout != want || stderr != "" {
		t.Fatalf("exit %d, stderr %q, stdout:\n%s\nwant:\n%s", code, stderr, stdout, want)
	}
	if !slices.Equal((*asked)[0], []string{"unit"}) {
		t.Fatalf("asked for %v, want [unit]", *asked)
	}
}

// --by repository groups by source repository, records without one under
// unknown, and duration sums saturate instead of wrapping.
func TestStatsByRepository(t *testing.T) {
	at, p := recordedAt, evidence.ResultPassed
	big := rec("unit", evidence.OutcomeVerified, true, p, p, 0, math.MaxInt64, at)
	big.Repository = "acme/orders"
	hand := rec("unit", evidence.OutcomeMiss, false, p, "", math.MaxInt64, 0, at)
	hand.Repository = "acme/orders\nINJECTED"
	fakeRecords(t, registry.RecordSet{Records: []evidence.RunRecord{big, big, hand, rec("e2e", evidence.OutcomeMiss, false, p, "", 2_000, 0, at)}}, nil)
	code, stdout, stderr := runCLI("stats", "--registry", "registry.example/acme/stats", "--by", "repository")
	want := `run records in registry.example/acme/stats
acme/orders: 2 runs, 2 reused (hit rate 100.0%), test time 0s, saved 2562047h47m16s
  not reused: 0 no trusted result, 0 error, 0 verified but run (shadow)
  verification failures: 0, shadow false hits: 0
acme/orders\nINJECTED: 1 runs, 0 reused (hit rate 0.0%), test time 2562047h47m16s, saved 0s
  not reused: 1 no trusted result, 0 error, 0 verified but run (shadow)
  verification failures: 0, shadow false hits: 0
unknown: 1 runs, 0 reused (hit rate 0.0%), test time 2s, saved 0s
  not reused: 1 no trusted result, 0 error, 0 verified but run (shadow)
  verification failures: 0, shadow false hits: 0
total: 4 runs, 2 reused (hit rate 50.0%), test time 2562047h47m16s, saved 2562047h47m16s
  not reused: 2 no trusted result, 0 error, 0 verified but run (shadow)
  verification failures: 0, shadow false hits: 0
`
	if code != ExitOK || stdout != want || stderr != "" {
		t.Fatalf("exit %d, stderr %q, stdout:\n%s\nwant:\n%s", code, stderr, stdout, want)
	}
	_, stdout, _ = runCLI("stats", "--registry", "registry.example/acme/stats", "-o", "json")
	var r evidence.StatsReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil || r.Repositories != nil || r.Total.SavedDurationMs != math.MaxInt64 || r.Total.TestDurationMs != math.MaxInt64 || len(r.Tests) != 2 {
		t.Fatalf("--by test JSON: %v %+v", err, r)
	}
}

func TestAddMs(t *testing.T) {
	for _, tc := range [][3]int64{{1, 2, 3}, {1, -5, 1}, {math.MaxInt64 - 1, 1, math.MaxInt64}, {math.MaxInt64, math.MaxInt64, math.MaxInt64}, {5, math.MinInt64, 5}} {
		if got := addMs(tc[0], tc[1]); got != tc[2] {
			t.Errorf("addMs(%d, %d) = %d, want %d", tc[0], tc[1], got, tc[2])
		}
	}
}

func TestStatsDeadline(t *testing.T) {
	fakeRecords(t, registry.RecordSet{}, fmt.Errorf("reading the records of x: %w", context.DeadlineExceeded))
	code, _, stderr := runCLI("stats", "--registry", "registry.example/acme/stats")
	if code != ExitError || !strings.Contains(stderr, "context deadline exceeded; raise --registry-timeout") || !strings.Contains(stderr, "Pruning run records") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}

func TestStatsNoRecords(t *testing.T) {
	asked := fakeRecords(t, registry.RecordSet{Truncated: "more than 64 pages listing tags"}, nil)
	code, stdout, stderr := runCLI("stats", "--registry", "registry.example/acme/stats")
	if code != ExitOK || stdout != "run records in registry.example/acme/stats\nno run records\nincomplete: more than 64 pages listing tags\n" || !strings.Contains(stderr, "warning") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if (*asked)[0] != nil {
		t.Fatalf("asked for %v, want every test", *asked)
	}
	_, stdout, _ = runCLI("stats", "--registry", "registry.example/acme/stats", "-o", "json")
	if !strings.Contains(stdout, `"tests": []`) {
		t.Fatalf("json without records: %s", stdout)
	}
	// repositories is omitted, not [], when there are no records.
	_, stdout, _ = runCLI("stats", "--registry", "registry.example/acme/stats", "--by", "repository", "-o", "json")
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil || doc["repositories"] != nil || doc["total"] == nil {
		t.Fatalf("--by repository json without records: %v %s", err, stdout)
	}
}

func TestStatsErrors(t *testing.T) {
	fakeRecords(t, registry.RecordSet{}, errors.New("registry down"))
	for name, tc := range map[string]struct {
		args []string
		code int
	}{
		"registry error": {nil, ExitError},
		"no registry":    {[]string{"--registry", ""}, ExitUsage},
		"bad registry":   {[]string{"--registry", "https://ghcr.io/acme/x"}, ExitUsage},
		"bad output":     {[]string{"-o", "yaml"}, ExitUsage},
		"bad by":         {[]string{"--by", "owner"}, ExitUsage},
		"bad since":      {[]string{"--since", "last week"}, ExitUsage},
		"bad timeout":    {[]string{"--registry-timeout", "0s"}, ExitUsage},
		"stray argument": {[]string{"unit"}, ExitUsage},
		"negative since": {[]string{"--since=-1h"}, ExitUsage},
	} {
		args := append([]string{"stats", "--registry", "registry.example/acme/stats"}, tc.args...)
		if code, _, stderr := runCLI(args...); code != tc.code {
			t.Errorf("%s: exit %d, want %d (stderr %q)", name, code, tc.code, stderr)
		}
	}
}

func TestWriteStatsPropagatesWriteErrors(t *testing.T) {
	if err := writeStats(failWriter{}, "json", evidence.StatsReport{}); err == nil {
		t.Fatal("want the write error")
	}
}

func TestParseSince(t *testing.T) {
	n := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"168h":                      n.Add(-168 * time.Hour),
		"30d":                       n.AddDate(0, 0, -30),
		"2026-11-01":                time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		"2026-11-01T10:00:00+02:00": time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC),
	} {
		if got, err := parseSince(in, n); err != nil || !got.Equal(want) {
			t.Errorf("%s: %v %v, want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"-1h", "0s", "0d", "-3d", "d", "x", "2026-13-01", "36501d", "9999999d", "999999999999999999d", "0000-06-01"} {
		if _, err := parseSince(in, n); err == nil {
			t.Errorf("%s: accepted", in)
		}
	}
}
