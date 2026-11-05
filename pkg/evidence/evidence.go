// Package evidence defines the public, versioned format of Aval execution
// attestations and the functions external tools use to consume them.
//
// This package is the stable integration surface of Aval. It has no
// dependency on the rest of the module and knows nothing about any specific
// consumer: external tools import it to turn an Aval attestation into
// their own evidence model. Adapters live in the consumer's repository.
//
// All media types and the predicate type are PROVISIONAL until v1.
package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

const (
	// PredicateType identifies the Aval execution predicate inside an
	// in-toto Statement. Provisional.
	PredicateType = "https://aval.trianalab.io/attestation/execution/v1"

	// MediaTypeExecution is the media type of the raw predicate JSON.
	// Provisional.
	MediaTypeExecution = "application/vnd.trianalab.aval.execution.v1+json"

	// ArtifactType is the OCI artifactType of the manifest that carries an
	// execution result. Provisional.
	ArtifactType = "application/vnd.trianalab.aval.execution.v1"

	// MediaTypeDSSE is the media type of the signed DSSE envelope layer.
	MediaTypeDSSE = "application/vnd.dsse.envelope.v1+json"

	// StatementType is the in-toto Statement v1 type.
	StatementType = "https://in-toto.io/Statement/v1"

	// PayloadType is the DSSE payload type for in-toto statements.
	PayloadType = "application/vnd.in-toto+json"

	// SchemaVersion is the predicate schema version emitted by this build.
	SchemaVersion = "v1"
)

// Result is the outcome of a test execution.
type Result string

const (
	ResultPassed Result = "passed"
	ResultFailed Result = "failed"
)

// TrustLevel ranks how much a signer's claim is worth. Higher is stronger.
type TrustLevel int

const (
	// TrustUnknown means the level could not be established. Never accepted.
	TrustUnknown TrustLevel = 0
	// TrustDeveloper is a local developer signature: personal acceleration,
	// not full trust.
	TrustDeveloper TrustLevel = 1
	// TrustRunner is a CI runner with a verifiable workload identity.
	TrustRunner TrustLevel = 2
	// TrustIsolated is an isolated execution with verifiable attestation of
	// the environment itself.
	TrustIsolated TrustLevel = 3
)

func (l TrustLevel) String() string {
	switch l {
	case TrustDeveloper:
		return "developer"
	case TrustRunner:
		return "runner"
	case TrustIsolated:
		return "isolated"
	}
	return "unknown"
}

// Predicate is the Aval execution predicate (predicateType PredicateType).
// Field names and JSON tags are part of the stable contract: additive changes
// only within v1.
type Predicate struct {
	SchemaVersion string    `json:"schemaVersion"`
	Subject       Subject   `json:"subject"`
	Test          Test      `json:"test"`
	Result        Result    `json:"result"`
	Execution     Execution `json:"execution"`
	Runner        Runner    `json:"runner"`
	StartedAt     time.Time `json:"startedAt"`
	FinishedAt    time.Time `json:"finishedAt"`
	// Annotations is a free map for third-party metadata. Aval never
	// interprets it. Use reverse-DNS keys (e.g. "com.example.team").
	Annotations map[string]string `json:"annotations,omitempty"`
}

// Subject is the revision or artifact that was tested, e.g. a git commit
// (Name "git+https://github.com/org/repo", Digest {"gitCommit": "..."}) or
// an image (Digest {"sha256": "..."}).
type Subject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

// Test is the test identity: *what* was checked, independent of inputs.
type Test struct {
	ID string `json:"id"`
	// Cacheability is the class declared in the contract.
	Cacheability string `json:"cacheability"`
}

// Execution describes *this* run: its inputs and conditions.
type Execution struct {
	// Fingerprint is "sha256:<hex>" over the declared inputs.
	Fingerprint string `json:"fingerprint"`
	// FingerprintVersion is the manifest format, e.g. "aval-fingerprint/v1".
	FingerprintVersion string `json:"fingerprintVersion"`
	// Environment holds declared conditions (toolchain, OS/arch).
	Environment map[string]string `json:"environment,omitempty"`
	ExitCode    int               `json:"exitCode"`
}

// Runner is who produced the result, as claimed inside the predicate. The
// authoritative identity is the verified signer, not this field.
type Runner struct {
	Identity   string     `json:"identity"`
	Issuer     string     `json:"issuer,omitempty"`
	TrustLevel TrustLevel `json:"trustLevel"`
}

// Statement is an in-toto v1 Statement carrying a Predicate.
type Statement struct {
	Type          string           `json:"_type"`
	Subject       []ResourceDigest `json:"subject"`
	PredicateType string           `json:"predicateType"`
	Predicate     json.RawMessage  `json:"predicate"`
}

// ResourceDigest is an in-toto ResourceDescriptor reduced to name + digest.
type ResourceDigest struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

// Envelope is a DSSE envelope.
type Envelope struct {
	PayloadType string      `json:"payloadType"`
	Payload     []byte      `json:"payload"` // base64 in JSON
	Signatures  []Signature `json:"signatures"`
}

