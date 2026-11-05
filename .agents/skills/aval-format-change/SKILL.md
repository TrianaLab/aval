---
name: aval-format-change
description: Guides any change to one of Aval's versioned formats (predicate type, media types, artifact type, predicate schemaVersion, the verify report and its outcomes, the contract apiVersion, the fingerprint manifest version, CLI exit codes). Classifies the change as additive or breaking and walks through types, docs, golden files, version identifiers and release notes. Use when the user says "add a field to the predicate", "change the verify report", "new outcome", "new exit code", "bump the fingerprint version", "change aval.yaml schema", "rename a media type", or when a diff touches any of the identifiers listed in the skill.
license: Apache-2.0
metadata:
  project: aval
compatibility: Requires Go and make; run from the repository root.
---

# aval-format-change

Rules come from `AGENTS.md` (invariants 9, 10, 11; Style: provisional markers)
and `docs/artifact-format.md` "Versioning and compatibility". This skill is the
process; do not restate those rules in code comments or docs.

## 1. Identify the format

Find which identifiers the change touches. Each owns a version:

| Format | Identifier | Lives in | Spec |
|---|---|---|---|
| Predicate / OCI artifact | `evidence.PredicateType`, `SchemaVersion`, `MediaTypeExecution`, `ArtifactType`, `MediaTypeDSSE` usage | `pkg/evidence/evidence.go` | `docs/artifact-format.md`, `docs/integrations.md` |
| Verify report | `evidence.ReportVersion`, `Report` fields, `Outcome` values | `pkg/evidence/report.go` | `docs/artifact-format.md`, `docs/integrations.md` |
| Contract | `contract.APIVersion` | `internal/contract/contract.go` | `docs/contract.md` |
| Fingerprint manifest | `fingerprint.Version` | `internal/fingerprint/fingerprint.go` | `docs/artifact-format.md`, `docs/contract.md` |
| CLI exit codes | `Exit*` constants | `cmd/aval/main.go` | `docs/artifact-format.md` (outcomes table), `docs/integrations.md` |

A change can touch more than one (a new outcome usually means a new exit code).
Handle each format separately.

## 2. Classify: additive or breaking

Additive (same identifier):
- New optional field in the predicate or report that old consumers can ignore.
- New key under `annotations`.
- New optional contract field. Note: contract parsing rejects unknown fields,
  so older `aval` binaries reject contracts that use it. Old contracts keep
  working, which is what "additive" means here; say so in the release notes.
  A new contract field changes the canonical contract hash unless it is
  tagged `json:",omitempty"` and empty/omitted hashes the same as before.
  Prove it with a test that an existing contract's fingerprint is unchanged;
  if it can't be, treat it as a `fingerprint.Version` bump (3b.4).

Breaking (new identifier):
- Rename, removal, type change, or meaning change of any existing field.
- Making an optional field required, or tightening validation of an existing one.
- New `Outcome` value or new or reassigned exit code. Strict consumers switch
  on these exhaustively; treat every new enum value as breaking.
- Anything that changes which bytes go into the fingerprint manifest or how
  they are hashed (`fingerprint.Version` bump).

If unsure, it is breaking. Ask the user before choosing additive for a
borderline case.

Reject the change if it adds a field that models a consumer concept (Pacto
readiness, gates, delivery, EvidenceSet). That data belongs in `annotations`
(invariant 9).

## 3a. Additive path

1. Update the Go types. Keep `omitempty` on new optional JSON fields so
   existing outputs stay byte-identical. For contract fields this is also
   what keeps existing fingerprints stable; test it (see section 2). `pkg/evidence` must stay free of
   `internal/` imports (invariant 11).
2. Update the field table and examples in `docs/artifact-format.md` or
   `docs/contract.md`, and `docs/integrations.md` when the verify report,
   outcomes, exit codes or the pkg/evidence API change. Do not document behaviour the code does not have.
3. Golden files: if `testdata/*.golden.json` tests exist for the format, update
   them and show the diff to the user. If they do not exist yet (Epic 5 adds
   them), add one for the touched format with a test that compares marshalled
   output to it, so the next change shows up as a diff.
4. Add unit tests: a document without the new field still parses, and one with
   it round-trips.
5. Write a release-notes entry. There is no `CHANGELOG` file yet; put the note
   in the PR description under a "Format changes" heading so it lands in the
   release notes.

## 3b. Breaking path

1. Mint a new identifier (e.g. `.../execution/v2`, `execution.v2`,
   `verify-report/v1alpha2`, `aval.dev/v1alpha2`, `aval-fingerprint/v2`). Never
   reuse or recycle an old identifier, not even one that was never released.
2. Predicate and media types move together: new `PredicateType`,
   `MediaTypeExecution`, `ArtifactType` and `SchemaVersion` (see
   `docs/artifact-format.md`).
3. Decide with the user whether old documents need an accept window. If yes,
   keep the old parser beside the new one and dispatch on the identifier;
   unknown identifiers fail closed (invariant 3). If no, remove the old
   constant and say so in the notes.
4. Fingerprint version bump: every existing attestation becomes a miss. That
   is correct and fail closed; do not add a migration or a fallback lookup by
   the old version. State the cache invalidation in the docs and release notes.
   Pair it with `aval-false-hit` cases.
5. New outcome or exit code: keep invariant 4 intact (miss and
   verification-failed stay distinct, exit 4 vs 5). Update the outcomes table,
   `exitCode` in `cmd/aval/main.go`, and its tests.
6. Do the additive-path steps 2 to 5 for the new version.

## 4. Provisional markers

Until the Epic 5 issue "Freeze predicate, media types and verify report v1"
lands, every identifier keeps its PROVISIONAL marker in
`docs/artifact-format.md`, `docs/contract.md`, `docs/integrations.md` and
`docs/vision.md`. Do not remove them in a format change. Pre-freeze breaking
changes still get a new identifier. If `docs/artifact-format.md` still says
the rules only apply from v1, raise it with the user as docs/AGENTS.md drift
(invariant 10 wins); do not follow the doc.

## 5. Verify

1. Run `make ci`.
2. If coverage, integration (`tests/integration`), acceptance
   (`tests/acceptance`) or fuzz targets exist in the `Makefile`, run them;
   otherwise note each as not yet available (Epic 0). Registry and signing
   paths (milestone 2) are the same: run them if present, else note it.
3. Run the `aval-audit` skill on the diff. Always.

## 6. Notify consumers

Consumers (Pacto, external tools) learn about format changes from the release
notes only. Never add consumer-specific fields, flags or compatibility shims.
Report to the user: format, classification, old and new identifier, and the
release-notes text.
