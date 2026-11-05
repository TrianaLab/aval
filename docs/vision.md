# Aval: vision

Status: pre-alpha. Milestones 1 (contract + fingerprint) and 2 (sign, push
and verify against a registry, `aval run` reuse or execute) are implemented.

## Thesis

Aval turns test results into signed, verifiable OCI artifacts. An expensive
test runs once, locally or in CI. The result is published to an OCI registry as
a signed attestation bound to a deterministic fingerprint of the test's inputs.
Any other environment computes the same fingerprint, finds the attestation,
verifies it, and if the trust policy allows it, reuses the result instead of
running the test again.

This is not a test cache. It is a CI primitive: **verifiable test execution
reuse**. A cache optimizes for hit rate and quietly tolerates a stale entry now
and then. Aval optimizes for being able to justify every reuse, and when it
can't, it runs the test.

A signature alone does not get you there. A signature proves *who claims* the
test passed, not that it ran honestly or against the inputs you have now.
Reuse is only safe when two things hold:

- an explicit **input contract** (`aval.yaml`) that says what the test depends
  on, so "same inputs" is something you can check, and
- a **trust policy** over signer identities, so "who ran it" is something you
  can accept or reject.

## Problem

Heavy tests (integration, end-to-end, hardware, long suites) get re-run on
every pipeline, branch and environment even when nothing they depend on has
changed. Teams either pay for it in time and compute, or they skip tests based
on informal judgment ("that service didn't change"), which nobody can audit.
Existing build caches solve this for teams that buy into a hermetic build
system. Most teams have not, and their CI cannot prove that a result from one
environment is valid in another.

## Goals

- A test contract (`aval.yaml`) that declares a test's identity, its runner
  command, its input scope, the environment conditions that matter, and its
  cacheability class.
- A deterministic execution fingerprint over the declared inputs, independent
  of checkout path and file iteration order.
- Signed in-toto attestations of execution results, stored in any OCI 1.1
  registry and found by fingerprint.
- Verification that keeps "no result" (miss) separate from "result exists
  but can't be trusted" (verification failure), in both code and exit codes.
- A versioned output format and a public Go package (`pkg/evidence`) that
  other tools can consume without depending on Aval internals.

## Non-goals

- **No registry, server or database of our own.** Storage, lookup and linking
  go through standard OCI registries.
- **No runtime tracing or input inference in v0.** Scope is declared, not
  observed. Recording the files a test actually reads ("observed inputs") is
  future work, and would be used to check declared scope, not to replace it.
- **Not a build system and not a Bazel replacement.** Aval doesn't model
  build graphs, actions or outputs. Teams already on Bazel's remote cache may
  not need it.
- **Not a test framework.** Aval runs an arbitrary command and records its exit
  code. It doesn't discover, select or report individual test cases.
- **No knowledge of Pacto or any other consumer.** Aval doesn't import Pacto
  or model its concepts. Consumers integrate through the stable formats below,
  and their adapters live in their own repositories.

## Design decisions

1. **Standard supply-chain plumbing, no own infrastructure.** Publishing and
   fetching use ORAS, signatures use Cosign/Sigstore, attestations use
   in-toto Statements in DSSE envelopes, and evidence is linked through OCI 1.1
   referrers. Lookup goes only through the registry, via the tag
   `sha256-<fingerprint hex>`, and artifacts are always consumed by digest.
   *Rationale:* registries, signing and access control already exist and are
   already operated; tags are mutable, digests aren't.
2. **Scope belongs to the test contract, not the runner.** Every test declares
   its inputs in `aval.yaml` (`scope.source`, `scope.exclude`,
   `scope.dependencies`, `environment`). Nothing is inferred or traced in v0.
   *Rationale:* the test author is the one who knows what the test depends on,
   and a declared scope can be reviewed in a PR.
3. **Fail closed.** If input equivalence can't be proven, the test runs. That
   covers unknown contract fields, dependency globs that match nothing,
   symlinks in scope, and an empty scope. A miss (exit 4, `ErrNotFound`) and
   a verification failure (exit 5, `ErrVerification`) are separate outcomes.
   A verification failure is a security event and is never downgraded to a
   miss. *Rationale:* a false hit costs more than a re-run, and a forged or
   mis-signed result is something someone needs to look at.
4. **Trust tiers.** Level 1 is a developer signature, meant as personal
   acceleration and not full trust. Level 2 is a runner with a verifiable
   identity. Level 3 is isolated execution with a verifiable attestation. The
   policy decides which signer identities are accepted, at which level, and
   the minimum level needed for reuse. With no policy, nothing is accepted.
   *Rationale:* how much a signed claim is worth depends on where it was
   produced, and that call belongs to whoever consumes the result.
