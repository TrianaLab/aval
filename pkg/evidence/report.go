package evidence

// ReportVersion is the version of the `aval verify --output json` document.
const ReportVersion = "aval.trianalab.io/verify-report/v1"

// Outcome is the machine-readable verdict of `aval verify`.
type Outcome string

const (
	// OutcomeVerified: a valid attestation satisfies the policy; reuse it.
	OutcomeVerified Outcome = "verified"
	// OutcomeMiss: no attestation for the fingerprint that the policy can
	// reuse (none exists, or only authentic ones whose signer the policy
	// lists below its minimum trust level); run the test.
	OutcomeMiss Outcome = "miss"
	// OutcomeVerificationFailed: an attestation exists but is not trusted.
	// Security event: run the test AND alert.
	OutcomeVerificationFailed Outcome = "verification-failed"
	// OutcomeError: Aval could not decide (e.g. registry unreachable). Treat
	// as a miss for execution purposes (fail closed).
	OutcomeError Outcome = "error"
)

// Report is the stable JSON document printed by `aval verify --output json`.
// Additive changes only within a ReportVersion.
type Report struct {
	Version     string  `json:"version"`
	Outcome     Outcome `json:"outcome"`
	TestID      string  `json:"testId"`
	Fingerprint string  `json:"fingerprint"`
	// Reference is the attestation's digest reference, when one was found.
	Reference string     `json:"reference,omitempty"`
	Result    Result     `json:"result,omitempty"`
	Signer    *Identity  `json:"signer,omitempty"`
	Predicate *Predicate `json:"predicate,omitempty"`
	// Reason explains miss, verification-failed and error outcomes.
	Reason string `json:"reason,omitempty"`
	// Rejected lists candidate attestations for the fingerprint that were
	// not used, with why. An entry with outcome verification-failed is a
	// security event even when Outcome is verified; one with outcome miss is
	// a signer below the policy's minimum trust level, routine non-reuse.
	Rejected []Rejection `json:"rejected,omitempty"`
}

// Rejection is a candidate attestation that did not verify.
type Rejection struct {
	Reference string `json:"reference"`
	// Outcome is OutcomeVerificationFailed, OutcomeError or OutcomeMiss.
	Outcome Outcome `json:"outcome"`
	Reason  string  `json:"reason"`
}
