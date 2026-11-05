---
"aval": minor
---

Formats are frozen. The contract and policy `apiVersion` is now `aval.trianalab.io/v1` and the verify report `version` is `aval.trianalab.io/verify-report/v1`; `aval.dev/v1alpha1` contracts and policy files are rejected, so change that line in yours. The contract hash covers `apiVersion`, so results published before are misses and each test runs once. The predicate type (`https://aval.trianalab.io/attestation/execution/v2`), media and artifact types (`application/vnd.trianalab.aval.execution.v2`, `application/vnd.trianalab.aval.fingerprint.v1`) and predicate `schemaVersion` `v2` are unchanged and now frozen, so attestations written by v0.1.0 keep a valid, readable format. Compatibility rules (additive changes within a version, what needs a new version, how consumers treat unknown fields and values, deprecation) are in `docs/artifact-format.md`, and golden files guard the JSON shape of the statement, predicate and verify report.
