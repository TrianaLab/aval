// Package evidence defines the public, versioned format of Aval execution
// attestations and the functions external tools use to consume them.
//
// This package is the stable integration surface of Aval. It has no
// dependency on the rest of the module and knows nothing about any specific
// consumer: external tools import it to turn an Aval attestation into
// their own evidence model. Adapters live in the consumer's repository.
//
// The identifiers below and the JSON shape of Predicate, Statement and
// Report are frozen: within a version they change additively only (see
// docs/artifact-format.md, "Versioning and compatibility"). The Go API
// follows semantic versioning: within a major version it changes additively
// only (docs/integrations.md, "Go API compatibility").
package evidence

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
)

const (
	// PredicateType identifies the Aval execution predicate inside an
	// in-toto Statement.
	PredicateType = "https://aval.trianalab.io/attestation/execution/v2"

	// MediaTypeExecution is the media type of the raw predicate JSON.
	MediaTypeExecution = "application/vnd.trianalab.aval.execution.v2+json"

	// ArtifactType is the OCI artifactType of the manifest that carries an
	// execution result.
	ArtifactType = "application/vnd.trianalab.aval.execution.v2"

	// ArtifactTypeFingerprint is the OCI artifactType of the per-fingerprint
	// anchor manifest that every execution manifest refers to (its subject).
	ArtifactTypeFingerprint = "application/vnd.trianalab.aval.fingerprint.v1"

	// ArtifactTypeOutputs is the OCI artifactType of the manifest that
	// carries the output files of one execution (Execution.Outputs). Its
	// subject is the fingerprint anchor.
	ArtifactTypeOutputs = "application/vnd.trianalab.aval.outputs.v1"

	// MediaTypeOutput is the media type of one output file layer; its
	// org.opencontainers.image.title annotation is the file's path.
	MediaTypeOutput = "application/vnd.trianalab.aval.output.v1"

	// MediaTypeDSSE is the media type of the signed DSSE envelope layer.
	MediaTypeDSSE = "application/vnd.dsse.envelope.v1+json"

	// StatementType is the in-toto Statement v1 type.
	StatementType = "https://in-toto.io/Statement/v1"

	// PayloadType is the DSSE payload type for in-toto statements.
	PayloadType = "application/vnd.in-toto+json"

	// SchemaVersion is the predicate schema version emitted by this build.
	SchemaVersion = "v2"
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
// only within PredicateType and SchemaVersion.
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
	// Outputs are the files the run wrote that a reused result restores
	// (contract outputs). Signed with the rest, so the signature covers
	// every output digest.
	Outputs *Outputs `json:"outputs,omitempty"`
}

// Outputs points at the OCI manifest (artifactType ArtifactTypeOutputs)
// that carries the output files, and lists them in its layer order.
type Outputs struct {
	// Manifest is the digest of the outputs manifest, "sha256:<hex>".
	Manifest string       `json:"manifest"`
	Files    []OutputFile `json:"files"`
}

// OutputFile is one output: its slash-separated path relative to the
// contract directory, and the digest and size of its content.
type OutputFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// validate checks the manifest digest and that every path is relative,
// clean and listed once, so a restore can never leave the contract
// directory.
func (o *Outputs) validate() error {
	if !fingerprintRe.MatchString(o.Manifest) {
		return fmt.Errorf("execution.outputs.manifest: %q is not sha256:<64 hex>", o.Manifest)
	}
	if len(o.Files) == 0 {
		return errors.New("execution.outputs.files: at least one file is required")
	}
	seen := map[string]bool{}
	for i, f := range o.Files {
		switch {
		case !fs.ValidPath(f.Path) || f.Path == "." || strings.ContainsFunc(f.Path, func(r rune) bool { return r == '\\' || unicode.IsControl(r) }):
			return fmt.Errorf("execution.outputs.files[%d]: path %q must be clean, relative and slash-separated", i, f.Path)
		case seen[f.Path]:
			return fmt.Errorf("execution.outputs.files[%d]: path %q is listed twice", i, f.Path)
		case !fingerprintRe.MatchString(f.Digest) || f.Size < 0:
			return fmt.Errorf("execution.outputs.files[%d]: digest %q or size %d is invalid", i, f.Digest, f.Size)
		}
		seen[f.Path] = true
	}
	return nil
}