5. **Test identity vs execution fingerprint.** `metadata.name` / `test.id` says
   *what* is checked. The fingerprint says *which concrete inputs and
   conditions* a particular run had. *Rationale:* the same check gets many
   executions, and policy and reporting need to tell the two apart.
6. **Cacheability classes.** `hermetic` and `controlled-environment` are
   reusable by default. `external-dependencies` needs an explicit policy
   opt-in. `non-deterministic` is never reusable by default. *Rationale:* a
   matching fingerprint only means something if the result depends on the
   declared inputs.
7. **Language-, framework- and CI-agnostic.** v0 ships only a generic runner:
   an argv command with no shell interpolation, where the exit code is the
   result. *Rationale:* one generic runner covers every stack, and
   framework-specific runners can come later if there's demand.

### Consumption contract

External tools consume Aval results through:

- versioned artifact and predicate formats with Aval's own media types
  (provisional: predicate type
  `https://aval.trianalab.io/attestation/execution/v2`, media type
  `application/vnd.trianalab.aval.execution.v2+json`, artifact type
  `application/vnd.trianalab.aval.execution.v2`, contract `apiVersion`
  `aval.dev/v1alpha1`),
- neutral predicate fields: `subject`, `test.id`, `result`,
  `execution.fingerprint`, `execution.environment`, `runner` identity,
  `startedAt`/`finishedAt`, plus a free `annotations` map that Aval never
  reads,
- the public Go package `pkg/evidence` (`Parse`, `Verify`),
- `aval verify --output json`.

These formats are provisional until milestone 5 makes them stable.

## Risks

- **Bazel or a remote cache is already enough for some teams.** Mitigation:
  don't compete there. Aval targets tests outside a hermetic build graph
  (integration, e2e, cross-environment). We'll know it isn't working if early
  users are mostly Bazel shops that also want Aval for build actions.
- **Declaring scope can cost more than it saves.** Mitigation: keep the
  contract small (globs, lockfiles, a toolchain map) and make it debuggable
  with `aval run --explain`. Detection: contracts that drift away from the code
  they cover, or teams that give up on tests with a wide scope.
- **Real savings are small if inputs always change.** If every commit touches
  the scope, the fingerprint never matches. Detection: the hit rate in the
  milestone 3 example and in early adopters' pipelines. A test whose scope is
  "the whole repo" is a bad fit, and the docs should say so.
- **Security policies may reject attestations from developer machines.**
  Mitigation: this is expected, and it's why trust tiers exist. CI policies
  should require level 2 or higher, and level 1 stays personal acceleration.
  Detection: whether the value holds up when only runner and isolated
  signatures are accepted.

## Roadmap

1. **Fingerprint + contract.** *Implemented.* Exit criterion: `aval.yaml` is
   parsed and validated with unknown fields rejected, and
   `aval run --fingerprint-only` / `--explain` produce a deterministic
   `aval-fingerprint/v1` digest and manifest that fail closed on undeclared
   or unprovable inputs.
2. **Push/verify against a local registry with Cosign signing.**
   *Implemented* (`aval run` exits with the test's own result after a miss,
   and reports the miss). Exit criterion: `aval run --push` signs and publishes an attestation to a local
   OCI registry. `aval verify` and `aval run` against that registry reuse a
   policy-accepted result (exit 0), report a miss (exit 4), and report a
   tampered or untrusted attestation as a verification failure (exit 5). The
   policy file format is defined.
3. **End-to-end example in GitHub Actions.** Exit criterion: a public example
   repo where one workflow run executes and publishes, a second run with
   unchanged inputs reuses the result, and a run with changed inputs misses
   and re-executes, all visible in the workflow logs.
   *In progress:* the `example-go-compose` job in `.github/workflows/ci.yaml`
   runs this story for `examples/go-compose` in this repository on every
   push to `main` (see [its README](../examples/go-compose/README.md)).
4. **False-hit suite.** Exit criterion: an automated suite that applies every
   known kind of relevant change (source, dependency, toolchain, env var,
   exec bit, contract edit, symlink, file rename) and asserts that none of
   them can reuse a previous execution.
5. **Stable consumption contract for external integrations.** Exit criterion:
   the predicate type, media types, predicate fields, `pkg/evidence` API and
   `aval verify --output json` report are declared stable and versioned,
   with only additive changes allowed within v1, and an external consumer
   can verify results using `pkg/evidence` alone.
