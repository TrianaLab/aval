---
"aval": minor
---

`aval init --set` writes a two-file starting layout (#215): `aval.yaml` as a
`kind: TestSet` with the test and one fragment holding the source globs, and
`.github/aval/policy.yaml` with one placeholder keyless identity whose
anchored `subjectRegexp` trusts no signer until `<OWNER>/<REPO>` is replaced.
Scope and cacheability stay required. An existing file exits 2 unless
`--force`. No format change.
