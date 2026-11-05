# Trust model

Status: pre-alpha. Milestone 1 (contract + fingerprint) is implemented.
Signing (`attest.Sign`) is implemented but not yet wired into the CLI.
Push, lookup and fetch against an OCI registry (`internal/registry`) are
implemented. Verification and policy loading are milestone-2 stubs that
return "not implemented" (exit code 6). This document describes the model
the code is built around; where behavior is not implemented yet, it says so.

The predicate type, media types and the `aval.dev/v1alpha1` apiVersion are
provisional until v1.

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
signer, not the one written in the predicate. In code: `attest.Sign` returns
the `Signer` read from the Fulcio certificate (OIDC issuer and subject), and
`Signer.TrustLevel` / `evidence.MatchIdentity` look that identity up in the
policy's `Identities`. A key-based signer matches no keyless identity and
gets level 0 (unknown) until key identities land in the policy format
(milestone 2, #8).

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
| `Identities` | accepted signers. Each `evidence.Identity` is an OIDC `Issuer`, a `SubjectRegexp` matched against the certificate subject (SAN), and the `TrustLevel` that signer is granted. Empty means nothing is accepted. |
| `MinTrustLevel` | minimum granted level required for reuse. |
| `AllowedCacheability` | cacheability classes that may be reused. Empty means `hermetic` and `controlled-environment` only. |
| `ExpectedFingerprint`, `ExpectedTestID` | set by Aval from the local contract, not by the policy file. Must equal the predicate's `execution.fingerprint` and `test.id`. |

Without `--policy` nothing is trusted. Aval does not even query the
registry: the outcome is `miss` (exit 4) with the reason "no trust policy
configured", and the test runs. This avoids reporting every attestation it
finds as a verification failure just because no policy exists. Once a policy
is configured, an attestation from a signer it does not accept *is* a
verification failure (exit 5).

The policy file format (`aval.dev/v1alpha1`, kind `Policy`, regexp
validation, rejecting `non-deterministic` in `AllowedCacheability`) is a
milestone-2 TODO. Today `policy.Load` returns "not implemented".

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
  highest trust level; a candidate that fails never decides the outcome while
  another verifies, and is listed in `rejected` in the verify report (and on
  stderr), so the security event stays visible.
- **Unknown or missing fields** in `aval.yaml` are rejected (exit 2) rather
  than ignored.

`aval run --no-reuse` (milestone 2) will always run and never reuse; the flag is accepted today but `aval run` is still a stub.

## Miss vs verification failure

These are different outcomes, in code and in exit codes:

| Situation | `aval verify` outcome | Exit code | Go error |
|---|---|---|---|
| valid attestation, policy satisfied | `verified` | 0 | nil |
| no attestation for this fingerprint | `miss` | 4 | wraps `evidence.ErrNotFound` |
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
| Compromised developer key | Policy grants developer identities level 1 at most; CI sets `MinTrustLevel` >= 2, so a stolen developer key cannot satisfy CI. It can still poison that developer's own reuse. Prefer keyless (short-lived Fulcio certs, Rekor log) over long-lived keys (milestone 2). |
| Tag swapping (`sha256-<fp>` repointed to another manifest) | Tag is lookup only; fetch and verify are by digest. A swapped tag pointing at an untrusted or mismatched attestation yields `verification-failed`, not reuse. A swapped tag pointing at a different *validly signed* attestation for the same fingerprint and test id is equivalent evidence. |
| Replay of a passing result for a different fingerprint or test id | `Verify` requires `execution.fingerprint == ExpectedFingerprint` and `test.id == ExpectedTestID`, both computed locally. The signed predicate cannot be re-labeled without breaking the signature. Mismatch is `verification-failed`. |
| Undeclared input changed (file outside scope, unlisted env var, network service) | Out of scope for detection in v0: the fingerprint only covers declared inputs. Mitigated by contract review, `external-dependencies` / `non-deterministic` classes not being reusable by default, and the false-hit suite (roadmap item 4). Observed-input tracing is future work. |
| Registry compromise (attacker can write arbitrary manifests and tags) | Cannot forge signatures, so it cannot produce reuse on its own; injected attestations show up as `verification-failed`. It can delete attestations (forcing misses, i.e. re-runs) or deny service (`error`, re-run). Availability is out of scope. |
| Dishonest but trusted signer (ran a modified test and signed "passed") | Out of scope for Aval. A signature proves who claims it, not that the test ran honestly. Limit which identities get which level; level 3 (isolated) exists for this. |
