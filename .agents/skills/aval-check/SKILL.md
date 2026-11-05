---
name: aval-check
description: Runs Aval's validation ladder (quick, coverage, registry, e2e, dogfood), picks the levels a diff needs, and reports each result with the command and its real output. Use before pushing, before opening or updating a PR, or when asked to "check", "validate", "run the tests", "run CI locally", "does this pass", "run the e2e" or "dogfood" in the Aval repo.
license: Apache-2.0
metadata:
  project: aval
compatibility: Needs Go, make and golangci-lint.
---

# aval-check

Run the checks, report what they printed. The checks themselves live in make
targets and Go tests; this skill only decides what to run and how to report it.
Rules, commands and test levels come from `AGENTS.md` (sections Commands and
Tests). Do not restate or reinterpret them here.

## Choose the levels

Get the diff (`git diff --name-only` against the base branch, plus unstaged
changes). `quick` always runs. Add levels by path:

| Paths touched | Add |
|---|---|
| any `.go` file, `go.mod`, `go.sum` | coverage |
| `internal/registry`, `internal/attest`, `internal/policy`, `pkg/evidence` | registry, e2e |
| `cmd/aval` (exit codes, flags, `verify --output json`) | e2e |
| `internal/fingerprint`, `internal/contract` | e2e, dogfood |
| `aval*.yaml`, `.github/workflows/`, `Makefile` | dogfood |
| only `docs/`, `*.md`, `.agents/skills/` | quick only |

If the user names a level, run that level (plus `quick`). If unsure, run more,
not less (invariant 3 applies to checking too).

## Levels

For every level: if the target or test directory exists, run it; otherwise
mark the level `not yet available` and name the milestone or epic that adds
it. Never substitute an ad hoc script for a missing target and call it a pass.

### quick

Run `make ci` (= `ci-static` + `ci-gates` + `ci-engine`). If it fails, run the
failing leg alone to isolate it. When Epic 0 adds legs to `make ci`, they run
here automatically.

### coverage

If a coverage target exists (Epic 0), run it; it must report 100% statement
coverage outside the paths excluded in `AGENTS.md` (Tests). Until then run
`go test -coverprofile=/tmp/aval.cover ./...` and
`go tool cover -func=/tmp/aval.cover | tail -1`, and report the number as
informational, not as pass or fail. List packages that dropped.

### registry

Milestone 2. Level 2 integration tests (`tests/integration/`) against an
in-process OCI registry (AGENTS.md "Tests"): push, lookup by tag
`sha256-<fingerprint>`, fetch by digest (invariant 1), with and without
referrers support so the fallback path is exercised. If the directory or
target does not exist, report `not yet available` (milestone 2 / Epic 0).

### e2e

Local acceptance (level 4, `tests/acceptance/`) with local key signing. The
suite must cover:

- hit: matching signed attestation, reused, exit 0
- miss: no attestation, exit 4
- tampered attestation: exit 5
- signer outside policy: exit 5
- replay of a valid attestation under a different fingerprint or test id: exit 5
- CLI exit codes as declared in `cmd/aval/main.go`
- `verify --output json` matches the report schema in `docs/artifact-format.md`

If the suite does not exist, report `not yet available` and list which of the
cases above are covered elsewhere (unit tests) and which are not covered at
all. An exit 5 case that passes with exit 4 is a failure (invariant 4).

### dogfood

Run Aval's own contracts exactly as CI does. Skip if there is no `aval*.yaml`
at the repo root yet.

1. Fingerprint reproducibility: build `bin/aval`, copy the repo to two
   different temp directories, and run
   `aval run -f <contract> --fingerprint-only` in each, for every contract.
   The outputs must be byte-identical. Any difference is a failure; rerun with
   `--explain` in both copies and diff the manifests to show the cause.
2. Shadow mode: if `aval run --shadow` exists, run it per contract as CI does
   and report hit/miss/verification-failed counts. Otherwise mark
   `not yet available`. For trend reports use `aval-dogfood-report`.

## Report

One table, one row per level considered:

| Level | Command | Result |
|---|---|---|
| quick | `make ci` | pass / fail / skipped / not yet available |

Rules:

- Never claim `pass` without having run the command in this session and seen
  its output. If you did not run it, say so.
- On failure, quote the relevant output (the failing test name and assertion,
  or the lint line) under the table. Do not paraphrase errors.
- Any exit 5 or `verification-failed` seen where it was not expected is
  reported first, as a security finding, never folded into "some tests
  failed".
- State which levels were skipped by the path rules and why.
- Do not fix code as part of this skill unless the user asks; report and stop.