// Signature is a DSSE signature.
type Signature struct {
	KeyID string `json:"keyid,omitempty"`
	Sig   []byte `json:"sig"`
}

var fingerprintRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Parse decodes and structurally validates predicate JSON. It does NOT check
// signatures: a parsed predicate is a claim, not evidence. Use Verify.
func Parse(data []byte) (*Predicate, error) {
	var p Predicate
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("decoding predicate: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate checks required fields.
func (p *Predicate) Validate() error {
	var errs []error
	if p.SchemaVersion != SchemaVersion {
		errs = append(errs, fmt.Errorf("schemaVersion: got %q, want %q", p.SchemaVersion, SchemaVersion))
	}
	if p.Subject.Name == "" || len(p.Subject.Digest) == 0 {
		errs = append(errs, errors.New("subject: name and digest are required"))
	}
	if p.Test.ID == "" {
		errs = append(errs, errors.New("test.id: required"))
	}
	if p.Result != ResultPassed && p.Result != ResultFailed {
		errs = append(errs, fmt.Errorf("result: unknown value %q", p.Result))
	}
	if !fingerprintRe.MatchString(p.Execution.Fingerprint) {
		errs = append(errs, fmt.Errorf("execution.fingerprint: %q is not sha256:<64 hex>", p.Execution.Fingerprint))
	}
	if p.Runner.Identity == "" {
		errs = append(errs, errors.New("runner.identity: required"))
	}
	if p.StartedAt.IsZero() || p.FinishedAt.Before(p.StartedAt) {
		errs = append(errs, errors.New("startedAt/finishedAt: missing or inverted"))
	}
	return errors.Join(errs...)
}

// Identity is an accepted signer identity (Sigstore keyless style).
type Identity struct {
	// Issuer is the OIDC issuer, e.g. "https://token.actions.githubusercontent.com".
	Issuer string `json:"issuer"`
	// SubjectRegexp matches the certificate subject (SAN).
	SubjectRegexp string `json:"subjectRegexp"`
	// TrustLevel granted to signatures from this identity.
	TrustLevel TrustLevel `json:"trustLevel"`
}

// MatchIdentity returns the accepted identity that grants the highest trust
// level to a signer with this OIDC issuer and certificate subject. The issuer
// must be equal and non-empty, and SubjectRegexp must match the whole
// subject (it is anchored even if the policy omits ^ and $). An invalid
// regexp matches nothing. The level comes from the policy entry only, never
// from a predicate's runner claim.
func MatchIdentity(ids []Identity, issuer, subject string) (Identity, bool) {
	var best Identity
	found := false
	for _, id := range ids {
		if issuer == "" || id.Issuer != issuer || (found && id.TrustLevel <= best.TrustLevel) {
			continue
		}
		// Compile the policy regexp alone first: wrapping an unbalanced one
		// such as "a)|(b" would compile but escape the anchors.
		if _, err := regexp.Compile(id.SubjectRegexp); err != nil {
			continue
		}
		re, err := regexp.Compile(`^(?:` + id.SubjectRegexp + `)$`)
		if err != nil || !re.MatchString(subject) {
			continue
		}
		best, found = id, true
	}
	return best, found
}

// VerifyOptions is the trust policy applied by Verify.
type VerifyOptions struct {
	// ExpectedFingerprint must equal the predicate's execution fingerprint.
	ExpectedFingerprint string
	// ExpectedTestID must equal the predicate's test.id.
	ExpectedTestID string
	// Identities lists accepted signers. Empty means nothing is accepted.
	Identities []Identity
	// MinTrustLevel is the minimum level required for reuse.
	MinTrustLevel TrustLevel
	// AllowedCacheability lists classes that may be reused. Empty means
	// hermetic and controlled-environment only.
	AllowedCacheability []string
}

// Verification is the outcome of a successful Verify.
type Verification struct {
	Predicate  *Predicate
	Signer     Identity
	TrustLevel TrustLevel
}

var (
	// ErrNotFound means no attestation exists for the fingerprint (a miss).
	// A miss is normal: the caller re-runs the test.
	ErrNotFound = errors.New("no attestation found for fingerprint")
	// ErrVerification means an attestation exists but failed signature or
	// policy checks. This is a security event and must be surfaced.
	ErrVerification = errors.New("attestation verification failed")
	// ErrNotImplemented marks milestone stubs.
	ErrNotImplemented = errors.New("not implemented")
)

// VerificationError wraps ErrVerification with a reason.
type VerificationError struct{ Reason string }

func (e *VerificationError) Error() string { return ErrVerification.Error() + ": " + e.Reason }
func (e *VerificationError) Unwrap() error { return ErrVerification }

// Verify checks a DSSE envelope's signature against opts.Identities, decodes
// the in-toto statement and predicate, and enforces opts. Any failure returns
// an error wrapping ErrVerification.
//
// TODO(milestone-2): verify with sigstore-go (Fulcio cert chain, Rekor
// inclusion), then apply fingerprint, test id, cacheability and trust level.
func Verify(ctx context.Context, env *Envelope, opts VerifyOptions) (*Verification, error) {
	return nil, fmt.Errorf("evidence.Verify: %w", ErrNotImplemented)
}
