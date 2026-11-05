# Artifact format

This document describes how Aval stores an execution result in an OCI
registry, the attestation it signs, and the JSON that `aval verify --output json`
prints. It is the contract external tools consume through `pkg/evidence`.

Status: pre-alpha. The Go types in `pkg/evidence` are the source of truth.
Signing, push, lookup and fetch are implemented (`attest.Sign`,
`registry.Push`, `registry.Lookup`, `registry.Fetch`) but not yet wired into
`aval push` and `aval run`; signature verification and the policy file are
milestone-2 stubs (#8).

**Every media type, the predicate type and the report version are PROVISIONAL
until v1.** They may change before then without a compatibility path.

## Identifiers

All constants live in `pkg/evidence`.

| Constant | Value | Used for | Status |
|---|---|---|---|
| `ArtifactType` | `application/vnd.trianalab.aval.execution.v1` | `artifactType` of the OCI manifest | PROVISIONAL |
| `ArtifactTypeFingerprint` | `application/vnd.trianalab.aval.fingerprint.v1` | `artifactType` of the per-fingerprint anchor manifest | PROVISIONAL |
| `MediaTypeDSSE` | `application/vnd.dsse.envelope.v1+json` | Media type of the single layer (signed DSSE envelope) | PROVISIONAL (Aval usage) |
| `MediaTypeExecution` | `application/vnd.trianalab.aval.execution.v1+json` | Media type of the raw predicate JSON, when carried on its own | PROVISIONAL |
| `PayloadType` | `application/vnd.in-toto+json` | DSSE `payloadType` | in-toto standard |
| `StatementType` | `https://in-toto.io/Statement/v1` | in-toto Statement `_type` | in-toto standard |
| `PredicateType` | `https://aval.trianalab.io/attestation/execution/v1` | in-toto `predicateType` | PROVISIONAL |
| `SchemaVersion` | `v1` | Predicate `schemaVersion` | PROVISIONAL |
| `ReportVersion` | `aval.dev/verify-report/v1alpha1` | `version` of the verify report | PROVISIONAL |

Registry identifiers (`internal/registry`): the optional bundle layer uses the
Sigstore media type `application/vnd.dev.sigstore.bundle.v0.3+json`
(`MediaTypeSigstoreBundle`), and manifests carry the annotation
`io.trianalab.aval.fingerprint` (`AnnotationFingerprint`, informational:
verification reads the fingerprint from the signed predicate).

Related identifiers from other packages: the fingerprint manifest format is
`aval-fingerprint/v1` (`internal/fingerprint`), and the contract `apiVersion`
is `aval.dev/v1alpha1` (`internal/contract`). Both are provisional too.

## OCI layout

There is no Aval server or database. The registry is the only index.

Per fingerprint there is one **anchor** manifest, and one **execution**
manifest per execution whose `subject` is the anchor, so the OCI 1.1
referrers API lists every execution for a fingerprint.

The anchor is deterministic: its bytes, and so its digest, depend only on the
fingerprint. It is tagged `sha256-<fingerprint hex>` (`registry.Tag`):

```json
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "artifactType": "application/vnd.trianalab.aval.fingerprint.v1",
  "config": {
    "mediaType": "application/vnd.oci.empty.v1+json",
    "digest": "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
    "size": 2,
    "data": "e30="
  },
  "layers": [
    {
      "mediaType": "application/vnd.oci.empty.v1+json",
      "digest": "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
      "size": 2,
      "data": "e30="
    }
  ],
  "annotations": {
    "io.trianalab.aval.fingerprint": "sha256:<fingerprint hex>"
  }
}
```

One execution = one OCI 1.1 image manifest, following the OCI 1.1 artifact
guidance (`artifactType` set, empty config):

```json
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "artifactType": "application/vnd.trianalab.aval.execution.v1",
  "config": {
    "mediaType": "application/vnd.oci.empty.v1+json",
    "digest": "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a",
    "size": 2,
    "data": "e30="
  },
  "layers": [
    {
      "mediaType": "application/vnd.dsse.envelope.v1+json",
      "digest": "sha256:<envelope digest>",
      "size": 1834
    },
    {
      "mediaType": "application/vnd.dev.sigstore.bundle.v0.3+json",
      "digest": "sha256:<bundle digest>",
      "size": 4210
    }
  ],
  "subject": {
    "mediaType": "application/vnd.oci.image.manifest.v1+json",
    "digest": "sha256:<anchor digest>",
    "size": 512
  },
  "annotations": {
    "io.trianalab.aval.fingerprint": "sha256:<fingerprint hex>"
  }
}
```

- **config**: the OCI empty descriptor (`{}`, media type
  `application/vnd.oci.empty.v1+json`).
- **layers**: the signed DSSE envelope (see below), then optionally the
  Sigstore bundle with the verification material. `registry.Fetch` rejects
  any other layer, a repeated layer, or a missing envelope as a verification
  failure.
- **subject**: always the anchor of the execution's fingerprint. The tested
  revision or artifact is recorded inside the statement (`subject`), not in
  the manifest, so the referrers of a tested image do not list its evidence.

### Push

`registry.Push` uploads the blobs, pushes the anchor and (re)tags it, then
pushes the execution manifest by digest. It never moves or replaces an
earlier execution: pushing the same bytes again is a no-op, and anything else
becomes one more referrer of the anchor. Re-tagging the deterministic anchor
also repairs a tag that was moved.

Registries without the referrers API (for example GHCR) are supported: oras
maintains the OCI referrers tag schema (`sha256-<anchor hex>` index) on push
and reads it on lookup. That index is rewritten on every push, so two concurrent
pushes for the same fingerprint can drop one entry from it. The dropped
execution is then not found (a miss, so the test runs again), never a false
hit.

### Lookup and consumption

1. Compute the fingerprint locally (`aval run --fingerprint-only`), e.g.
   `sha256:3f9a...`.
2. Resolve the tag `sha256-<fingerprint hex>` in the repository passed via
   `--registry`. It must point at the anchor digest Aval computes locally for
   that fingerprint; anything else is a verification failure (the tag was
   moved).
3. List the anchor's referrers with `artifactType`
   `application/vnd.trianalab.aval.execution.v1`. Each one is a candidate,
   identified by its manifest **digest**.
4. Fetch each candidate by digest (`repo@sha256:...`). Never fetch by tag:
   tags are mutable, digests aren't.
5. Verify each envelope and apply the trust policy (see below).

A missing tag, or an anchor without executions, is a **miss**
(`evidence.ErrNotFound`, exit code 4). An artifact that exists but is
malformed or fails signature or policy checks is a **verification failure**
(`evidence.ErrVerification`, exit code 5) and is a security event.

### Multiple attestations per fingerprint

Several executions can produce the same fingerprint (a developer run and a CI
run, or a re-run). All of them stay listed as referrers of the anchor, so a
later push cannot hide or replace an earlier one. `aval verify` verifies every
candidate: the verified one with the highest trust level wins (ties go to the
lowest digest). Candidates that fail are printed as warnings on stderr; if
none verifies, the outcome is `verification-failed` when any candidate failed
verification, otherwise `error`.

## DSSE envelope

The layer is a DSSE envelope (`evidence.Envelope`):

```json
{
  "payloadType": "application/vnd.in-toto+json",
  "payload": "<base64 of the in-toto Statement JSON>",
  "signatures": [
    { "keyid": "", "sig": "<base64 signature>" }
  ]
}
```

Signing uses sigstore-go (`attest.Sign`, keyless with a Fulcio certificate
and Rekor entry, or key-based; see [trust-model.md](trust-model.md#signing)).
It also produces a Sigstore bundle (`application/vnd.dev.sigstore.bundle.v0.3+json`)
carrying the certificate or key hint and the Rekor entry; it is stored as the
optional second layer of the execution manifest. Verification is milestone 2
(#8); `evidence.Verify` returns `ErrNotImplemented` today.

## in-toto Statement

The envelope payload is an in-toto Statement v1 (`evidence.Statement`). Its
`subject` is the tested revision or artifact, the same one recorded in
`predicate.subject`.

```json
{
  "_type": "https://in-toto.io/Statement/v1",
  "subject": [
    {
      "name": "git+https://github.com/acme/orders",
      "digest": { "gitCommit": "9b2c1e0f4a7d3b5c6e8f9a0b1c2d3e4f5a6b7c8d" }
    }
  ],
  "predicateType": "https://aval.trianalab.io/attestation/execution/v1",
  "predicate": { "...": "see below" }
}
```

For an image, use `"digest": {"sha256": "<hex>"}` with the image reference as
`name`.

## Predicate

`evidence.Predicate`, JSON tags exactly as below. Required fields are the ones
checked by `Predicate.Validate` (called by `evidence.Parse`).

| Field | Type | Required | Meaning |
|---|---|---|---|
| `schemaVersion` | string | yes, must be `v1` | Predicate schema version. |
| `subject` | object | yes | Tested revision/artifact. |
| `subject.name` | string | yes | e.g. `git+https://github.com/acme/orders` or an image reference. |
| `subject.digest` | map string to string | yes, non-empty | e.g. `{"gitCommit": "..."}` or `{"sha256": "..."}`. |
| `test` | object | | Test identity: *what* was checked, independent of inputs. |
| `test.id` | string | yes | `metadata.name` from `aval.yaml`. |
| `test.cacheability` | string | | Class declared in the contract: `hermetic`, `controlled-environment`, `external-dependencies`, `non-deterministic`. |
| `result` | string | yes | `passed` or `failed`. |
| `execution` | object | | This run: its inputs and conditions. |
| `execution.fingerprint` | string | yes, `sha256:<64 hex>` | Fingerprint over the declared inputs. |
| `execution.fingerprintVersion` | string | | Manifest format, e.g. `aval-fingerprint/v1`. |
| `execution.environment` | map string to string | no (omitted when empty) | Conditions of the run (toolchain, OS/arch). Only keys declared under `environment.toolchain` in `aval.yaml` are part of the fingerprint; anything else here is informational. |
| `execution.exitCode` | integer | | Exit code of `runner.command`. |
| `runner` | object | | Who produced the result, **as claimed**. |
| `runner.identity` | string | yes | Claimed producer identity. |
| `runner.issuer` | string | no (omitted when empty) | Claimed identity issuer, e.g. an OIDC issuer URL. |
| `runner.trustLevel` | integer | | `0` unknown, `1` developer, `2` runner, `3` isolated. |
| `startedAt` | RFC 3339 timestamp | yes | Execution start. |
| `finishedAt` | RFC 3339 timestamp | yes, not before `startedAt` | Execution end. |
| `annotations` | map string to string | no (omitted when empty) | Free third-party metadata. Aval never reads it. |

`runner.identity`, `runner.issuer` and `runner.trustLevel` are claims written
by whoever produced the predicate. The authoritative identity and trust level
are those of the **verified signer**, matched against the policy's accepted
identities (`evidence.Verification.Signer` / `.TrustLevel`). Consumers must not
make trust decisions from the `runner` block.

`evidence.Parse` only checks structure. A parsed predicate is a claim, not
evidence; use `evidence.Verify` (milestone 2) to get something you can rely on.

### Annotations

Keys use reverse-DNS of the owner, e.g. `com.example.team`,
`com.acme.ticket`. Aval does not define, validate or interpret any key. Do not
put secrets there: the predicate is published to the registry.

### Example

```json
{
  "schemaVersion": "v1",
  "subject": {
    "name": "git+https://github.com/acme/orders",
    "digest": { "gitCommit": "9b2c1e0f4a7d3b5c6e8f9a0b1c2d3e4f5a6b7c8d" }
  },
  "test": {
    "id": "orders-integration",
    "cacheability": "controlled-environment"
  },
  "result": "passed",
  "execution": {
    "fingerprint": "sha256:3f9a7c2e1b4d5f60718293a4b5c6d7e8f9012a3b4c5d6e7f8091a2b3c4d5e6f7",
    "fingerprintVersion": "aval-fingerprint/v1",
    "environment": {
      "go": "1.23.4",
      "os": "linux",
      "arch": "amd64"
    },
    "exitCode": 0
  },
  "runner": {
    "identity": "https://github.com/acme/orders/.github/workflows/ci.yaml@refs/heads/main",
    "issuer": "https://token.actions.githubusercontent.com",
    "trustLevel": 2
  },
  "startedAt": "2026-11-05T10:12:03Z",
  "finishedAt": "2026-11-05T10:31:47Z",
  "annotations": {
    "com.acme.team": "payments"
  }
}
```

## Verify report

`aval verify --output json` prints one `evidence.Report` to stdout. This is the
stable machine interface for CI scripts and external tools.

| Field | Type | Present | Meaning |
|---|---|---|---|
| `version` | string | always | `aval.dev/verify-report/v1alpha1`. |
| `outcome` | string | always | See outcomes below. |
| `testId` | string | always | `metadata.name` from the contract. |
| `fingerprint` | string | always | Locally computed `sha256:<hex>`. |
| `reference` | string | when an attestation was found | Immutable `repo@sha256:<manifest digest>`. |
| `result` | string | `verified` only | `passed` or `failed` from the verified predicate. |
| `signer` | object | `verified` only | Accepted policy identity that matched: `issuer`, `subjectRegexp`, `trustLevel`. |
| `predicate` | object | `verified` only | Full verified predicate. |
| `reason` | string | non-`verified` outcomes | Human-readable cause. |

Outcomes and exit codes:

| `outcome` | Meaning | What the caller does | Exit code |
|---|---|---|---|
| `verified` | Valid attestation satisfies the policy. | Reuse `result`. | 0 |
| `miss` | No attestation for this fingerprint. | Run the test. | 4 |
| `verification-failed` | Attestation exists but fails signature or policy. | Run the test **and** alert. Never hide this. | 5 |
| `error` | Aval could not decide (registry unreachable, bad policy file, not implemented). | Treat as a miss: run the test (fail closed). | 1, or 6 while stubbed |

If the fingerprint cannot be computed (empty scope, missing dependency,
symlink, control characters in a path, I/O error), `aval verify` prints no
report and exits 1; bad flags or an invalid contract print no report and exit 2.

Without `--policy`, `aval verify` skips the lookup and reports `outcome: "miss"`
(exit 4). With `--policy`, it currently ends in `outcome: "error"` with exit
code 6 because `policy.Load` and `evidence.Verify` are milestone-2 stubs.

Example (`verified`):

```json
{
  "version": "aval.dev/verify-report/v1alpha1",
  "outcome": "verified",
  "testId": "orders-integration",
  "fingerprint": "sha256:3f9a7c2e1b4d5f60718293a4b5c6d7e8f9012a3b4c5d6e7f8091a2b3c4d5e6f7",
  "reference": "ghcr.io/acme/orders-aval@sha256:8c1d4e5f60718293a4b5c6d7e8f9012a3b4c5d6e7f8091a2b3c4d5e6f7a8b9c0",
  "result": "passed",
  "signer": {
    "issuer": "https://token.actions.githubusercontent.com",
    "subjectRegexp": "^https://github.com/acme/orders/\\.github/workflows/ci\\.yaml@refs/heads/main$",
    "trustLevel": 2
  },
  "predicate": { "...": "as in the predicate example" }
}
```

Example (`miss`):

```json
{
  "version": "aval.dev/verify-report/v1alpha1",
  "outcome": "miss",
  "testId": "orders-integration",
  "fingerprint": "sha256:3f9a7c2e1b4d5f60718293a4b5c6d7e8f9012a3b4c5d6e7f8091a2b3c4d5e6f7",
  "reason": "no attestation found for fingerprint"
}
```

## Versioning and compatibility

- Within predicate `v1` (`PredicateType` `.../execution/v1`, `schemaVersion`
  `v1`): additive changes only. New optional fields may appear; existing
  fields keep their name, JSON tag, type and meaning. Consumers must ignore
  unknown fields.
- A breaking change (rename, removal, type or semantic change) requires a new
  predicate type (`.../execution/v2`), a new `MediaTypeExecution` and
  `ArtifactType` (`...execution.v2...`) and a new `schemaVersion`. Old and new
  can coexist in the same repository.
- The verify report follows the same rule per `ReportVersion`: additive within
  `v1alpha1`, new version string for anything breaking.
- A change to what the fingerprint covers bumps `aval-fingerprint/vN`. Old
  attestations then stop matching, which is a miss, not a false hit.
- Until Epic 5 freezes them, the identifiers themselves are provisional and
  may be renamed. Even then, a breaking change always gets a new identifier,
  and the fingerprint version bump applies from day one: it is a safety rule,
  not a compatibility promise.
