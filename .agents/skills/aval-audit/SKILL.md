---
name: aval-audit
description: Reviews the current Aval diff (committed on the branch plus uncommitted changes) against the AGENTS.md invariants, docs/code alignment, test level placement, false-hit coverage, TODO hygiene, frozen format identifiers, exit code and verify report stability, pkg/evidence API compatibility, consumer coupling and vendor agnosticism. Produces blockers, warnings and nits with file:line, the rule cited and the exact suggested edit. Read-only, never edits files. Use when the user says "audit my changes", "review the diff", "check before push", "check before PR", "does this break an invariant" or "is this ready".
license: Apache-2.0
metadata:
  project: aval
compatibility: Requires git and Go; run from the aval repository root.
---

# aval-audit

Read-only review of the pending change. The deliverable is the findings list.
Do not edit, stage or commit anything; the user or the implementing session
applies the fixes. Rules live in `AGENTS.md`; cite them, do not restate them.

## 1. Scope the diff

Run:

```sh
git fetch origin main --quiet || true
base=$(git merge-base origin/main HEAD)
git diff --stat $base                # committed + uncommitted vs the base
git status --short                   # untracked files count too
```

Review that diff plus untracked files; every `git diff $base` below uses the
same base. If the diff is empty, say so and stop. Read `AGENTS.md`
fully, then every changed file in full (not only the hunks), plus the docs
that describe the touched area (`docs/contract.md`, `docs/artifact-format.md`,
`docs/trust-model.md`, `docs/vision.md`, `docs/integrations.md`).

## 2. Run the existing gates

Real checks live in make targets; the audit reports their result, it does not
reimplement them.

- `make ci-static` and `make ci-gates`. Any failure is a blocker.
- `make ci-engine` if the diff touches Go code.
- Coverage gate, `tests/integration`, `tests/acceptance`, fuzzing, security
  and docs lint arrive with Epic 0: if the target exists in the `Makefile`,
  run it; otherwise list it under "not yet available". Never report it as passed.

## 3. Checklist

Run each command and inspect every hit that falls in the diff. Each comment
names the rule a hit may break. Add `--untracked` to `git grep` when
`git status` shows new files.

```sh
# invariant 9: consumer coupling (docs may mention Pacto, code may not)
git grep -n -i -E 'pacto|evidenceset|readiness' -- '*.go' ':!examples/'
# invariant 8: vendor hard-coding in core paths
git grep -n -E 'ghcr\.io|docker\.io|gcr\.io|quay\.io|token\.actions\.githubusercontent\.com|gitlab\.com|accounts\.google\.com' \
  -- '*.go' ':!*_test.go' ':!examples/' ':!tests/'
# invariant 11: pkg/evidence must not reach internal/ or cmd/ (expect no output)
go list -deps ./pkg/evidence | grep -E 'aval/(internal|cmd)'
git diff $base -- go.mod
# invariants 10, 11: removed/renamed exported identifiers or JSON tags
git diff $base -- pkg/evidence | grep -E '^-.*(func |type |const |var |json:")'
# exit codes are API (AGENTS.md "Layout"): values never change or disappear
git diff $base -- cmd/aval/main.go | grep -E '^[-+].*Exit'
# invariant 10: verify report fields only added, else ReportVersion bumps
git diff $base -- pkg/evidence/report.go
# invariant 10: versioned format strings touched (see aval-format-change)
git diff $base | grep -E 'aval-fingerprint/v|apiVersion|application/vnd\.|predicateType|ReportVersion'
# invariant 4: every handling site of miss vs verification failure
git grep -n -E 'ErrVerification|ErrNotFound|ExitMiss|ExitVerificationFailed' -- '*.go'
# AGENTS.md "Work tracking": TODOs must name a milestone
git grep -n -E 'TODO|FIXME|XXX' | grep -v -E 'TODO\(milestone-[0-9]+\)'
# AGENTS.md "Style": frozen identifiers change only per aval-format-change
# (a hit that changes an identifier value needs a new identifier and docs/artifact-format.md updated)
git diff $base | grep -E '^-.*(trianalab\.io|vnd\.trianalab|aval-fingerprint/v)'
```

`make ci-gates` (`TestEvidenceAPIIsAppendOnly`) enforces the append-only
`pkg/evidence` API snapshot (`docs/integrations.md`); if `apidiff` or
`gorelease` is installed, also run it on `./pkg/evidence` against `main`.

For invariant 4, read each hit: an `ErrVerification` that is ignored,
logged-and-continued, retried, mapped to a miss or to exit 1 is a blocker.

## 4. Judgement checks (no single grep)

- **Docs/code alignment.** Every behaviour change is reflected in `docs/`;
  no doc claims behaviour the code lacks; future work carries its milestone.
  Flags, exit codes and report fields in docs match `cmd/aval` and
  `pkg/evidence`.
- **Test level placement.** Each new test sits at the level matching what it
  proves (AGENTS.md "Tests"). Level 2 must not reach a network host. Semantic
  assertions in shell scripts belong in Go. Prefer level 1.
- **False-hit coverage.** A change in `internal/fingerprint`,
  `internal/contract`, `internal/policy`, `internal/registry`,
  `internal/attest` or `pkg/evidence` verification without new false-hit cases is a blocker
  (see `aval-false-hit`).
- **Fail closed.** Every new branch that decides reuse defaults to running
  the test; `non-deterministic` is never reusable (invariants 3, 7).
- **Trust from policy.** No trust decision reads signer identity or level from
  the predicate (invariant 5).
- **Scope inference.** Nothing derives inputs outside `aval.yaml` (invariant 2).
- **Identity vs fingerprint.** The fingerprint is never used as the test
  identity, and identity checks use `metadata.name` / `ExpectedTestID`, not
  the digest (invariant 6). The name does enter the fingerprint through the
  contract hash; that is intended (docs/contract.md).
- **Infrastructure.** No new server, database or own registry; lookup by tag,
  consumption by digest (invariant 1). Registry/signing code that is not
  part of the issue being implemented stays a stub with `TODO(milestone-2)`;
  code that resolves a TODO removes the marker.

## 5. Severity

- **Blocker:** breaks an invariant, a failing gate, a non-additive change to a
  versioned format or pkg/evidence, missing false-hit cases on a sensitive
  package, a swallowed `ErrVerification`.
- **Warning:** docs drift, test at the wrong level, untagged TODO, new
  dependency in pkg/evidence.
- **Nit:** naming, prose style (AGENTS.md "Style"), comment wording.

## 6. Output

Return exactly this shape, most severe first. Omit empty sections except the
summary line.

```
Audit: <branch> vs main — <N> blockers, <N> warnings, <N> nits

Gates: ci-static <pass|fail>, ci-gates <pass|fail>, ci-engine <pass|fail|skipped>
Not yet available: <targets missing from the Makefile>

## Blockers
1. <file>:<line> — <what is wrong>
   Rule: <invariant N | AGENTS.md section | doc>
   Edit: <exact replacement text or diff hunk>

## Warnings
...

## Nits
...
```

Every finding needs file:line, a cited rule and a concrete edit. A finding
without an edit is a question: ask the user instead of guessing. If nothing
survives, say "no findings" and list the checks run.
