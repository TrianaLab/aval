package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/TrianaLab/aval/internal/registry"
	"github.com/TrianaLab/aval/pkg/evidence"
)

func newStatsCmd() *cobra.Command {
	var repo, test, since, output string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Aggregate the run records in the registry: reuse, misses, time saved",
		Long: `stats reads the run records that 'aval run --record' and 'aval record'
published to the registry and aggregates them: runs, hit rate, misses by
cause, verification failures, shadow false hits, and total and saved test
time, per test and in total. It reads every test with a stats anchor, or only
--test. It writes nothing, locally or to the registry.

Run records are unsigned statistics: they never affect reuse. Records that
cannot be read, are oversized, malformed or inconsistent are skipped and
counted. Reading stops after 1000 records or 64 pages per listing, with a
warning; prune old records to stay below it.

--since keeps records made at or after a time: a duration back from now
(168h, 30d) or a date (2026-11-01, or RFC 3339).

Exit codes: 0 statistics printed, 1 error (registry), 2 usage.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if output != "text" && output != "json" {
				return usageError{fmt.Errorf("--output must be text or json, got %q", output)}
			}
			if repo == "" {
				return usageError{errors.New("--registry is required")}
			}
			if err := cmp.Or(checkRegistry(repo), checkTimeout(timeout)); err != nil {
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
			if err != nil {
				return err
			}
			if set.Truncated != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "aval: warning: stopped reading run records: %s; the statistics cover only the records read before it. Prune old run records (docs/artifact-format.md, \"Pruning run records\")\n", oneLine(set.Truncated))
			}
			return writeStats(cmd.OutOrStdout(), output, aggregate(repo, from, set))
		},
	}
	f := cmd.Flags()
	f.StringVar(&repo, "registry", "", "OCI repository holding the run records (required)")
	f.StringVar(&test, "test", "", "only this test id (metadata.name); default: every test with run records")
	f.StringVar(&since, "since", "", "only records made since: a duration (168h, 30d) or a date (2026-11-01, RFC 3339)")
	f.StringVarP(&output, "output", "o", "text", "output format: text or json")
	registryTimeoutFlag(cmd, &timeout)
	return cmd
}

// parseSince turns --since into a time: a positive Go duration or a number
// of days back from now, an RFC 3339 time or a date (UTC midnight).
func parseSince(s string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && n > 0 && strings.HasSuffix(s, "d") {
		return now.AddDate(0, 0, -n), nil
	}
	for _, layout := range []string{time.RFC3339, time.DateOnly} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("--since %q is neither a positive duration (168h, 30d) nor a date (2026-11-01, RFC 3339)", s)
}

// aggregate turns the records read into the stats report. Records made
// before since are left out; they are neither counted nor skipped.
func aggregate(repo string, since *time.Time, set registry.RecordSet) evidence.StatsReport {
	r := evidence.StatsReport{Version: evidence.StatsReportVersion, Registry: repo, Since: since, Tests: []evidence.TestStats{}, Skipped: set.Skipped, Truncated: set.Truncated}
	for _, rec := range set.Records {
		if since != nil && rec.RecordedAt.Before(*since) {
			continue
		}
		i := slices.IndexFunc(r.Tests, func(t evidence.TestStats) bool { return t.TestID == rec.TestID })
		if i < 0 {
			i = len(r.Tests)
			r.Tests = append(r.Tests, evidence.TestStats{TestID: rec.TestID})
		}
		count(&r.Tests[i].Stats, rec)
		count(&r.Total, rec)
	}
	slices.SortFunc(r.Tests, func(a, b evidence.TestStats) int { return strings.Compare(a.TestID, b.TestID) })
	for i := range r.Tests {
		hitRate(&r.Tests[i].Stats)
	}
	hitRate(&r.Total)
	return r
}

// count adds one record. ParseRunRecord already rejected the inconsistent
// ones (a reuse on any outcome but verified, or with test time). Negative
// durations, which only a clock jump or an edit can produce, count as 0.
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
	s.TestDurationMs += max(rec.TestDurationMs, 0)
	s.SavedDurationMs += max(rec.SavedDurationMs, 0)
}

func hitRate(s *evidence.RunStats) {
	if s.Runs > 0 {
		s.HitRate = float64(s.Reused) / float64(s.Runs)
	}
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
	for _, t := range r.Tests {
		writeRunStats(w, oneLine(t.TestID), t.Stats)
	}
	if len(r.Tests) > 1 {
		writeRunStats(w, "total", r.Total)
	}
	if r.Skipped > 0 {
		fmt.Fprintf(w, "skipped %d run records: unreadable, oversized, malformed, inconsistent or for another test\n", r.Skipped)
	}
	if r.Truncated != "" {
		fmt.Fprintf(w, "incomplete: %s\n", oneLine(r.Truncated))
	}
	return nil
}

func writeRunStats(w io.Writer, name string, s evidence.RunStats) {
	ms := func(n int64) time.Duration { return (time.Duration(n) * time.Millisecond).Round(time.Second) }
	fmt.Fprintf(w, "%s: %d runs, %d reused (hit rate %.1f%%), test time %s, saved %s\n", name, s.Runs, s.Reused, 100*s.HitRate, ms(s.TestDurationMs), ms(s.SavedDurationMs))
	fmt.Fprintf(w, "  not reused: %d no trusted result, %d error, %d verified but run (shadow)\n", s.Misses.NoTrustedResult, s.Misses.Error, s.Misses.VerifiedNotReused)
	fmt.Fprintf(w, "  verification failures: %d, shadow false hits: %d\n", s.VerificationFailures, s.ShadowFalseHits)
}
