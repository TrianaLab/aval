---
"aval": patch
---

The aval run action takes a `shadow` input (default `false`): `true` passes
`aval run --shadow`, so the test runs beside a verified result and a false
hit fails the step; any value other than `true` or `false` exits 2 before
anything runs (#216). The job's `AVAL_MIN_TRUST_LEVEL` still reaches aval,
which only lets it raise the policy's minimum. Aval's own CI now runs its
suites through `integrations/github/run` and publishes through
`integrations/github/publish`: `aval.yaml` is a `kind: TestSet` with the
tests `aval-engine` and `aval-e2e` (same fingerprints as the former
`aval.yaml` and `aval.e2e.yaml`), one `.github/aval/policy.yaml` carries the
registry and replaces `main.yaml` and `pr.yaml` (main and releases raise the
minimum to `runner`), and `.github/aval/install.sh`, `trust-from-base.sh`
and `summary.sh` are removed. The release gate verifies with the release
the actions pin, under main's policy. No change to the aval binary, its
exit codes or formats.
