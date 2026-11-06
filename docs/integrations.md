# Integrations

Status: pre-alpha. Milestone 1 (contract + fingerprint) is implemented.
Milestone 2 (sign, push, verify, `aval run` reuse or execute) is
implemented. The consumption contract is stable: the media types, the
predicate type and the report version are frozen, and the compatibility rules
are in [artifact-format.md](artifact-format.md#versioning-and-compatibility).

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
`https://aval.trianalab.io/attestation/execution/v2`.

See [artifact-format.md](artifact-format.md) for the media types, manifest
layout and predicate fields.

Predicate fields are neutral on purpose: `subject`, `test.id`,
`test.cacheability`, `result`, `execution.fingerprint`,
`execution.fingerprintVersion`, `execution.environment`, `execution.exitCode`,
`runner`, `startedAt`, `finishedAt`, and a free `annotations` map. Within `v2`
only additive changes are allowed.

### 2. Go package `github.com/TrianaLab/aval/pkg/evidence`

`pkg/evidence` has no dependency on the rest of the module and is the only
public Go API. It exposes:

| Symbol | Purpose |
|---|---|
| `Parse(data []byte) (*Predicate, error)` | Decode predicate JSON and run `Validate`. Does not check signatures. |
| `(*Predicate).Validate() error` | Structural checks: `schemaVersion == "v2"`, subject, `test.id`, `result`, fingerprint format `sha256:<64 hex>`, `runner.identity`, timestamps. |
| `Verify(ctx, *Envelope, VerifyOptions) (*Verification, error)` | Check the signature with `VerifyOptions.Signature`, match the signer against the accepted identities, enforce the minimum trust level, decode the statement and predicate, and require the expected fingerprint, test id and a reusable cacheability. Every failure is a `*VerificationError`. |
| `SignatureVerifier`, `Signer` | `VerifySignature(ctx, *Envelope) (Signer, error)` checks the signature cryptographically and returns the signer it binds (`Issuer` and `Subject` of a keyless certificate, or `KeyID`). It applies no policy. `pkg/evidence` ships no implementation, to stay free of Sigstore dependencies; Aval's own is `internal/attest.Verifier` over the Sigstore bundle. A public Sigstore implementation for consumers is planned for milestone 5. |
| `MatchIdentity(ids, issuer, subject) (Identity, bool)` | The accepted keyless identity that grants a signer the highest trust level. The issuer must be equal; `SubjectRegexp` must match the whole subject (anchored); an invalid regexp matches nothing. Entries with a `PublicKey` never match. The level comes from the policy, never from the predicate. |
| `MatchSigner(ids, Signer) (Identity, bool)` | `MatchIdentity` for keyless signers; for key-based ones, the entries whose `PublicKey` has the signer's `KeyID`. The returned identity carries the signer's real `Subject` / `KeyID`. |
| `PublicKeyID(pem) (string, error)` | `base64(sha256(DER))` of a PEM public key: the key hint in key-based Sigstore bundles. |
| `VerifyOptions` | Expected fingerprint and test id (both required), accepted `Identities` (empty accepts nothing), `MinTrustLevel` (level `0` and levels above `3` are never accepted), `AllowedCacheability` (`hermetic` and `controlled-environment` always; `external-dependencies` only if listed; `non-deterministic` never), `Signature` (required). |
| `ErrNotFound` | No attestation exists for the fingerprint. A miss; re-run. |
| `ErrVerification` | An attestation exists but failed signature or policy checks. Security event. `*VerificationError` wraps it with a reason. |
| `Report`, `ReportVersion` | The document printed by `aval verify --output json`. |

Trust levels: `1` developer (personal acceleration, not full trust), `2` CI
runner with verifiable identity, `3` isolated execution with verifiable
attestation of the environment. `0` (unknown) is never accepted.

A complete external consumer lives in
[`examples/evidence-consumer`](../examples/evidence-consumer/consumer.go): its
own Go module that imports only `pkg/evidence` and the standard library,
brings its own `SignatureVerifier` (ed25519 over the DSSE encoding), keeps a
miss apart from a verification failure, and reads `aval verify` reports. CI
builds and tests it against the checked-out `pkg/evidence` (`make example`),
and `tests/architecture` fails if it imports any other Aval package.

The shorter sketch below shows the same flow. How the envelope bytes are
fetched (ORAS, a registry client, `aval`) is up to the consumer; fetch by
digest, not by tag.

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
// error. envBytes is the DSSE envelope layer, fetched by digest; sig checks
// its signature, typically with sigstore-go over the bundle layer.
func CheckResult(ctx context.Context, envBytes []byte, sig evidence.SignatureVerifier, testID, fingerprint string) (*evidence.Predicate, error) {
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
		Signature:     sig,
	})
	switch {
	case errors.Is(err, evidence.ErrVerification):
		// Someone published a result we do not trust. Alert, do not reuse.
		return nil, err
	case err != nil:
		// Not reachable from Verify today, but fail closed. Do not reuse.
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

#### Go API compatibility

`pkg/evidence` follows semantic versioning as part of the module
`github.com/TrianaLab/aval`. Within a major version its exported API changes
additively only: new identifiers, new struct fields that keep a comparable
struct comparable, new methods on concrete types. Removing or renaming an
exported identifier, changing a type, a field type, a function or method
signature or a receiver, making a struct non-comparable, or adding a method
to an exported interface is a breaking change and needs a new major version, which
in Go means a new module path (`github.com/TrianaLab/aval/v2`). The
release that ships it is tagged v2.0.0 or later (Go rejects a `/v2` module
tagged v0 or v1), so its changeset sets the version explicitly rather than
relying on a `major` bump from 0.x.

This is enforced, not only documented. The exported surface is snapshotted in
[`pkg/evidence/testdata/api-v1.txt`](../pkg/evidence/testdata/api-v1.txt),
named after the major version in `go.mod`: one line per exported constant,
variable, function, type, field (embedded fields included, with its struct
tag: a changed JSON name or `omitempty` is breaking) and method, with types,
signatures, type parameters and constraints and without parameter names
(type parameter names and the order of union terms are kept, so changing
them needs a snapshot change too). Fields promoted through embedding get a
`promoted <Type>.<Field>` line, so one that becomes ambiguous or changes
type is breaking. Methods are the full method set, promoted ones included,
with the receiver (`T` or `*T`) they need. Interfaces are one line with
their whole method set or type set. A struct that supports `==` also gets a
`comparable <Type>` line. A constant of a type the package declares
(`TrustLevel`, `Result`, `Outcome`) is rendered with its value, since
consumers compare those values: policy files write trust levels by name,
but verify reports and predicates carry them as numbers (the mapping is in
[trust-model.md](trust-model.md#trust-levels)), so renumbering `TrustRunner`
is breaking. Untyped constants (the format
identifiers) are rendered without their value (see below).

`make ci-gates` (`TestEvidenceAPIIsAppendOnly` in `tests/architecture`) fails
when:

- a line of the snapshot no longer matches the code;
- the code exports something the snapshot lacks (an additive change appends
  its lines in the same pull request);
- the exported API names an unexported type of the package (an embedded,
  returned or field type), whose members would not be rendered;
- a non-test file of `pkg/evidence` has any build constraint: a
  `//go:build` or `// +build` line (GOOS, GOARCH, cgo, a `go1.N` release tag
  or a custom tag), `import "C"`, or a `_GOOS`/`_GOARCH` file name suffix.
  The snapshot is taken on one platform and toolchain, and consumers build
  the library on any, so every file must be built everywhere;
- a line of the snapshot at the base revision is missing from the current
  snapshot, so a break cannot be hidden by editing both. The base is the merge
  base with `origin/<base branch>` on a pull request, the commit `main`
  pointed to before a push to `main` (`before` of the push event, so all the
  commits of one push are compared together), and the merge base with
  `origin/main` otherwise. In CI
  (`CI` set) an unresolvable base fails the job; locally, without
  `origin/main`, this one comparison is skipped with a log line.

A new major module path starts a new snapshot file, which has no base to
compare with.

The Go API and the wire formats are versioned separately. The predicate type,
media types, report version and `apiVersion` follow
[artifact-format.md](artifact-format.md#versioning-and-compatibility): a
breaking format change gets a new identifier. Supporting a new format version
is an additive Go change (new constants or types next to the old ones); the
values of the untyped constants are format identifiers, so changing them is
governed by the format rules rather than by the API snapshot. `Outcome`
values are both: they are in the snapshot and in the verify report format. Dropping a type
or constant a consumer may still use is a breaking Go change and needs a new
major module path, even when the format it described is retired.

### 3. `aval verify --output json`

`aval verify` computes the fingerprint from `aval.yaml`, looks up the
attestation, verifies it against the trust policy and runs nothing. With
`--output json` (`-o json`) it prints an `evidence.Report` on stdout; the error
message, if any, goes to stderr.

```json
{
  "version": "aval.trianalab.io/verify-report/v1",
  "outcome": "verified",
  "testId": "orders-integration",
  "fingerprint": "sha256:...",
  "reference": "ghcr.io/acme/orders-aval@sha256:...",
  "result": "passed",
  "signer": { "issuer": "...", "subjectRegexp": "...", "trustLevel": 2 },
  "predicate": { "...": "..." }
}
```

`reference`, `result`, `signer`, `predicate`, `reason` and `rejected` are
omitted when empty. `reason` is set for `miss`, `verification-failed` and
`error`. `rejected` lists candidate attestations that were not used, each
`{reference, outcome, reason}` with `outcome` `verification-failed`, `error`
or `miss`. A `verification-failed` entry is a security event even when the
report's `outcome` is `verified`. A `miss` entry is an authentic attestation
from a signer the policy lists below `minTrustLevel`: routine non-reuse, not
a security event.

| Exit code | `outcome` | Meaning | What to do |
|---|---|---|---|
| 0 | `verified` | A valid attestation satisfies the policy. | Reuse `result`. |
| 4 | `miss` | No attestation for this fingerprint that the policy can reuse (none found, or only signers the policy lists below `minTrustLevel`). | Run the test. |
| 5 | `verification-failed` | Attestation exists but failed signature or policy. | Run the test **and** alert. |
| 1 | `error` (may have no report if fingerprinting failed) | Aval could not decide (registry down, I/O, bad policy file, trusted root unavailable). | Run the test (fail closed). |
| 2 | (no report) | Bad flags or invalid `aval.yaml`, checked before anything runs: a `--registry` that is not an OCI repository (`ghcr.io/acme/orders-aval`, no scheme, tag or digest), `--push` or `aval push` with a `--key` that does not load or with neither a key nor `$SIGSTORE_ID_TOKEN`, a `--result` or `--report` path that holds an input of the test or, for `aval run`, any existing file that is not an Aval result or report. | Fix the invocation. |

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
The policy file format (`--policy`) is described in
[trust-model.md](trust-model.md#policy-file).

### Guidance for consumers

- **Always verify.** `Parse` and `Validate` only check structure. A parsed
  predicate is a claim, not evidence. `runner.identity` inside the predicate is
  self-reported; the authoritative identity is the verified signer.
- **Treat `verification-failed` as a security event.** Log it, alert on it,
  never fold it into "miss" or silently re-run without a trace.
- **Fail closed.** Any outcome other than `verified` means run the test.
- **Consume by digest.** Tags are only for lookup.
- **Pin versions.** Check `schemaVersion == "v2"` on predicates (`Validate`
  does this) and `version == "aval.trianalab.io/verify-report/v1"` on reports.
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
