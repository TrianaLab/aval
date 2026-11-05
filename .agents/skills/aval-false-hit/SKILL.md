---
name: aval-false-hit
description: Writes adversarial Go tests for Aval, covering cases that must stop a result from being reused (false hits) and cases that must keep the fingerprint stable. Covers changes to internal/fingerprint, internal/contract, pkg/evidence (Verify), internal/policy, internal/registry or internal/attest. Adds table-driven tests and Go native fuzz targets for parsers. Use when a change touches fingerprinting, contract parsing, glob matching, verification, trust policy or registry lookup, or when the user asks to "add false-hit cases", "harden the fingerprint", "test trust bypasses", "fuzz the contract parser" or "could this be reused when it shouldn't".
license: Apache-2.0
metadata:
  project: aval
compatibility: Go toolchain; run from the root of the TrianaLab/aval repo.
---

# aval-false-hit

A false hit means a stored result gets reused for an execution it does not
cover. In Aval that is the worst possible failure (AGENTS.md invariant 3). This
skill writes the tests that rule it out. The rules themselves are in AGENTS.md
(invariants, test levels); the fingerprint layout and fail-closed errors are in
`docs/contract.md`; the threats are in `docs/trust-model.md`. Don't copy them
here. Point to them.

## 1. Scope the change

1. Read the diff, or the files the user named. List which packages it touches:
   `internal/fingerprint`, `internal/contract`, `pkg/evidence`,
   `internal/policy`, `internal/registry`, `internal/attest`.
2. Read the existing `*_test.go` next to each touched package and reuse its
   helpers (temp-tree builders, base contract). Don't add parallel helpers.
3. Pick the families: A for fingerprint/contract changes, B for
   evidence/policy/registry changes. A change can need both.
4. If the code under test is still a stub that returns
   `evidence.ErrNotImplemented` (milestone 2: `Verify`, `policy.Load`,
   registry signing), still write the table, but skip each case with
   `t.Skip("TODO(milestone-2): ...")`. That way the cases land now and get
   switched on together with the code. Never assert on the stub error as if
   it were the expected outcome.

## 2. Write each case with its expected outcome

Every row says which outcome it expects, and the test asserts exactly that:

- **different fingerprint**: compute before and after, assert the digests differ.
- **same fingerprint**: a negative case. Assert the digests are equal.
- **fail closed**: `fingerprint.Compute` or `contract.Parse`/`Validate` returns
  an error (e.g. a non-regular file, a newline or CR in a path, a glob that
  matches nothing).
- **ErrVerification**: `errors.Is(err, evidence.ErrVerification)`. Never
  accept a plain error or `ErrNotFound` in its place (invariant 4).
- **ErrNotFound**: a miss, e.g. a lookup for a fingerprint that was never pushed.

## 3. Family A: input false hits (expect a different fingerprint or fail closed)

- [ ] File content changed in `source`; in `dependencies`; in a lockfile.
- [ ] Rename inside scope (same content, new path).
- [ ] File added to scope; file deleted from scope.
- [ ] Executable bit flipped (other mode bits must not matter).
- [ ] `environment.toolchain` value changed, key added, key removed.
- [ ] Env var: value changed; set vs unset; set-but-empty vs unset.
- [ ] `runner.command` argv reordered or an argument changed.
- [ ] Each contract field changed: `metadata.name`, `cacheability`, every
      `scope` list, `env` names.
- [ ] Globs: `**` at start/middle/end and alone; dotfiles and dot-dirs; an
      exclude that covers a nested path and a source that is re-included
      deeper; files under `.git` never counted.
- [ ] Case: `A.go` vs `a.go`. On case-insensitive macOS filesystems the two
      can't coexist, so build the test so it passes on both macOS and Linux
      (skip with a reason, or compare manifests, not trees).
- [ ] Unicode: the same visible name in NFC vs NFD (macOS may normalize).
      Decide what is expected, assert it, and record the decision.
- [ ] Separators: a backslash in a glob is rejected; trailing slashes, `./`,
      `..`, a leading `/` and empty segments are rejected by `ValidateGlob`.
