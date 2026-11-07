package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Run records: what one `aval run` did, published for statistics. A record
// is unsigned and informational. It never grants trust and is never a
// candidate for reuse: `aval run` and `aval verify` do not read records.
const (
	// RunRecordVersion is the version of the run record document.
	RunRecordVersion = "aval.trianalab.io/run-record/v1"

	// ArtifactTypeRun is the OCI artifactType of the manifest that carries
	// a run record.
	ArtifactTypeRun = "application/vnd.trianalab.aval.run.v1"

	// MediaTypeRun is the media type of the run record JSON layer.
	MediaTypeRun = "application/vnd.trianalab.aval.run.v1+json"

	// ArtifactTypeStats is the OCI artifactType of the per-test stats
	// anchor manifest that every run record refers to (its subject).
	ArtifactTypeStats = "application/vnd.trianalab.aval.stats.v1"
)

// RunRecord is the JSON document of one run record. Additive changes only
// within a RunRecordVersion.
type RunRecord struct {
	Version string `json:"version"`
	// TestID is metadata.name from the contract.
	TestID string `json:"testId"`
	// Fingerprint is empty when it could not be computed.
	Fingerprint string `json:"fingerprint"`
	// Outcome is the lookup decision, as in the verify report.
	Outcome Outcome `json:"outcome"`
	// Reused is true when a verified result was reused and the test did not
	// run.
	Reused bool `json:"reused"`
	// Result is the reused or the real result; empty when the test could
	// not start.
	Result Result `json:"result,omitempty"`
	// VerifiedResult is the result of the verified attestation when the
	// outcome is verified; empty otherwise. With Reused false and the test
	// run, a VerifiedResult that differs from Result is a false hit.
	VerifiedResult Result `json:"verifiedResult,omitempty"`
	// TestDurationMs is how long runner.command ran (0 when it did not).
	TestDurationMs int64 `json:"testDurationMs"`
	// SavedDurationMs is, on reuse, the test duration recorded in the
	// reused attestation (0 otherwise).
	SavedDurationMs int64 `json:"savedDurationMs"`
	// AvalVersion is the version of the aval binary that ran.
	AvalVersion string    `json:"avalVersion"`
	RecordedAt  time.Time `json:"recordedAt"`
	// CIRunURL links the CI run, when the caller passed one.
	CIRunURL string `json:"ciRunUrl,omitempty"`
	// Repository is the source repository of the run (e.g. acme/orders),
	// when the caller passed one. Statistics count records without it as
	// unknown.
	Repository string `json:"repository,omitempty"`
}

// ParseRunRecord decodes a run record and checks its version, test id,
// closed sets and that it is consistent: reused requires outcome verified and
// testDurationMs 0, and verifiedResult requires outcome verified. Like Parse,
// it checks structure only: a record is a claim.
func ParseRunRecord(data []byte) (*RunRecord, error) {
	var r RunRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("decoding run record: %w", err)
	}
	var errs []error
	if r.Version != RunRecordVersion {
		errs = append(errs, fmt.Errorf("version: got %q, want %q", r.Version, RunRecordVersion))
	}
	if r.TestID == "" {
		errs = append(errs, errors.New("testId: required"))
	}
	if !slices.Contains([]Outcome{OutcomeVerified, OutcomeMiss, OutcomeVerificationFailed, OutcomeError}, r.Outcome) {
		errs = append(errs, fmt.Errorf("outcome: unknown value %q", r.Outcome))
	}
	for i, v := range []Result{r.Result, r.VerifiedResult} {
		if v != "" && v != ResultPassed && v != ResultFailed {
			errs = append(errs, fmt.Errorf("%s: unknown value %q", [...]string{"result", "verifiedResult"}[i], v))
		}
	}
	if r.Reused && (r.Outcome != OutcomeVerified || r.TestDurationMs != 0) {
		errs = append(errs, fmt.Errorf("reused: requires outcome verified and testDurationMs 0, got outcome %q and testDurationMs %d", r.Outcome, r.TestDurationMs))
	}
	if r.VerifiedResult != "" && r.Outcome != OutcomeVerified {
		errs = append(errs, fmt.Errorf("verifiedResult: requires outcome verified, got %q", r.Outcome))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &r, nil
}
