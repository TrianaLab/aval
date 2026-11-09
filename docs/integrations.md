# Consume evidence

Three surfaces are stable: the OCI artifact, the Go package `pkg/evidence`,
and `aval verify -o json`. Nothing else (internal packages, CLI text output,
the manifest layout) is a contract. The formats are frozen
([compatibility rules](artifact-format.md#versioning-and-compatibility)).

## 1. OCI artifact and predicate

A result is an in-toto v1 Statement in a DSSE envelope, found through the tag
`sha256-<fingerprint hex>` and the OCI 1.1 referrers of the manifest it points
at, and consumed by digest. Predicate type
`https://aval.trianalab.io/attestation/execution/v2`; fields and layout:
[artifact format](artifact-format.md).

## 2. Go package `github.com/TrianaLab/aval/pkg/evidence`

`pkg/evidence` has no dependency on the rest of the module and is the only
public Go API:

| Symbol | Purpose |
|---|---|
| `Parse(data []byte) (*Predicate, error)` | decode predicate JSON and run `Validate`; no signature check |
| `(*Predicate).Validate() error` | structural checks: `schemaVersion == "v2"`, subject, `test.id`, `result`, fingerprint format, `runner.identity`, timestamps |
| `Verify(ctx, *Envelope, VerifyOptions) (*Verification, error)` | check the signature, match the signer against the policy, enforce the minimum level, decode the statement, require the expected fingerprint, test id and a reusable cacheability; every failure is a `*VerificationError` |
| `SignatureVerifier`, `Signer` | checks a signature cryptographically and returns the signer it binds, with no policy. `pkg/evidence` ships no implementation, to stay free of Sigstore; a public Sigstore one is planned for milestone 5 |
| `MatchIdentity(ids, issuer, subject) (Identity, bool)` | the keyless identity that grants a signer the highest level; `SubjectRegexp` matches the whole subject; sets `TrustLevelName` from the entry's level |
| `MatchSigner(ids, Signer) (Identity, bool)` | `MatchIdentity` for keyless signers, `PublicKey` entries for key signers; sets `TrustLevelName` from the entry's level |
| `PublicKeyID(pem) (string, error)` | `base64(sha256(DER))` of a PEM public key, the key hint in key-based bundles |
| `VerifyOptions` | expected fingerprint and test id, accepted `Identities`, `MinTrustLevel`, `AllowedCacheability`, `Signature` (required) |
| `ErrNotFound` | no attestation for the fingerprint: a miss, run the test |
| `ErrVerification` | an attestation failed signature or policy checks: a security event |
| `Report`, `ReportVersion` | the document `aval verify --output json` prints |
| `RunRecord`, `RunRecordVersion`, `ParseRunRecord` | a run record ([run records](artifact-format.md#run-records)); unsigned statistics, never a basis for trust |
| `StatsReport`, `TestStats`, `RepositoryStats`, `RunStats`, `MissCauses`, `StatsReportVersion`, `StatsUnknownRepository` | the document `aval stats --output json` prints ([stats report](artifact-format.md#stats-report-json)) |

`ExpectedFingerprint` and `ExpectedTestID` come from your local contract,
never from the attestation; both are required and must equal the predicate's
`execution.fingerprint` and `test.id`. Levels are numbers in reports: 0
unknown, 1 developer, 2 runner, 3 isolated
([trust model](trust-model.md#trust-levels)).

A complete consumer that imports only `pkg/evidence`:
[Consume from Go](guides/go-consumer.md). `ErrNotFound` comes from your lookup
step, not from `Verify`.

### Go API compatibility

`pkg/evidence` follows semantic versioning as part of the module
`github.com/TrianaLab/aval`. Within a major version its exported API changes
additively only: new identifiers, new struct fields that keep a comparable
struct comparable, new methods on concrete types. Anything else (removing or
renaming, changing a type or signature, adding a method to an exported
interface) needs a new module path, `github.com/TrianaLab/aval/v2`. The
exported surface is snapshotted in
[`pkg/evidence/testdata/api-v1.txt`](https://github.com/TrianaLab/aval/blob/main/pkg/evidence/testdata/api-v1.txt)
and checked in CI.

The Go API and the wire formats are versioned separately: a breaking format
change gets a new identifier, and supporting it is an additive Go change.

## 3. `aval verify --output json`

`aval verify` computes the fingerprint, looks up the attestation, verifies it
against the trust policy and runs nothing. With `-o json` it prints the
[verify report](artifact-format.md#verify-report) on stdout; stderr adds one
`aval: SECURITY: ...` line when an attestation failed verification. Exit 2
prints no report, and neither does exit 1 when fingerprinting failed
([exit codes](cli-reference.md#exit-codes)).

CI snippet that keeps miss and verification failure apart:

```sh
set +e
report=$(aval verify --registry ghcr.io/acme/orders-aval --policy aval-policy.yaml -o json)
code=$?
set -e

outcome=$(jq -r '.outcome // "error"' <<<"$report")
case "$code:$outcome" in
  0:verified)
    if jq -e '[.rejected[]? | select(.outcome == "verification-failed")] | length > 0' <<<"$report" >/dev/null; then
      ./notify-security.sh "$report" # a rejected candidate failed verification
    fi
    echo "reusing $(jq -r .reference <<<"$report") (result: $(jq -r .result <<<"$report"))"
    [ "$(jq -r .result <<<"$report")" = passed ] || exit 1
    ;;
  4:miss)
    echo "no attestation for $(jq -r .fingerprint <<<"$report"), running test"
    ./run-tests.sh
    ;;
  5:verification-failed)
    echo "::error::untrusted attestation for $(jq -r .testId <<<"$report"): $(jq -r .reason <<<"$report")"
    ./notify-security.sh "$report"
    ./run-tests.sh
    ;;
  *)
    echo "aval could not decide (exit $code), running test"
    ./run-tests.sh
    ;;
esac
```

Check both the exit code and `outcome`; if they disagree, treat it as `error`.

## Guidance

- **Always verify.** `Parse` and `Validate` only check structure; a parsed
  predicate is a claim, and `runner.identity` is self-reported.
- **Pin versions.** Check `schemaVersion == "v2"` on predicates (`Validate`
  does this) and `version == "aval.trianalab.io/verify-report/v1"` on reports.
  Reject versions you do not know. Within a version, ignore unknown fields.
- Treat `verification-failed` as a security event, fail closed, consume by
  digest and never widen cacheability: see [fail closed](trust-model.md#fail-closed).
- **Your metadata goes in `annotations`** with reverse-DNS keys
  (`dev.pacto.service`). Aval never reads them and will not add consumer
  fields.