// Runner is who produced the result, as claimed inside the predicate. The
// authoritative identity is the verified signer, not this field.
type Runner struct {
	Identity string `json:"identity"`
	// Issuer is never set by Aval.
	//
	// Deprecated: the OIDC issuer of a keyless signer is in
	// Verification.Signer.Issuer, taken from the verified certificate.
	Issuer string `json:"issuer,omitempty"`
	// TrustLevel is never set by Aval (always 0) and never read by Verify.
	//
	// Deprecated: the trust level is granted by the policy; read
	// Verification.Signer.TrustLevel.
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
	if p.Execution.Outputs != nil {
		if err := p.Execution.Outputs.validate(); err != nil {
			errs = append(errs, err)
		}
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
	// TrustLevelName is TrustLevel.String() ("developer", "runner",
	// "isolated"; "unknown" only for an out-of-range level a library caller
	// passed). MatchIdentity and MatchSigner set it from the matched entry's
	// level, so it never comes from a predicate claim; any value set on
	// VerifyOptions.Identities is overwritten.
	TrustLevelName string `json:"trustLevelName,omitempty"`
	// PublicKey is a PEM-encoded public key accepted for key-based
	// signatures. An entry has either PublicKey or Issuer and
	// SubjectRegexp, never both. Not part of the verify report; KeyID
	// identifies the key there.
	PublicKey string `json:"-"`
	// Subject is the certificate subject (SAN) the verified signature
	// actually carried, and KeyID the key that verified a key-based
	// signature (see PublicKeyID). Verify sets them on Verification.Signer,
	// so the verify report shows the real signer next to the policy entry
	// it matched. They are ignored in VerifyOptions.Identities.
	Subject string `json:"subject,omitempty"`
	KeyID   string `json:"keyId,omitempty"`
}

// MatchIdentity returns the accepted identity that grants the highest trust
// level to a signer with this OIDC issuer and certificate subject. The issuer
// must be equal and non-empty, and SubjectRegexp must match the whole
// subject (it is anchored even if the policy omits ^ and $). An invalid
// regexp matches nothing. The level comes from the policy entry only, never
// from a predicate's runner claim; TrustLevelName is set from that level.
func MatchIdentity(ids []Identity, issuer, subject string) (Identity, bool) {
	var best Identity
	found := false
	for _, id := range ids {
		if issuer == "" || id.PublicKey != "" || id.Issuer != issuer || (found && id.TrustLevel <= best.TrustLevel) {
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
	if found {
		best.TrustLevelName = best.TrustLevel.String()
	}
	return best, found
}

// Signer is the identity a cryptographically valid signature binds: the
// OIDC issuer and subject of a keyless certificate, or the KeyID of a key.
type Signer struct {
	Issuer  string
	Subject string
	KeyID   string
}

// SignatureVerifier checks the signature over an envelope (and the
// certificate chain and transparency log behind it) and returns the signer
// it binds. It applies no policy: Verify matches the signer against the
// accepted identities. Aval's Sigstore implementation lives outside this
// package so that it stays dependency-light.
type SignatureVerifier interface {
	VerifySignature(ctx context.Context, env *Envelope) (Signer, error)
}

// PublicKeyID returns the key identifier of a PEM-encoded PKIX public key:
// base64(sha256(DER)), the hint Aval writes into key-based Sigstore bundles.
func PublicKeyID(pemKey string) (string, error) {
	b, _ := pem.Decode([]byte(pemKey))
	if b == nil || b.Type != "PUBLIC KEY" {
		return "", errors.New("not a PEM public key")
	}
	if _, err := x509.ParsePKIXPublicKey(b.Bytes); err != nil {
		return "", err
	}
	sum := sha256.Sum256(b.Bytes)
	return base64.StdEncoding.EncodeToString(sum[:]), nil
}

// MatchSigner returns the accepted identity that grants the highest trust
// level to a verified signer, with Subject and KeyID set to what the signer
// actually carried. A keyless signer is matched with MatchIdentity; a
// key-based one (KeyID set) only by entries whose PublicKey has that
// PublicKeyID. A signer with both a key and a certificate identity matches
// nothing. The level comes from the policy entry only.
func MatchSigner(ids []Identity, s Signer) (Identity, bool) {
	var (
		best  Identity
		found bool
	)
	switch {
	case s.KeyID == "":
		best, found = MatchIdentity(ids, s.Issuer, s.Subject)
	case s.Issuer == "" && s.Subject == "":
		for _, id := range ids {
			if kid, err := PublicKeyID(id.PublicKey); err == nil && kid == s.KeyID && (!found || id.TrustLevel > best.TrustLevel) {
				best, found = id, true
			}
		}
	}
	if !found {
		return Identity{}, false
	}
	best.Subject, best.KeyID, best.TrustLevelName = s.Subject, s.KeyID, best.TrustLevel.String()
	return best, true
}

// VerifyOptions is the trust policy applied by Verify.
type VerifyOptions struct {
	// ExpectedFingerprint must equal the predicate's execution fingerprint.
	// Required.
	ExpectedFingerprint string
	// ExpectedTestID must equal the predicate's test.id. Required.
	ExpectedTestID string
	// Identities lists accepted signers. Empty means nothing is accepted.
	Identities []Identity
	// MinTrustLevel is the minimum level required for reuse. Levels below
	// TrustDeveloper or above TrustIsolated are never accepted.
	MinTrustLevel TrustLevel
	// AllowedCacheability lists classes that may be reused besides
	// hermetic and controlled-environment. Only external-dependencies has
	// an effect; non-deterministic and unknown classes are never reused.
	AllowedCacheability []string
	// Signature checks the envelope's signature. Required: without it
	// nothing verifies.
	Signature SignatureVerifier
}

// Verification is the outcome of a successful Verify.
type Verification struct {
	Predicate *Predicate
	// Signer is the policy entry that accepted the signer, with Subject and
	// KeyID set to the identity the signature actually carried.
	Signer Identity
	// TrustLevel equals Signer.TrustLevel.
	//
	// Deprecated: use Signer.TrustLevel.
	TrustLevel TrustLevel
}

var (
	// ErrNotFound means no attestation exists for the fingerprint (a miss).
	// A miss is normal: the caller re-runs the test.
	ErrNotFound = errors.New("no attestation found for fingerprint")
	// ErrVerification means an attestation exists but failed signature or
	// policy checks. This is a security event and must be surfaced.
	ErrVerification = errors.New("attestation verification failed")
	// ErrNotImplemented marks milestone stubs (exit code 6, reserved).
	//
	// Deprecated: no function in this package or the aval CLI returns it,
	// so consumers have nothing to check it for.
	ErrNotImplemented = errors.New("not implemented")
)

// VerificationError wraps ErrVerification with a reason.
type VerificationError struct{ Reason string }

func (e *VerificationError) Error() string { return ErrVerification.Error() + ": " + e.Reason }
func (e *VerificationError) Unwrap() error { return ErrVerification }

// Verify checks a DSSE envelope's signature with opts.Signature, matches the
// signer against opts.Identities, decodes the in-toto statement and
// predicate, and enforces the minimum trust level, cacheability, expected
// fingerprint and expected test id. Any failure returns a
// *VerificationError, which wraps ErrVerification.
func Verify(ctx context.Context, env *Envelope, opts VerifyOptions) (*Verification, error) {
	v, reason := verify(ctx, env, opts)
	if reason != "" {
		return nil, &VerificationError{Reason: reason}
	}
	return v, nil
}

func verify(ctx context.Context, env *Envelope, opts VerifyOptions) (*Verification, string) {
	if opts.ExpectedFingerprint == "" || opts.ExpectedTestID == "" {
		return nil, "expected fingerprint and test id are required"
	}
	id, reason := verifySigner(ctx, env, opts)
	if reason != "" {
		return nil, reason
	}
	p, reason := verifyPayload(env.Payload, opts)
	if reason != "" {
		return nil, reason
	}
	return &Verification{Predicate: p, Signer: id, TrustLevel: id.TrustLevel}, ""
}

// verifySigner checks the signature and returns the accepted identity that
// grants the signer at least the minimum trust level.
func verifySigner(ctx context.Context, env *Envelope, opts VerifyOptions) (Identity, string) {
	if opts.Signature == nil {
		return Identity{}, "no signature verifier configured"
	}
	if env == nil || env.PayloadType != PayloadType {
		return Identity{}, "envelope: missing or not an in-toto payload"
	}
	signer, err := opts.Signature.VerifySignature(ctx, env)
	if err != nil {
		return Identity{}, "signature: " + err.Error()
	}
	id, ok := MatchSigner(opts.Identities, signer)
	if !ok {
		return Identity{}, fmt.Sprintf("signer %s is not accepted by the policy", describe(signer))
	}
	need := max(opts.MinTrustLevel, TrustDeveloper)
	if id.TrustLevel < need || id.TrustLevel > TrustIsolated {
		return Identity{}, fmt.Sprintf("signer %s has trust level %s, policy requires at least %s", describe(signer), id.TrustLevel, need)
	}
	return id, ""
}

// verifyPayload decodes the signed statement and checks the predicate
// against what the caller computed locally.
func verifyPayload(payload []byte, opts VerifyOptions) (*Predicate, string) {
	var st Statement
	if err := json.Unmarshal(payload, &st); err != nil {
		return nil, "decoding statement: " + err.Error()
	}
	if st.Type != StatementType || st.PredicateType != PredicateType {
		return nil, fmt.Sprintf("statement: type %q, predicate type %q; want %q, %q", st.Type, st.PredicateType, StatementType, PredicateType)
	}
	p, err := Parse(st.Predicate)
	if err != nil {
		return nil, "predicate: " + err.Error()
	}
	switch {
	case len(st.Subject) != 1 || st.Subject[0].Name != p.Subject.Name || !maps.Equal(st.Subject[0].Digest, p.Subject.Digest):
		return nil, "statement subject does not match the predicate subject"
	case p.Execution.Fingerprint != opts.ExpectedFingerprint:
		return nil, fmt.Sprintf("fingerprint %s, want %s", p.Execution.Fingerprint, opts.ExpectedFingerprint)
	case p.Test.ID != opts.ExpectedTestID:
		return nil, fmt.Sprintf("test id %q, want %q", p.Test.ID, opts.ExpectedTestID)
	case !reusable(p.Test.Cacheability, opts.AllowedCacheability):
		return nil, fmt.Sprintf("cacheability %q is not reusable under the policy", p.Test.Cacheability)
	}
	return p, ""
}

// reusable reports whether a cacheability class may be reused. Only the
// known reusable classes count: non-deterministic never does, whatever the
// policy lists (invariant 7), and neither does an unknown class.
func reusable(class string, allowed []string) bool {
	switch class {
	case "hermetic", "controlled-environment":
		return true
	case "external-dependencies":
		return slices.Contains(allowed, class)
	}
	return false
}

func describe(s Signer) string {
	if s.KeyID != "" {
		return "key " + s.KeyID
	}
	return fmt.Sprintf("%q (issuer %q)", s.Subject, s.Issuer)
}
