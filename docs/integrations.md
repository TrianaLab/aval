# Integrations

Status: pre-alpha. Milestone 1 (contract + fingerprint) is implemented.
Lookup, fetch, signature verification and the policy file land in milestone 2;
until then the code paths below return "not implemented" (exit code 6). The
consumption contract is scheduled to be declared stable in milestone 5. All
media types, the predicate type and the report version are **provisional**
until then.

## Consuming Aval evidence

Aval produces signed test execution results. Other tools (CI systems, release
gates, dashboards, policy engines) may consume them. There are three surfaces
meant for that. Anything else in the repository (`internal/...`, the text
output of the CLI, the fingerprint manifest layout) is not a consumption
contract.

### 1. OCI artifact and predicate format

An execution result is an in-toto v1 Statement wrapped in a DSSE envelope,
stored in an OCI registry, found through the tag `sha256-<fingerprint hex>`
and the OCI 1.1 referrers of the manifest it points at, and consumed by
digest. The predicate type is
`https://aval.trianalab.io/attestation/execution/v1` (provisional).

See [artifact-format.md](artifact-format.md) for the media types, manifest
layout and predicate fields.

Predicate fields are neutral on purpose: `subject`, `test.id`,
`test.cacheability`, `result`, `execution.fingerprint`,
`execution.fingerprintVersion`, `execution.environment`, `execution.exitCode`,
`runner`, `startedAt`, `finishedAt`, and a free `annotations` map. Within `v1`
only additive changes are allowed.

### 2. Go package `github.com/TrianaLab/aval/pkg/evidence`

`pkg/evidence` has no dependency on the rest of the module and is the only
public Go API. It exposes:

| Symbol | Purpose |
|---|---|
| `Parse(data []byte) (*Predicate, error)` | Decode predicate JSON and run `Validate`. Does not check signatures. |
| `(*Predicate).Validate() error` | Structural checks: `schemaVersion == "v1"`, subject, `test.id`, `result`, fingerprint format `sha256:<64 hex>`, `runner.identity`, timestamps. |
| `Verify(ctx, *Envelope, VerifyOptions) (*Verification, error)` | Check the DSSE signature against accepted identities, decode the statement and predicate, enforce the policy. **Milestone-2 stub**: returns `ErrNotImplemented` today. |
| `MatchIdentity(ids, issuer, subject) (Identity, bool)` | The accepted identity that grants a signer the highest trust level. The issuer must be equal; `SubjectRegexp` must match the whole subject (anchored); an invalid regexp matches nothing. The level comes from the policy, never from the predicate. |
| `VerifyOptions` | Expected fingerprint, expected test id, accepted `Identities` (empty accepts nothing), `MinTrustLevel`, `AllowedCacheability` (empty means `hermetic` and `controlled-environment` only). |
| `ErrNotFound` | No attestation exists for the fingerprint. A miss; re-run. |
| `ErrVerification` | An attestation exists but failed signature or policy checks. Security event. `*VerificationError` wraps it with a reason. |
| `Report`, `ReportVersion` | The document printed by `aval verify --output json`. |

Trust levels: `1` developer (personal acceleration, not full trust), `2` CI
runner with verifiable identity, `3` isolated execution with verifiable
attestation of the environment. `0` (unknown) is never accepted.

Example external consumer. How the envelope bytes are fetched (ORAS, a
registry client, `aval`) is up to the consumer; fetch by digest, not by tag.

