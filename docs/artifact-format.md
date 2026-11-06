# Artifact format

This document describes how Aval stores an execution result in an OCI
registry, the attestation it signs, and the JSON that `aval verify --output json`
prints. It is the contract external tools consume through `pkg/evidence`.

Status: pre-alpha. The Go types in `pkg/evidence` are the source of truth.
Signing, push, lookup, fetch, verification and the policy file are
implemented (`attest.Sign`, `registry.Push`, `registry.Lookup`,
`registry.Fetch`, `evidence.Verify`, `policy.Load`). `aval verify`,
`aval run` and `aval push` use them.

The identifiers below and the JSON shape of the statement, predicate and
verify report are **frozen**. Within a version they change additively only;
see [Versioning and compatibility](#versioning-and-compatibility).

## Identifiers

All constants live in `pkg/evidence`.

| Constant | Value | Used for | Status |
|---|---|---|---|
| `ArtifactType` | `application/vnd.trianalab.aval.execution.v2` | `artifactType` of the OCI manifest | frozen |
| `ArtifactTypeFingerprint` | `application/vnd.trianalab.aval.fingerprint.v1` | `artifactType` of the per-fingerprint anchor manifest | frozen |
| `MediaTypeDSSE` | `application/vnd.dsse.envelope.v1+json` | Media type of the signed DSSE envelope layer | DSSE standard |
| `MediaTypeExecution` | `application/vnd.trianalab.aval.execution.v2+json` | Media type of the raw predicate JSON, when carried on its own | frozen |
| `PayloadType` | `application/vnd.in-toto+json` | DSSE `payloadType` | in-toto standard |
| `StatementType` | `https://in-toto.io/Statement/v1` | in-toto Statement `_type` | in-toto standard |
| `PredicateType` | `https://aval.trianalab.io/attestation/execution/v2` | in-toto `predicateType` | frozen |
| `SchemaVersion` | `v2` | Predicate `schemaVersion` | frozen |
| `ReportVersion` | `aval.trianalab.io/verify-report/v1` | `version` of the verify report | frozen |
| `RunRecordVersion` | `aval.trianalab.io/run-record/v1` | `version` of a run record | provisional |
| `ArtifactTypeRun` | `application/vnd.trianalab.aval.run.v1` | `artifactType` of a run record manifest | provisional |
| `MediaTypeRun` | `application/vnd.trianalab.aval.run.v1+json` | Media type of the run record layer | provisional |
| `ArtifactTypeStats` | `application/vnd.trianalab.aval.stats.v1` | `artifactType` of the per-test stats anchor manifest | provisional |
| `StatsReportVersion` | `aval.trianalab.io/stats-report/v1` | `version` of the `aval stats --output json` document | provisional |

Registry identifiers (`internal/registry`): the bundle layer uses the
Sigstore media type `application/vnd.dev.sigstore.bundle.v0.3+json`
(`MediaTypeSigstoreBundle`), and manifests carry the annotation
`io.trianalab.aval.fingerprint` (`AnnotationFingerprint`, informational:
verification reads the fingerprint from the signed predicate). The
annotation key is frozen. The stats anchor carries `io.trianalab.aval.test`
(`AnnotationTest`, provisional) with the test id.

Related identifiers from other packages: the fingerprint manifest format is
`aval-fingerprint/v1` (`internal/fingerprint`), and the contract and policy
`apiVersion` is `aval.trianalab.io/v1` (`internal/contract`, see
[contract.md](contract.md#versioning)). Both are frozen. The fingerprint
manifest version is a format tag, not a namespaced identifier: it names the
canonical manifest layout and hash, not a resource, so it stays outside
`aval.trianalab.io` and is frozen as `aval-fingerprint/v1`; a breaking change
mints `aval-fingerprint/v2`.

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
  "artifactType": "application/vnd.trianalab.aval.execution.v2",
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
- **layers**: the signed DSSE envelope (see below), then the Sigstore
  bundle with the verification material. `registry.Push` refuses an
  attestation whose bundle is missing or is not valid JSON. If
  `registry.Fetch` reads a manifest without a bundle layer, the attestation
  can never verify, so it ends as a verification failure.
  `registry.Fetch` rejects any other layer, a repeated layer, or a missing
  envelope as a verification failure, and so a manifest that is not
  sha256-addressed, is larger than 4 MiB, or does not match the digest it
  was fetched by.
- **subject**: always the anchor of the execution's fingerprint. The tested
  revision or artifact is recorded inside the statement (`subject`), not in
  the manifest, so the referrers of a tested image do not list its evidence.

The `execution.v2` identifiers replace `execution.v1`, whose manifest had the
tested artifact as an optional `subject` and exactly one layer. Aval no
longer reads or writes v1: there is no accept window, and a v1 artifact is
never a candidate.

### Push

`registry.Push` uploads the blobs, pushes the anchor and (re)tags it, then
pushes the execution manifest by digest. It never moves or replaces an
earlier execution: pushing the same bytes again is a no-op, and anything else
becomes one more referrer of the anchor. Re-tagging the deterministic anchor
also repairs a tag that was moved.

Registries without the referrers API (for example GHCR) are supported: oras
maintains the OCI referrers tag schema (`sha256-<anchor hex>` index) on push
and reads it on lookup. That index is rewritten on every push, so two concurrent
pushes for the same fingerprint can drop one entry from it. Push therefore
lists the anchor's referrers after pushing and returns only once the new
digest is among them; if it is not, it pushes the manifest again (which
re-adds it to the index) and lists again, three attempts in total, one second
apart. If the digest is still not listed, `aval push` and `aval run --push`
fail (exit 1). A registry that indexes referrers asynchronously for longer
than about two seconds also fails the push; rerun it. Once a fingerprint has more than 64 attestations the
confirmation listing fails too, so push exits 1. A dropped execution is never
a candidate, so even then it never makes an unsigned or untrusted result
reusable. Aval does not
delete the previous referrers index after rewriting it, so pushing does not
need DELETE rights.

### Lookup and consumption

1. Compute the fingerprint locally (`aval run --fingerprint-only`), e.g.
   `sha256:3f9a...`.
2. Resolve the tag `sha256-<fingerprint hex>` in the repository passed via
   `--registry`. It must point at the anchor digest Aval computes locally for
   that fingerprint; anything else is a verification failure (the tag was
   moved).
3. List the anchor's referrers with `artifactType`
   `application/vnd.trianalab.aval.execution.v2`. Each one is a candidate,
   identified by its manifest **digest**, even when the referrer entry is
   malformed (not an image manifest, not sha256): Fetch then rejects it as a
   verification failure, so it never turns into a miss.
4. Fetch each candidate by digest (`repo@sha256:...`). Never fetch by tag:
   tags are mutable, digests aren't.
5. Verify each envelope and apply the trust policy (see below).

Lookup accepts at most 64 candidates per fingerprint and makes at most 64
requests to list referrers, whatever paths the registry's `Link` headers
point at. Above either limit it fails with outcome `error` (run the
test), so whoever can push, or a buggy registry, cannot make `aval verify`
fetch an unbounded number of manifests or pages.

A listed candidate whose manifest the registry answers with 404 (deleted,
for example by an "untagged manifests" retention policy) is absent: it is
skipped with a warning on stderr and is not part of `rejected`. If every
candidate is absent, the outcome is a miss. Absence never hides a
verification failure of another candidate.

Every registry call of one command runs under `--registry-timeout` (default
`1m`): lookup and every fetch share one deadline, and push has its own. A
registry that stalls past it is outcome `error` (exit 1), so `aval run` runs
the test; `runner.command` is never under this deadline. One credential store
and one auth token cache are shared by all registry calls of a process.

A missing tag, or an anchor without executions, is a **miss**
(`evidence.ErrNotFound`, exit code 4). An artifact that exists but is
malformed or fails signature or policy checks is a **verification failure**
(`evidence.ErrVerification`, exit code 5) and is a security event.

### Registry credentials

Aval reads registry credentials the way Docker does: from
`$DOCKER_CONFIG/config.json`, or `~/.docker/config.json` when `DOCKER_CONFIG`
is not set, including the credential helpers it names (`credsStore`,
`credHelpers`, for example `docker-credential-osxkeychain` or
`docker-credential-ecr-login`). Log in with `docker login <registry>` (or
`oras login`, which writes the same file). Without an entry for the registry,
Aval connects anonymously, which is enough to read a public repository.
Every registry call runs under `--registry-timeout` (default `1m`; see
[Lookup and consumption](#lookup-and-consumption)).

A config file that cannot be parsed, or a helper that fails, is outcome
`error` (exit 1): `aval run` runs the test and nothing is reused. Loopback
registries (`localhost`, `127.0.0.1`, `::1`) use plain HTTP; every other
registry uses HTTPS.

### Multiple attestations per fingerprint

Several executions can produce the same fingerprint (a developer run and a CI
run, or a re-run). All of them stay listed as referrers of the anchor, so a
later push cannot hide or replace an earlier one. `aval verify` verifies every
candidate: the verified one with the highest trust level wins (ties go to a
non-passed result, then the lowest digest). Candidates that are not used are
listed in the report's `rejected` field; when any of them failed verification,
stderr gets one `aval: SECURITY: ...` line. A candidate
that cannot be fetched or checked (outcome `error`) might have outranked the
verified one, so it blocks reuse and the test runs. A candidate that passes
every check but whose signer the policy lists below `minTrustLevel` is
rejected as `miss`: it was fully evaluated, so it does not block a verified
sibling. When nothing is reused, the outcome is `verification-failed` when
any candidate failed verification, otherwise `error` when any could not be
checked, otherwise `miss`.

### The 64-candidate cap and cleanup

Every push adds one execution, even for the same inputs: the hostname and
timestamps in the predicate differ. Aval never deletes one. Once a
fingerprint has more than 64 executions, every lookup for it is outcome
`error`: the test always runs. Every push for it still adds an execution,
then fails its confirmation and exits 1. It stays
that way until someone deletes executions. Other fingerprints are not
affected, so the next change to an input also ends it.

To stay below the cap, push from one place (for example CI on the main
branch). `aval run --push` does not push after it reuses a result, but
`aval run --shadow --push` runs and pushes every time.

To clean up a fingerprint, list its executions and delete the ones you no
longer need, for example with [ORAS](https://oras.land):

```sh
aval run --fingerprint-only            # prints sha256:<hex>
oras discover --artifact-type application/vnd.trianalab.aval.execution.v2 <repo>:sha256-<hex>
oras manifest delete <repo>@sha256:<execution digest>
```

The registry must allow deletes (`registry:2` needs
`REGISTRY_STORAGE_DELETE_ENABLED=true`; on GHCR, delete the package
versions). Deleting executions is safe: it can make a result stop being
reusable, never make an untrusted one reusable. Keep the one you still want
to reuse, and keep any execution that failed verification until you have
looked into it.

## Run records

`aval run --record` publishes one run record per run, for every outcome
(`verified`, `miss`, `error`, `verification-failed`), so reuse can be
measured from the registry alone. `--record-file <path>` writes the same
record to a file, and `aval record --record-file <path>` publishes it later,
for example from a CI job that has write credentials but runs no tests.

Records are **statistics, never evidence**. They are unsigned and
informational. `aval run` and `aval verify` never fetch or read them, so a
record, forged or not, cannot cause reuse, change an outcome or count toward
the 64-candidate cap.

Recording is best-effort: a record that cannot be published (no write
credentials, registry down, past `--registry-timeout`) or written prints
`aval: warning: run record not ...` on stderr and never changes the exit
code. Publishing has its own `--registry-timeout` deadline, so it cannot
hold the command longer. `--record` needs `--registry`. Aval keeps no local
state; a record file exists only when `--record-file` asks for one. An
existing file at `--record-file` that is not an Aval result, report or valid
run record is never overwritten: the run warns and goes on, and inside the
contract scope that file stays an input of the fingerprint. A `--record-file`
that is also `--result` or `--report` is not written either.

A run that stops before the lookup publishes no record: bad flags, an
invalid `aval.yaml`, an output path that would clobber an input (exit 2),
and `--fingerprint-only`. So does `--explain` when the fingerprint cannot be
computed.

### Layout

Per test there is one **stats anchor**, separate from every fingerprint
anchor. It is deterministic (its digest depends on the test id alone) and
tagged `stats-<test id>` (`registry.StatsTag`); fingerprint tags and OCI
referrers tag schema tags start with `sha256-`, so the tags cannot collide.
It has the same shape as the fingerprint anchor, with
`"artifactType": "application/vnd.trianalab.aval.stats.v1"` and the
annotation `"io.trianalab.aval.test": "<test id>"`.

Each record is an OCI 1.1 image manifest with
`"artifactType": "application/vnd.trianalab.aval.run.v1"`, the empty config,
one layer of media type `application/vnd.trianalab.aval.run.v1+json` holding
the record JSON, and the stats anchor as `subject`. Records are listed by the
stats anchor's referrers. Unlike `registry.Push`, publishing a record does
not confirm the referrers listing, so on a registry without the referrers
API two concurrent records for one test can drop one of them.
On such registries all records of a test also share one referrers index,
which grows with every record; once it exceeds the registry's manifest size
limit, publishing a record fails with a warning; prune old records (see
[Pruning run records](#pruning-run-records)).

### Record JSON

`evidence.RunRecord`, parsed by `evidence.ParseRunRecord`:

| Field | Type | Present | Meaning |
|---|---|---|---|
| `version` | string | always | `aval.trianalab.io/run-record/v1`. |
| `testId` | string | always | `metadata.name` from the contract. |
| `fingerprint` | string | always | `sha256:<hex>`; empty when the fingerprint could not be computed. |
| `outcome` | string | always | The lookup decision, as in the verify report. |
| `reused` | bool | always | `true` when a verified result was reused and the test did not run. With `--shadow`, or when `--report` cannot be written, it is `false`. |
| `result` | string | when known | The reused or the real `passed` / `failed`; omitted when `runner.command` could not start. |
| `verifiedResult` | string | when `outcome` is `verified` | The result of the verified attestation. With `reused` `false` and the test run (`--shadow`), a `verifiedResult` that differs from `result` is a false hit. Records made before it was added (#88) lack it. |
| `testDurationMs` | integer | always | How long `runner.command` ran, in milliseconds; `0` when it did not run. |
| `savedDurationMs` | integer | always | On reuse, the test duration recorded in the reused attestation (`finishedAt - startedAt`); `0` otherwise. |
| `avalVersion` | string | always | Version of the `aval` binary that ran. |
| `recordedAt` | RFC 3339 timestamp | always | When the record was made. |
| `ciRunUrl` | string | when passed | `--ci-run-url`, a link to the CI run. Aval does not derive it from any CI vendor. |
| `repository` | string | when passed | `--repository` on `aval run` (or `aval record`, which overrides the record's), the source repository, e.g. `acme/orders`. Aval does not derive it from any CI vendor. `aval stats --by repository` counts records without it (made before it was added, #88) as `unknown`. |

Example:

```json
{
  "version": "aval.trianalab.io/run-record/v1",
  "testId": "orders-integration",
  "fingerprint": "sha256:3f9a7c2e1b4d5f60718293a4b5c6d7e8f9012a3b4c5d6e7f8091a2b3c4d5e6f7",
  "outcome": "verified",
  "reused": true,
  "result": "passed",
  "verifiedResult": "passed",
  "testDurationMs": 0,
  "savedDurationMs": 1184000,
  "avalVersion": "0.3.0",
  "recordedAt": "2026-11-06T09:00:00Z",
  "ciRunUrl": "https://ci.example.com/acme/orders/runs/42",
  "repository": "acme/orders"
}
```

`evidence.ParseRunRecord` also rejects an inconsistent record: `reused`
`true` with an `outcome` other than `verified` or a `testDurationMs` other
than `0`, and a `verifiedResult` with an `outcome` other than `verified`. A
hand-edited record cannot claim a reuse that never happened; `aval record`
refuses it and `aval stats` skips it.

### Statistics: `aval stats`

`aval stats --registry <repo> [--test <id>] [--since <duration|date>]
[--by test|repository] [--output text|json]` reads the run records and aggregates them on demand.
It lists the `stats-<test id>` tags of the repository (or takes `--test`),
lists each stats anchor's referrers with artifact type
`application/vnd.trianalab.aval.run.v1`, and fetches each record by digest.
It writes nothing, locally or to the registry, and needs only read access.
`--since` keeps records whose `recordedAt` is at or after a time: a duration
back from now (`168h`, `30d`) or a date (`2026-11-01`, or RFC 3339).
`--by` groups the records by test (default) or by `repository`, the source
repository in each record; records without one count as `unknown`.

Statistics are informational: records are unsigned, so anyone with push
access can skew them, and they never affect reuse.

Registry data is untrusted, so reading is bounded:

- A record whose manifest is not a run record of that stats anchor, whose
  layer is over 64 KiB, that cannot be fetched or parsed, that is
  inconsistent (see above) or that names another test is **skipped and
  counted** (`skipped`), never an error.
- Reading stops after 1000 records in total, or 64 pages in one tag or
  referrers listing. The statistics then cover only what was read before,
  `aval stats` prints `aval: warning: stopped reading run records: ...` on
  stderr and the JSON report sets `truncated`. Which records come first is up
  to the registry; prune old records to stay below the bound.
- A listing that fails for any other reason (registry down, no read
  access) is exit 1, and so is exceeding `--registry-timeout` while listing
  or fetching records: a deadline never shows up as skipped records.
- `--since` filters after reading: the read bound, the timeout and
  `skipped` cover every record listed, whatever its `recordedAt`. Records
  are fetched one at a time, two requests each (about 0.7 s per record on
  GHCR), so the default `1m` timeout covers some 80 records; raise
  `--registry-timeout` or prune old records.

Exit codes: 0 statistics printed, 1 error, 2 usage.

#### Stats report JSON

`aval stats --output json` prints `evidence.StatsReport`:

| Field | Type | Present | Meaning |
|---|---|---|---|
| `version` | string | always | `aval.trianalab.io/stats-report/v1`. |
| `registry` | string | always | The repository read. |
| `since` | RFC 3339 timestamp | with `--since` | Records made before it are left out (not counted, not skipped). |
| `total` | object | always | Run statistics over every test in `tests`. |
| `tests` | array | always | One `{"testId", "stats"}` per test with records, sorted by test id; `[]` when there are none. |
| `repositories` | array | with `--by repository` | One `{"repository", "stats"}` per source repository, sorted; records without a repository are under `unknown`. Omitted when there are no records. |
| `skipped` | integer | always | Records read but not counted (see above). |
| `truncated` | string | when a bound was hit | Which bound, e.g. `more than 1000 run records`. |

Run statistics (`total`, `tests[].stats`, `repositories[].stats`); `runs` is
the sum of `reused`, the three `misses` and `verificationFailures`:

| Field | Meaning |
|---|---|
| `runs` | Records counted. |
| `reused` | Records with `reused` `true`. |
| `hitRate` | `reused / runs`; `0` when `runs` is `0`. |
| `misses.noTrustedResult` | Outcome `miss`. |
| `misses.error` | Outcome `error`: Aval could not decide. |
| `misses.verifiedNotReused` | Outcome `verified` with `reused` `false`: `--shadow`, or the `--report` file could not be written. |
| `verificationFailures` | Outcome `verification-failed`: security events, not misses. |
| `shadowFalseHits` | Of `verifiedNotReused`, the runs whose `result` differs from `verifiedResult`. Records without both are not counted. |
| `testDurationMs` | Sum of `testDurationMs`: time spent running tests. |
| `savedDurationMs` | Sum of `savedDurationMs`: test time reuse saved. |

A negative duration in a record (a clock jump, or an edit) counts as `0`,
and duration sums stop at the largest int64 instead of wrapping.
The golden file is `pkg/evidence/testdata/stats-report-v1.golden.json`.

### Pruning run records

Records accumulate: one per recorded run. Aval never deletes one. To keep
`aval stats` below its read bound, and the referrers index of registries
without the referrers API below their manifest size limit, delete old
records, for example with [ORAS](https://oras.land):

```sh
oras discover --artifact-type application/vnd.trianalab.aval.run.v1 <repo>:stats-<test id>
oras manifest delete <repo>@sha256:<record digest>
```

`oras discover --format json` shows each record's digest; fetch one with
`oras manifest fetch` and `oras blob fetch` to read its `recordedAt`.
Deleting records only changes statistics: run records never affect reuse.
The registry must allow deletes (see
[the 64-candidate cap and cleanup](#the-64-candidate-cap-and-cleanup)).

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
second layer of the execution manifest. `registry.Push` requires it:
`aval verify` checks the signature through the bundle (`attest.Verifier`),
so an attestation without one could only ever be a verification failure.

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
  "predicateType": "https://aval.trianalab.io/attestation/execution/v2",
  "predicate": { "...": "see below" }
}
```

For an image, use `"digest": {"sha256": "<hex>"}` with the image reference as
`name`.

`aval run` records `name` = the test id and `digest` = `{"sha256": "<fingerprint
hex>"}` (the declared inputs), `runner.identity` = the host name (a claim;
trust comes from the signer), `runner.trustLevel` = 0, and
`execution.environment` = `os` and `arch` plus the declared toolchain.
Recording a git commit or image as the subject is milestone 3 work.

## Predicate

`evidence.Predicate`, JSON tags exactly as below. Required fields are the ones
checked by `Predicate.Validate` (called by `evidence.Parse`).

| Field | Type | Required | Meaning |
|---|---|---|---|
| `schemaVersion` | string | yes, must be `v2` | Predicate schema version. |
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
identities (`evidence.Verification.Signer` and its `TrustLevel`). Consumers
must not make trust decisions from the `runner` block. The Go fields
`Runner.Issuer`, `Runner.TrustLevel` and `Verification.TrustLevel` are
deprecated: Aval leaves the first two empty, and the third duplicates
`Verification.Signer.TrustLevel`.

`evidence.Parse` only checks structure. A parsed predicate is a claim, not
evidence; use `evidence.Verify` to get something you can rely on.

### Annotations

Keys use reverse-DNS of the owner, e.g. `com.example.team`,
`com.acme.ticket`. Aval does not define, validate or interpret any key. Do not
put secrets there: the predicate is published to the registry.

### Example

```json
{
  "schemaVersion": "v2",
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

`aval verify --output json` prints one `evidence.Report` to stdout, and
`aval run --report <file>` writes the same document for its lookup decision.
This is the stable machine interface for CI scripts and external tools.

<!-- --8<-- [start:verify-report] -->

| Field | Type | Present | Meaning |
|---|---|---|---|
| `version` | string | always | `aval.trianalab.io/verify-report/v1`. |
| `outcome` | string | always | See outcomes below. |
| `testId` | string | always | `metadata.name` from the contract. |
| `fingerprint` | string | always | Locally computed `sha256:<hex>`. |
| `reference` | string | when an attestation was found | Immutable `repo@sha256:<manifest digest>`. |
| `result` | string | `verified` only | `passed` or `failed` from the verified predicate. |
| `signer` | object | `verified` only | Accepted policy identity that matched (`issuer`, `subjectRegexp`, `trustLevel`) plus the identity the signature actually carried: `subject`, the certificate subject (SAN) for keyless signatures, or `keyId`, `base64(sha256(DER public key))` for key-based ones. `subject` and `keyId` are omitted when empty. |
| `predicate` | object | `verified` only | Full verified predicate. |
| `reason` | string | non-`verified` outcomes | Human-readable cause. |
| `rejected` | array | when a candidate was not used | Each `{reference, outcome, reason}`; `outcome` is `verification-failed`, `error` or `miss`. A `verification-failed` entry is a security event even when `outcome` is `verified`: alert. A `miss` entry is an authentic attestation whose signer the policy lists below `minTrustLevel`: routine non-reuse, no alert. |

Outcomes:

| `outcome` | Meaning | What the caller does |
|---|---|---|
| `verified` | Valid attestation satisfies the policy. | Reuse `result`. |
| `miss` | No attestation for this fingerprint that the policy can reuse: none found, or only authentic ones whose signer the policy lists below `minTrustLevel`. | Run the test. |
| `verification-failed` | Attestation exists but fails signature or policy. | Run the test **and** alert. Never hide this. |
| `error` | Aval could not decide (registry unreachable, bad policy file, trusted root unavailable). | Treat as a miss: run the test (fail closed). |

<!-- --8<-- [end:verify-report] -->

Exit codes, from the [CLI reference](cli-reference.md#exit-codes):

--8<-- "docs/cli-reference.md:exit-codes"

`aval verify` exits with the outcome. `aval run` acts on the outcome and
exits with the result instead; see
[trust-model.md](trust-model.md#what-aval-run-does-with-each-outcome).

If the fingerprint cannot be computed (empty scope, missing dependency,
symlink, control characters in a path, I/O error), `aval verify` prints no
report and exits 1; bad flags or an invalid contract print no report and exit 2.
`aval run --report` still writes one in that case, with outcome `error`, the
reason, and an empty `fingerprint`, and then runs the test.

Without `--policy`, `aval verify` skips the lookup and reports `outcome: "miss"`
(exit 4), and so it does for a `non-deterministic` contract. With `--policy`, a policy file that cannot be loaded is
`outcome: "error"` (exit 1) and no lookup is made. A trusted root that
cannot be loaded is `outcome: "error"` (exit 1) once a candidate with a
certificate needs it.

Example (`verified`):

```json
{
  "version": "aval.trianalab.io/verify-report/v1",
  "outcome": "verified",
  "testId": "orders-integration",
  "fingerprint": "sha256:3f9a7c2e1b4d5f60718293a4b5c6d7e8f9012a3b4c5d6e7f8091a2b3c4d5e6f7",
  "reference": "ghcr.io/acme/orders-aval@sha256:8c1d4e5f60718293a4b5c6d7e8f9012a3b4c5d6e7f8091a2b3c4d5e6f7a8b9c0",
  "result": "passed",
  "signer": {
    "issuer": "https://token.actions.githubusercontent.com",
    "subjectRegexp": "^https://github.com/acme/orders/\\.github/workflows/ci\\.yaml@refs/heads/main$",
    "trustLevel": 2,
    "subject": "https://github.com/acme/orders/.github/workflows/ci.yaml@refs/heads/main"
  },
  "predicate": { "...": "as in the predicate example" }
}
```

Example (`miss`):

```json
{
  "version": "aval.trianalab.io/verify-report/v1",
  "outcome": "miss",
  "testId": "orders-integration",
  "fingerprint": "sha256:3f9a7c2e1b4d5f60718293a4b5c6d7e8f9012a3b4c5d6e7f8091a2b3c4d5e6f7",
  "reason": "no attestation found for fingerprint"
}
```

## Versioning and compatibility

These rules apply to every frozen format:

| Format | Version identifiers (move together) | Frozen at |
|---|---|---|
| Statement and predicate | `PredicateType`, `SchemaVersion`, `MediaTypeExecution`, `ArtifactType` | `.../execution/v2`, `v2`, `execution.v2` |
| Fingerprint anchor | `ArtifactTypeFingerprint` | `fingerprint.v1` |
| Verify report, outcomes and exit codes | `ReportVersion` | `aval.trianalab.io/verify-report/v1` |
| Run record and stats anchor | `RunRecordVersion`, `ArtifactTypeRun`, `MediaTypeRun`, `ArtifactTypeStats` | `run-record/v1`, `run.v1`, `stats.v1` (provisional) |
| Stats report | `StatsReportVersion` | `aval.trianalab.io/stats-report/v1` (provisional) |
| Contract and policy file | `apiVersion` | `aval.trianalab.io/v1` |
| Fingerprint manifest | header line, `execution.fingerprintVersion` | `aval-fingerprint/v1` |

The predicate keeps `v2`: `v1` was used before and an identifier is never
recycled, and `v2` is already under `aval.trianalab.io` and is what v0.1.0
writes, so attestations published by v0.1.0 stay readable. The report and the
contract moved from the pre-freeze `aval.dev/.../v1alpha1` names to
`aval.trianalab.io` at `v1`. There is no accept window for the old names: a
contract or policy file with `aval.dev/v1alpha1` is invalid.

### Additive changes (same identifier)

- A new optional field, omitted when empty, whose absence means what it
  meant before.
- A new key under `annotations`.
- A new optional contract field. Older `aval` binaries reject a contract
  that uses it (unknown fields are rejected), which fails closed.

Existing fields keep their name, JSON tag, type and meaning.

### Breaking changes (new identifier)

- Renaming or removing a field, changing its type or its meaning.
- Making an optional field required, or tightening validation of an
  existing one.
- A new value of a closed set: `outcome`, `result`, `trustLevel`,
  cacheability class, or an exit code.
- A change to what the fingerprint covers or how it is hashed: a new
  `aval-fingerprint/vN`. There is no accept window: old attestations become
  misses, never false hits.

A new predicate version changes `PredicateType`, `SchemaVersion`,
`MediaTypeExecution` and `ArtifactType` together (`execution/v3`,
`execution.v3`). Old and new artifacts can coexist in one repository.

### How consumers treat versions

- **Unknown version: not evidence.** Reject a `predicateType`,
  `schemaVersion`, report `version` or `artifactType` you do not know, and
  run the test. `evidence.Verify` does this for predicates.
- **Unknown fields: ignore them** within a version you know. Do not decode
  Aval documents strictly. `evidence.Parse` ignores unknown fields.
- **Unknown values of a closed set: fail closed.** An `outcome` other than
  the four listed above, a `result` other than `passed` or `failed`, or an
  unknown trust level or cacheability class means "not verified": run the
  test. Within a version such a value can only come from a bug or tampering.
- **No field grants trust.** Trust comes from the verified signer and the
  policy (see [trust-model.md](trust-model.md)), never from a field that is
  new or that you do not understand.

### Deprecation

Aval writes only the newest version of each format. When a breaking change
mints a new version, the release notes say whether Aval keeps reading the
old one for a transition. If it does, the old parser stays next to the new
one, dispatching on the identifier, and old documents get no more trust than
new ones; any identifier Aval does not know fails closed. Ending a transition
is announced in the release notes of the release that removes it.

### Golden files

`pkg/evidence/testdata/statement-execution-v2.golden.json`,
`pkg/evidence/testdata/verify-report-v1.golden.json`,
`pkg/evidence/testdata/run-record-v1.golden.json` and
`pkg/evidence/testdata/stats-report-v1.golden.json` set every field of the
statement, predicate, verify report, run record and stats report. A test decodes each strictly into the
current types and requires the same JSON back, so dropping, renaming or
retyping a field, or adding a required one, fails. Another test requires
every JSON field of those types to be set in its golden file, so a new field
fails until it is added there. Golden files only grow: an existing field or
value is never removed or changed, an additive change adds its new field to
the golden file of its version, and a new version adds a new file next to
the old ones.

### History

- Predicate `v1` became `v2` when the execution manifest's `subject` changed
  from the tested artifact to the fingerprint anchor and the optional
  Sigstore bundle layer was added.
- At the freeze (#17), the verify report moved from
  `aval.dev/verify-report/v1alpha1` to `aval.trianalab.io/verify-report/v1`
  and the contract and policy `apiVersion` from `aval.dev/v1alpha1` to
  `aval.trianalab.io/v1`, without other changes. The contract hash covers
  `apiVersion`, so every fingerprint changed: results published before
  are misses and the tests run once.
- Within `verify-report/v1` (#65), a signer the policy lists below
  `minTrustLevel` became `miss` instead of `verification-failed`, and
  `rejected[].outcome` may now also be `miss`. No new value, field or exit
  code; the maintainers confirmed this as additive, so the version stays.
- Within the provisional `run-record/v1` (#88), records gained the optional
  `verifiedResult` and `repository`, and `evidence.ParseRunRecord` started rejecting
  inconsistent records (a reuse with an outcome other than `verified` or
  with test time, a `verifiedResult` without outcome `verified`). Tightening
  validation is breaking for a frozen format; the identifier stays because
  it is provisional and no record `aval` writes is affected (#88).