- [ ] Symlink in scope (to a file inside and to one outside the root), FIFO,
      socket: fail closed.
- [ ] Newline or CR in a path: fail closed (docs/contract.md). Other control
      characters are hashed as-is; only change that with a doc update.
- [ ] Manifest injection: a filename or toolchain value that would forge a
      manifest line (`\nsource <hash> - x`) must not collide with a real tree.

Negative cases (expect the same fingerprint):

- [ ] Edits to files outside scope; files that only `exclude` matches.
- [ ] Glob order, env-name order, duplicate entries, directory walk order,
      checkout path.
- [ ] Comments, formatting and key order in `aval.yaml`; `exclude: []` vs omitted.
- [ ] Timestamps, owner, non-execute permission bits.

## 4. Family B: trust bypasses (expect ErrVerification unless stated)

- [ ] Replay: a valid attestation checked against another `ExpectedFingerprint`
      or another `ExpectedTestID`.
- [ ] Tag swap between resolve and fetch: the tag gets repointed after
      `Lookup`. Fetch must use the resolved digest (invariant 1). A foreign or
      tampered manifest fails verification.
- [ ] Signer not in `Identities`; empty `Identities` (accepts nothing).
- [ ] Issuer matches but the subject regexp doesn't, and the reverse.
      Unanchored regexps must not match a longer subject.
- [ ] Signer level below `MinTrustLevel`.
- [ ] Predicate claims a higher `trustLevel` than policy grants the signer:
      policy wins (invariant 5).
- [ ] `external-dependencies` without an opt-in in `AllowedCacheability`.
- [ ] `non-deterministic` forced through `AllowedCacheability`: still rejected,
      and `policy.Load` rejects that file (invariant 7).
- [ ] Expired, not-yet-valid or untrusted-chain certificate; bad signature;
      envelope payload edited after signing.
- [ ] Several attestations for the same fingerprint with mixed trust: a
      low-trust or invalid one must not shadow a valid one or win over it,
      and an invalid one is still reported.
- [ ] A failed or errored result is never reused as a pass.
- [ ] Nothing pushed for the fingerprint: `ErrNotFound`, not `ErrVerification`.

## 5. Fuzz the parsers

Add Go native fuzz targets (`func FuzzX(f *testing.F)`) beside the code, seeded
from the table cases, for: `contract.Parse`, `contract.ValidateGlob` together
with `contract.Match`, `evidence.Parse`, and manifest construction in
`internal/fingerprint` (fuzz path names and toolchain/env values). Properties
to check: no panic; if a glob is accepted, `Match` never matches a path with
`..` or a leading `/`; two different inputs never produce the same manifest
text. Run a short local session, e.g. `go test -run '^$' -fuzz FuzzParse
-fuzztime 30s ./internal/contract`. If a `make` fuzz target exists, use it.
Otherwise note that the fuzz CI target is not available yet (Epic 0). Commit
any crashers under `testdata/fuzz/` as regression seeds.

## 6. Where tests go

- Unit cases (level 1) go beside the code. This is the default.
- Registry-level cases (tag swap, multiple attestations, push/lookup/fetch
  against an in-process registry) go in `tests/integration/`. If that
  directory or its make target doesn't exist yet, write the case as a skipped
  unit test pointing to `TODO(milestone-2)` and say so in the summary. Don't
  invent the harness.

## 7. Gaps you can't close

Some cases can't be caught by design: an undeclared file, an unlisted env var,
a network service, the real toolchain version (invariant 2). Don't write a test
that pretends they are caught. Document each one in `docs/contract.md` ("What
does not go in" or "Future work") and, for trust gaps, in
`docs/trust-model.md` threats. Mention it in the summary.

## 8. Verify and report

1. Run `make ci`. Run `make ci-engine` while iterating.
2. If coverage, integration or fuzz targets exist in the Makefile, run them.
   Otherwise list them as not yet available.
3. Report: the cases added per family and their outcome, the skipped cases
   with the milestone they wait on, the fuzz targets and how long they ran,
   any bug found (fix it or open an issue, but don't hide it), and the
   documented gaps.
