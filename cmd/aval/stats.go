package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/TrianaLab/aval/internal/cliout"
	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/pkg/evidence"
)

func newStatsCmd() *cobra.Command {
	var test, since, output, by string
	g := &globalFlags{}
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Aggregate the run records in the registry: reuse, misses, time saved",
		Long: `stats reads the run records that 'aval run --record' and 'aval record'
published to the registry and aggregates them: runs, hit rate, misses by
cause, verification failures, shadow false hits, and total and saved test
time, per test (or per source repository with --by repository) and in total.
It reads every test with a stats anchor, or only --test. It writes nothing,
locally or to the registry.

Run records are unsigned statistics and never affect reuse. Unreadable or
invalid records are skipped and counted. Reading stops after 1000 records or
64 pages per listing; prune old records. Raise --registry-timeout for many
records.

--since keeps records made at or after a time: a duration back from now
(168h, 30d) or a date (2026-11-01, or RFC 3339).

Exit codes: 0 statistics printed, 1 error (registry), 2 usage.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if output != "text" && output != "json" {
				return usageError{fmt.Errorf("--output must be text or json, got %q", output)}
			}
			if by != "test" && by != "repository" {
				return usageError{fmt.Errorf("--by must be test or repository, got %q", by)}
			}
			repo, timeout := g.registry, g.timeout
			g.printConfig(cmd.ErrOrStderr()) // stdout may be JSON
			if err := cmp.Or(requireRegistry(repo), checkTimeout(timeout)); err != nil {
				return err
			}
			var from *time.Time
			if since != "" {
				t, err := parseSince(since, now().UTC())
				if err != nil {
					return usageError{err}
				}
				from = &t
			}
			var ids []string
			if test != "" {
				ids = []string{test}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			set, err := readRecords(ctx, repo, ids)
			if errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("%w; raise --registry-timeout (records are read one at a time) or prune old run records (docs/artifact-format.md, \"Pruning run records\")", err)
			}
			if err != nil {
				return err
			}
			if set.Truncated != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "aval: warning: stopped reading run records: %s; the statistics cover only the records read before it. Prune old run records (docs/artifact-format.md, \"Pruning run records\")\n", cliout.OneLine(set.Truncated))
			}
			return writeStats(cmd.OutOrStdout(), output, aggregate(repo, from, by == "repository", set))
		},
	}
	g.bind(cmd, false, false)
	f := cmd.Flags()
	f.StringVar(&test, "test", "", "only this test id (metadata.name); default: every test with run records")
	f.StringVar(&since, "since", "", "only records made since: a duration (168h, 30d) or a date (2026-11-01, RFC 3339)")
	f.StringVarP(&output, "output", "o", "text", "output format: text or json")
	f.StringVar(&by, "by", "test", "group by test or repository (the run record's source repository)")
	return cmd
}

// parseSince turns --since into a time: a positive Go duration or a number
// of days back from now, an RFC 3339 time or a date (UTC midnight).
func parseSince(s string, now time.Time) (time.Time, error) {
	t, err := parseSinceTime(s, now)
	if err == nil && (t.Year() < 1 || t.Year() > 9999) {
		return time.Time{}, fmt.Errorf("--since %q is outside the years 1 to 9999", s)
	}
	return t, err
}

func parseSinceTime(s string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && n > 0 && n <= 36500 && strings.HasSuffix(s, "d") {
		return now.AddDate(0, 0, -n), nil
	}
	for _, layout := range []string{time.RFC3339, time.DateOnly} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("--since %q is neither a positive duration (168h, 30d, at most 36500d) nor a date (2026-11-01, RFC 3339)", s)
}

// aggregate turns the records read into the stats report. Records made
// before since are left out; they are neither counted nor skipped. Tests are
// always grouped; byRepo also groups by source repository.
func aggregate(repo string, since *time.Time, byRepo bool, set registry.RecordSet) evidence.StatsReport {
	r := evidence.StatsReport{Version: evidence.StatsReportVersion, Registry: repo, Since: since, Tests: []evidence.TestStats{}, Skipped: set.Skipped, Truncated: set.Truncated}
	tests, repos := map[string]*evidence.RunStats{}, map[string]*evidence.RunStats{}
	for _, rec := range set.Records {
		if since != nil && rec.RecordedAt.Before(*since) {
			continue
		}
		count(group(tests, rec.TestID), rec)
		count(group(repos, cmp.Or(rec.Repository, evidence.StatsUnknownRepository)), rec)
		count(&r.Total, rec)
	}
	for _, id := range slices.Sorted(maps.Keys(tests)) {
		r.Tests = append(r.Tests, evidence.TestStats{TestID: id, Stats: hitRate(tests[id])})
	}
	if byRepo {
		r.Repositories = []evidence.RepositoryStats{}
		for _, name := range slices.Sorted(maps.Keys(repos)) {
			r.Repositories = append(r.Repositories, evidence.RepositoryStats{Repository: name, Stats: hitRate(repos[name])})
		}
	}
	r.Total = hitRate(&r.Total)
	return r
}

func group(m map[string]*evidence.RunStats, key string) *evidence.RunStats {
	if m[key] == nil {
		m[key] = &evidence.RunStats{}
	}
	return m[key]
}

// count adds one record. ParseRunRecord already rejected the inconsistent
// ones (a reuse on any outcome but verified, or with test time). Negative
// durations, which only a clock jump or an edit can produce, count as 0, and
// sums saturate at the int64 maximum instead of wrapping.
func count(s *evidence.RunStats, rec evidence.RunRecord) {
	s.Runs++
	switch rec.Outcome {
	case evidence.OutcomeVerified:
		if rec.Reused {
			s.Reused++
			break
		}
		s.Misses.VerifiedNotReused++
		if rec.Result != "" && rec.VerifiedResult != "" && rec.Result != rec.VerifiedResult {
			s.ShadowFalseHits++
		}
	case evidence.OutcomeMiss:
		s.Misses.NoTrustedResult++
	case evidence.OutcomeVerificationFailed:
		s.VerificationFailures++
	default: // evidence.OutcomeError: ParseRunRecord admits no other value
		s.Misses.Error++
	}
	s.TestDurationMs = addMs(s.TestDurationMs, rec.TestDurationMs)
	s.SavedDurationMs = addMs(s.SavedDurationMs, rec.SavedDurationMs)
}

// addMs adds b to a, with a negative b as 0 and saturating at MaxInt64. a is
// never negative.
func addMs(a, b int64) int64 {
	b = max(b, 0)
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

func hitRate(s *evidence.RunStats) evidence.RunStats {
	if s.Runs > 0 {
		s.HitRate = float64(s.Reused) / float64(s.Runs)
	}
	return *s
}

func writeStats(w io.Writer, format string, r evidence.StatsReport) error {
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	fmt.Fprintf(w, "run records in %s", r.Registry)
	if r.Since != nil {
		fmt.Fprintf(w, " since %s", r.Since.UTC().Format(time.RFC3339))
	}
	fmt.Fprintln(w)
	if len(r.Tests) == 0 {
		fmt.Fprintln(w, "no run records")
	}
	groups := len(r.Tests)
	if r.Repositories != nil {
		groups = len(r.Repositories)
		for _, g := range r.Repositories {
			writeRunStats(w, cliout.OneLine(g.Repository), g.Stats)
		}
	} else {
		for _, t := range r.Tests {
			writeRunStats(w, cliout.OneLine(t.TestID), t.Stats)
		}
	}
	if groups > 1 {
		writeRunStats(w, "total", r.Total)
	}
	if r.Skipped > 0 {
		fmt.Fprintf(w, "skipped %d run records: unreadable, oversized, malformed, inconsistent or for another test\n", r.Skipped)
	}
	if r.Truncated != "" {
		fmt.Fprintf(w, "incomplete: %s\n", cliout.OneLine(r.Truncated))
	}
	return nil
}

func writeRunStats(w io.Writer, name string, s evidence.RunStats) {
	ms := func(n int64) time.Duration {
		if n > int64(math.MaxInt64/time.Millisecond) {
			return time.Duration(math.MaxInt64).Truncate(time.Second) // Round would overflow
		}
		return (time.Duration(n) * time.Millisecond).Round(time.Second)
	}
	fmt.Fprintf(w, "%s: %d runs, %d reused (hit rate %.1f%%), test time %s, saved %s\n", name, s.Runs, s.Reused, 100*s.HitRate, ms(s.TestDurationMs), ms(s.SavedDurationMs))
	fmt.Fprintf(w, "  not reused: %d no trusted result, %d error, %d verified but run (shadow)\n", s.Misses.NoTrustedResult, s.Misses.Error, s.Misses.VerifiedNotReused)
	fmt.Fprintf(w, "  verification failures: %d, shadow false hits: %d\n", s.VerificationFailures, s.ShadowFalseHits)
}
