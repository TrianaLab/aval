# Trust model

Status: pre-alpha. Milestone 1 (contract + fingerprint) is implemented.
Signing (`attest.Sign`), push, lookup and fetch (`internal/registry`),
verification (`evidence.Verify`, `attest.Verifier`) and the policy file
(`policy.Load`) are implemented; `aval verify`, `aval run` and `aval push`
use them. This document describes the model the code is built around; where
behavior is not implemented yet, it says so.

The predicate type, media types and the `aval.trianalab.io/v1` apiVersion are
frozen; see [artifact-format.md](artifact-format.md#versioning-and-compatibility).

## What a signature proves

An Aval attestation is a DSSE envelope around an in-toto Statement whose
predicate (`evidence.Predicate`) says: "test `test.id`, with inputs hashing
to `execution.fingerprint`, produced `result` on `subject`".

A valid signature proves:

- **Who claims it.** The signer identity (OIDC issuer + certificate subject
  for keyless, or a key) is bound to the claim.
- **Integrity of the claim.** The predicate has not been modified since it
  was signed.

A valid signature does not prove:

- that the test actually ran;
- that it ran honestly (unmodified test code, no skipped cases, no patched
  runner);
- that inputs not declared in `aval.yaml` were the same. The fingerprint
  only covers what the contract declares (see [contract.md](contract.md)).
  Anything else is invisible to Aval.

So reuse is never "the signature is valid". It is "the signature is valid,
the signer is someone the policy trusts at a sufficient level, and the
fingerprint and test id match what we computed locally".

`runner.identity`, `runner.issuer` and `runner.trustLevel` inside the
predicate are what the signer *claims*. The authoritative identity is the
verified signer, and the trust level is the one the policy grants to that
signer, not the one written in the predicate. In code: `attest.Verifier`
verifies the Sigstore bundle and returns the `evidence.Signer` it binds (the
OIDC issuer and subject of the Fulcio certificate, or the key's id), and
`evidence.Verify` looks that signer up in the policy's `Identities` with
`evidence.MatchSigner`. A key-based signer only matches identities that list
its public key; a keyless one only matches issuer and subject entries.

## Signing

`attest.Sign` (sigstore-go) signs the in-toto statement as a DSSE envelope
in one of two modes:

- **Keyless** (CI): an ephemeral key, a short-lived Fulcio certificate for
  the OIDC token the caller passes in, and a Rekor entry (always recorded,
  since the certificate expires in minutes and Rekor's timestamp is what
  makes it verifiable later). Defaults to the Sigstore public-good instance;
  any Fulcio and Rekor can be configured. Obtaining the OIDC token is the
  caller's job, so no CI vendor or issuer is assumed.
- **Key-based** (local): an unencrypted ECDSA P-256 PEM key. No Rekor entry
  unless a Rekor URL is given. Expect policies to grant such signatures at
  most level 1.

Besides the envelope, `Sign` returns the Sigstore bundle (v0.3 JSON) with the
verification material (certificate or key hint, Rekor entry). `registry.Push`
stores it next to the envelope as an optional layer of the execution manifest
(see [artifact-format.md](artifact-format.md#oci-layout)).

## Trust levels

`evidence.TrustLevel`, higher is stronger:

| Level | Name | Constant | Who | Good for |
|---|---|---|---|---|
| 0 | unknown | `TrustUnknown` | level could not be established | never accepted |
| 1 | developer | `TrustDeveloper` | a developer's local signature | personal acceleration only: skip a test you already ran on your machine. Not full trust. |
| 2 | runner | `TrustRunner` | a CI runner with a verifiable workload identity (e.g. GitHub Actions OIDC) | reuse in CI. CI should require at least this level. |
| 3 | isolated | `TrustIsolated` | an isolated execution with a verifiable attestation of the environment itself | reuse where the runner host is not trusted either |

Level 1 exists because a local signature is still useful to its author.
Expect many security policies to refuse it outright; that is the intended
use of `MinTrustLevel`.

## Policy

The trust policy decides which signers are accepted and at what level. It
is `policy.Policy` (`internal/policy`), converted to
`evidence.VerifyOptions` for `evidence.Verify`:

| Field | Meaning |
|---|---|
| `Identities` | accepted signers. Each `evidence.Identity` is either an OIDC `Issuer` plus a `SubjectRegexp` matched against the whole certificate subject (SAN), or a `PublicKey`, and the `TrustLevel` that signer is granted. Empty means nothing is accepted. |
| `MinTrustLevel` | minimum granted level required for reuse. Level 0 (unknown) and levels above 3 are never accepted, whatever this says. |
| `AllowedCacheability` | cacheability classes that may be reused besides `hermetic` and `controlled-environment`. Only `external-dependencies` has an effect; `non-deterministic` is never reusable. |
| `TrustedRoot` | Sigstore trusted root for keyless signatures (policy file only). |
| `ExpectedFingerprint`, `ExpectedTestID` | set by Aval from the local contract, not by the policy file. Must equal the predicate's `execution.fingerprint` and `test.id`. Required. |

`evidence.Verify` checks, in order: the signature (through
`VerifyOptions.Signature`), that the signer matches an accepted identity,
that the granted level is at least `MinTrustLevel`, that the payload is an
Aval in-toto statement whose subject equals the predicate's, the predicate
structure, the expected fingerprint and test id, and the cacheability. Any
failure is a `*VerificationError` (`ErrVerification`). The verified
`Signer` is the policy entry that matched, plus the `subject` or `keyId` the
signature actually carried, so the verify report shows who really signed.

The CLI (`aval verify`, `aval run`) checks the minimum level last instead:
it calls `evidence.Verify` with `MinTrustLevel` developer, then checks the
signed cacheability against the contract, then the policy's
`minTrustLevel`. An attestation that passes every other check but whose
signer the policy lists below `minTrustLevel` is routine non-reuse, like a
developer result under a CI policy: outcome `miss` (exit 4), listed in
`rejected` with outcome `miss`, never reused and never a security event. Any
other failure, including a false claim from such a signer, is
`verification-failed` (exit 5). Library callers of `evidence.Verify` still
get `ErrVerification` for a level below `MinTrustLevel`.

`attest.Verifier` checks signatures with sigstore-go against the Sigstore
bundle stored next to the envelope. A keyless signature must chain to a
Fulcio CA in the trusted root, carry an embedded SCT from a trusted CT log,
have a Rekor entry, and have a trusted timestamp (Rekor or a TSA) inside the
certificate's validity. A key-based signature must verify with one of the
policy's public keys; no Rekor entry is required. The envelope must be
byte-for-byte the one the bundle signs. An attestation without a bundle
never verifies.

Without `--policy` nothing is trusted. Aval does not even query the
registry: the outcome is `miss` (exit 4) with the reason "no trust policy
configured", and the test runs. This avoids reporting every attestation it
finds as a verification failure just because no policy exists. Once a policy
is configured, an attestation from a signer it does not list *is* a
verification failure (exit 5); one from a signer it lists below
`minTrustLevel` is a miss (exit 4). A policy file that cannot be loaded is
outcome `error` (exit 1): the test runs and nothing is looked up. The
trusted root is loaded only when a candidate's Sigstore bundle carries a
certificate (a key-based bundle never needs it), at most once per command;
if it cannot be loaded, that candidate is outcome `error` (exit 1, the test
runs), never a verification failure.

### Policy file

```yaml
apiVersion: aval.trianalab.io/v1
kind: Policy
minTrustLevel: runner           # developer | runner | isolated
# trustedRoot: sigstore/trusted_root.json  # keyless only; default: Sigstore public-good, fetched with TUF
identities:
  - issuer: https://token.actions.githubusercontent.com
    subjectRegexp: https://github\.com/acme/orders/\.github/workflows/ci\.yaml@refs/heads/main
    trustLevel: runner
  - publicKey: keys/alice.pub   # PEM public key file
    trustLevel: developer
allowedCacheability: [external-dependencies]
```

`policy.Load` rejects, rather than ignores: unknown fields, more than one
YAML document, another `apiVersion` or `kind`, a missing or unknown
`minTrustLevel` or `trustLevel` (levels are written by name), no identities,
an identity with both `publicKey` and `issuer`/`subjectRegexp` or with
neither, a `subjectRegexp` that does not compile alone and anchored, a
`publicKey` file that is not a PEM public key, and `non-deterministic` or an
unknown class in `allowedCacheability`. Relative `trustedRoot` and
`publicKey` paths are resolved against the policy file's directory.

## Fail closed

If Aval cannot prove that a previous execution is equivalent and trusted,
the test runs.

- **Fingerprint errors** (empty source scope, missing or mismatched
  dependency, symlink in scope, unreadable file): input equivalence cannot
  be proven, so there is nothing to look up. Run the test.
- **Registry errors** (unreachable, auth failure): outcome `error`, exit 1.
  For execution purposes this is a miss: run the test. A malformed
  attestation manifest, or a fingerprint tag that does not point at the
  fingerprint anchor, is a verification failure (exit 5).
- **Consume by digest.** `registry.Lookup` resolves the tag
  `sha256-<fingerprint hex>`, checks that it points at the fingerprint's
  deterministic anchor manifest (a moved tag is a verification failure), and
  returns the anchor's referrers as candidate manifest digests; everything
  after that (fetch, verify, report `reference`) uses `repo@sha256:...`. The
  tag is only an index for lookup. It is mutable and is never trusted on its
  own.
- **No silent replacement.** Pushes only add referrers to the anchor; a later
  push for the same fingerprint cannot replace an earlier attestation. Every
  candidate is verified and the policy picks the verified one with the
  highest trust level. A candidate that fails verification never decides the
  outcome while another verifies, and is listed in `rejected` in the verify
  report (and on stderr as `aval: SECURITY: ...`), so the security event
  stays visible. A candidate
  that cannot be fetched or checked blocks reuse (the test runs), because it
  might have outranked the verified one.
- **Unknown or missing fields** in `aval.yaml` are rejected (exit 2) rather
  than ignored.

`aval run --no-reuse` always runs and never looks anything up.

## What `aval run` does with each outcome

`aval run` makes the same decision as `aval verify` and prints it as the
verify text report (`--report <file>` also writes the JSON report). Only
`verified` reuses; everything else runs `runner.command` in the contract's
directory:

| Lookup outcome | `aval run` | Exit code |
|---|---|---|
| `verified`, result `passed` | reuse, do not run | 0 |
| `verified`, result `failed` | reuse the failure, do not run | 3 |
| `miss` (also: no `--registry`, no `--policy`, `--no-reuse`, `non-deterministic`, `external-dependencies` not in `AllowedCacheability`, only signers below `minTrustLevel`) | run | test result: 0 or 3 |
| `error` (registry, policy or fingerprint error) | run | test result: 0 or 3 |
| `verification-failed` | run, print `aval: SECURITY: ...` on stderr | 5, whatever the test result |

A fingerprint error leaves nothing to look up or record: the test runs, the
report has outcome `error` with the reason, and `--push` fails (exit 1).
Otherwise the run is recorded as an unsigned predicate in `--result`
(default `.aval/result.json`) and, with `--push`, signed and published.
`aval push` publishes a recorded result later, only if its test id,
cacheability and fingerprint still match the contract.

A `--report` file that cannot be written does not skip the test: `aval run`
then never reuses, runs the test, and still exits 5 on a verification
failure.

`$SIGSTORE_ID_TOKEN` is read once and removed from the environment before
`runner.command` starts: the code under test cannot use it to sign results as
the workload identity.

Reasons can carry text from an untrusted attestation, so the text output
and stderr escape it with Go string escapes (newlines, control and other
non-printable characters, quotes and backslashes): such text never
starts a line of its own (a CI log command, a terminal escape). The JSON
report keeps the reason as is.

`--shadow` verifies and runs the test anyway. If a verified result disagrees
with the real run, that is a false hit: `aval run` prints `aval: FALSE HIT:
...` on stderr and exits 1. If they agree, it says reuse would have been
correct.

Defense in depth for cacheability (invariant 7): a `non-deterministic`
contract is a miss without any lookup, in `aval run` and `aval verify`, and
so is an `external-dependencies` contract that the policy does not list in
`AllowedCacheability` (routine non-reuse, not a security event). Also,
a candidate whose signed `test.cacheability` differs from the local
contract's is a verification failure, even if the policy would allow its
class.

## Miss vs verification failure

These are different outcomes, in code and in exit codes:

| Situation | `aval verify` outcome | Exit code | Go error |
|---|---|---|---|
| valid attestation, policy satisfied | `verified` | 0 | nil |
| no attestation for this fingerprint | `miss` | 4 | wraps `evidence.ErrNotFound` |
| only authentic attestations whose signer the policy lists below `minTrustLevel` | `miss` | 4 | CLI only (see [Policy](#policy)) |
| attestation exists but fails signature or policy | `verification-failed` | 5 | wraps `evidence.ErrVerification` (`*evidence.VerificationError` carries the reason) |
| Aval could not decide (registry down, I/O) | `error` | 1 | other |

A miss is routine: inputs changed, nobody ran this yet. Run the test.

A verification failure is a security event. Someone published a result
under this fingerprint that does not verify, or was signed by an identity
the policy does not trust, or claims a different test id or fingerprint
than the tag it sits under. Possible causes range from a misconfigured
policy to a compromised publisher. The caller must run the test **and**
surface the failure: fail the job, page, or at least emit a visible alert.
Never collapse it into a silent re-run; doing so hides exactly the case the
trust model exists to catch. That is why it has its own exit code (5) and
its own `outcome` in `aval verify --output json`.

## Cacheability classes

Declared per test in `aval.yaml` (`cacheability`), copied into the
predicate as `test.cacheability`. The contract (including this field) is
hashed into the fingerprint, so a matching fingerprint implies the same
declared class.

| Class | Meaning | Reusable by default |
|---|---|---|
| `hermetic` | depends only on declared files and toolchain | yes |
| `controlled-environment` | depends on declared services/images pinned in scope (e.g. compose file with digests) | yes |
| `external-dependencies` | talks to things outside the scope (SaaS APIs, shared databases) | no, only if the policy lists it in `AllowedCacheability` |
| `non-deterministic` | results vary across runs with identical inputs | never |

## Threats

| Threat | Mitigation |
|---|---|
| Compromised developer key | Policy grants developer identities level 1 at most; CI sets `MinTrustLevel` >= 2, so a stolen developer key cannot satisfy CI. It can still poison that developer's own reuse. Prefer keyless (short-lived Fulcio certs, Rekor log) over long-lived keys. |
| Tag swapping (`sha256-<fp>` repointed to another manifest) | Tag is lookup only and must resolve to the fingerprint's deterministic anchor manifest; any other target is `verification-failed`. Candidates are the anchor's referrers, fetched and verified by digest. |
| Replay of a passing result for a different fingerprint or test id | `Verify` requires `execution.fingerprint == ExpectedFingerprint` and `test.id == ExpectedTestID`, both computed locally. The signed predicate cannot be re-labeled without breaking the signature. Mismatch is `verification-failed`. |
| Undeclared input changed (file outside scope, unlisted env var, network service) | Out of scope for detection in v0: the fingerprint only covers declared inputs. Mitigated by contract review, `external-dependencies` / `non-deterministic` classes not being reusable by default, and the false-hit suite (roadmap item 4). Observed-input tracing is future work. |
| Registry compromise (attacker can write arbitrary manifests and tags) | Cannot forge signatures, so it cannot produce reuse on its own; injected attestations show up as `verification-failed`, or, when another candidate verifies, as a `rejected` entry with outcome `verification-failed` in the verify report (and a warning on stderr). It can delete attestations (forcing misses, i.e. re-runs) or deny service (`error`, re-run). Availability is out of scope. |
| Dishonest but trusted signer (ran a modified test and signed "passed") | Out of scope for Aval. A signature proves who claims it, not that the test ran honestly. Limit which identities get which level; level 3 (isolated) exists for this. |