```go
package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/TrianaLab/aval/pkg/evidence"
)

// CheckResult returns the verified predicate for testID at fingerprint, or an
// error. envBytes is the DSSE envelope layer, fetched by digest.
func CheckResult(ctx context.Context, envBytes []byte, testID, fingerprint string) (*evidence.Predicate, error) {
	var env evidence.Envelope
	if err := json.Unmarshal(envBytes, &env); err != nil {
		return nil, fmt.Errorf("decoding envelope: %w", err)
	}
	v, err := evidence.Verify(ctx, &env, evidence.VerifyOptions{
		ExpectedFingerprint: fingerprint,
		ExpectedTestID:      testID,
		Identities: []evidence.Identity{{
			Issuer:        "https://token.actions.githubusercontent.com",
			SubjectRegexp: `^https://github\.com/acme/orders/\.github/workflows/ci\.yml@refs/heads/main$`,
			TrustLevel:    evidence.TrustRunner,
		}},
		MinTrustLevel: evidence.TrustRunner,
	})
	switch {
	case errors.Is(err, evidence.ErrVerification):
		// Someone published a result we do not trust. Alert, do not reuse.
		return nil, err
	case err != nil:
		// Includes ErrNotImplemented until milestone 2. Do not reuse.
		return nil, err
	}
	if v.Predicate.Result != evidence.ResultPassed {
		return nil, fmt.Errorf("test %s failed at %s", testID, fingerprint)
	}
	return v.Predicate, nil
}
```

`ErrNotFound` is returned by the lookup step (the registry client), not by
`Verify`: if there is no envelope to pass in, it is a miss.

### 3. `aval verify --output json`

`aval verify` computes the fingerprint from `aval.yaml`, looks up the
attestation, verifies it against the trust policy and runs nothing. With
`--output json` (`-o json`) it prints an `evidence.Report` on stdout; the error
message, if any, goes to stderr.

```json
{
  "version": "aval.dev/verify-report/v1alpha1",
  "outcome": "verified",
  "testId": "orders-integration",
  "fingerprint": "sha256:...",
  "reference": "ghcr.io/acme/orders-aval@sha256:...",
  "result": "passed",
  "signer": { "issuer": "...", "subjectRegexp": "...", "trustLevel": 2 },
  "predicate": { "...": "..." }
}
```

`reference`, `result`, `signer`, `predicate` and `reason` are omitted when
empty. `reason` is set for `miss`, `verification-failed` and `error`.

| Exit code | `outcome` | Meaning | What to do |
|---|---|---|---|
| 0 | `verified` | A valid attestation satisfies the policy. | Reuse `result`. |
| 4 | `miss` | No attestation for this fingerprint. | Run the test. |
| 5 | `verification-failed` | Attestation exists but failed signature or policy. | Run the test **and** alert. |
| 1, 6 | `error` (exit 1 may have no report if fingerprinting failed) | Aval could not decide (registry down, I/O, not implemented). | Run the test (fail closed). |
| 2 | (no report) | Bad flags or invalid `aval.yaml`. | Fix the invocation. |

CI snippet that keeps miss and verification failure apart:

```sh
set +e
report=$(aval verify --registry ghcr.io/acme/orders-aval --policy aval-policy.yaml -o json)
code=$?
set -e

outcome=$(jq -r '.outcome // "error"' <<<"$report")
case "$code:$outcome" in
  0:verified)
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
The policy file format (`--policy`) is defined in milestone 2.

### Guidance for consumers

- **Always verify.** `Parse` and `Validate` only check structure. A parsed
  predicate is a claim, not evidence. `runner.identity` inside the predicate is
  self-reported; the authoritative identity is the verified signer.
- **Treat `verification-failed` as a security event.** Log it, alert on it,
  never fold it into "miss" or silently re-run without a trace.
- **Fail closed.** Any outcome other than `verified` means run the test.
- **Consume by digest.** Tags are only for lookup.
- **Pin versions.** Check `schemaVersion == "v1"` on predicates (`Validate`
  does this) and `version == "aval.dev/verify-report/v1alpha1"` on reports.
  Reject versions you do not know. Within a version, ignore unknown fields:
  changes are additive.
- **Respect cacheability.** `non-deterministic` results are never reusable by
  default; do not widen that in your adapter.

### Example: Pacto as an external consumer

This is a conceptual example of an integration that lives outside Aval. Aval
does not import or depend on Pacto and knows nothing about Pacto concepts.

Pacto could consume Aval results through `pkg/evidence` or
`aval verify --output json`: verify the attestation, convert the verified
predicate into Pacto's own typed evidence, and evaluate Pacto's own
requirements against it. That adapter lives in Pacto's repository, never in
Aval. Aval will not grow Pacto-specific fields; any metadata Pacto (or any
other tool) needs to carry goes in `annotations`, using reverse-DNS keys
(for example `dev.pacto.service`). Aval never interprets `annotations`.
