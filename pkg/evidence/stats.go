package evidence

import "time"

// StatsReportVersion is the version of the `aval stats --output json`
// document.
const StatsReportVersion = "aval.trianalab.io/stats-report/v1"

// StatsReport is the JSON document printed by `aval stats --output json`:
// run records aggregated on demand. Statistics are informational: they come
// from unsigned run records and never affect reuse. Additive changes only
// within a StatsReportVersion.
type StatsReport struct {
	Version string `json:"version"`
	// Registry is the repository the run records were read from.
	Registry string `json:"registry"`
	// Since is the lower bound on recordedAt, when one was given.
	Since *time.Time `json:"since,omitempty"`
	// Total aggregates every test in Tests.
	Total RunStats `json:"total"`
	// Tests holds one entry per test with records, sorted by test id.
	Tests []TestStats `json:"tests"`
	// Skipped counts records that were read but not aggregated: malformed,
	// oversized, inconsistent, unreadable, or for another test.
	Skipped int `json:"skipped"`
	// Truncated says which read bound was hit, when one was: the statistics
	// cover only the records read before it. Empty when every record was
	// read.
	Truncated string `json:"truncated,omitempty"`
}

// TestStats is the aggregate of one test's run records.
type TestStats struct {
	TestID string   `json:"testId"`
	Stats  RunStats `json:"stats"`
}

// RunStats aggregates run records. Runs is the sum of Reused,
// Misses.NoTrustedResult, Misses.Error, Misses.VerifiedNotReused and
// VerificationFailures.
type RunStats struct {
	Runs   int `json:"runs"`
	Reused int `json:"reused"`
	// HitRate is Reused / Runs, 0 when Runs is 0.
	HitRate float64    `json:"hitRate"`
	Misses  MissCauses `json:"misses"`
	// VerificationFailures counts runs with outcome verification-failed:
	// security events, not misses.
	VerificationFailures int `json:"verificationFailures"`
	// ShadowFalseHits counts runs where a verified result was not reused,
	// the test ran, and its result differed from the verified one.
	ShadowFalseHits int `json:"shadowFalseHits"`
	// TestDurationMs is the total time runner.command ran.
	TestDurationMs int64 `json:"testDurationMs"`
	// SavedDurationMs is the total test time reuse saved.
	SavedDurationMs int64 `json:"savedDurationMs"`
}

// MissCauses splits the runs that did not reuse a result, other than
// verification failures, by why.
type MissCauses struct {
	// NoTrustedResult: outcome miss.
	NoTrustedResult int `json:"noTrustedResult"`
	// Error: outcome error; Aval could not decide.
	Error int `json:"error"`
	// VerifiedNotReused: outcome verified, but the test ran (--shadow, or
	// the --report file could not be written).
	VerifiedNotReused int `json:"verifiedNotReused"`
}
