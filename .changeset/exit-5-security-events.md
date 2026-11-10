---
"aval": minor
---

Every security event Aval detects now exits 5 (#195). A verified result beside
an attestation that failed verification is no longer reused: `aval run` runs
the test and exits 5, and `aval verify` exits 5 with the unchanged report
(outcome `verified`, the bad attestation in `rejected`). More than 64
attestations for one fingerprint is now outcome `verification-failed` (exit 5
in `aval run` and `aval verify`) instead of `error`. The run record of such a
run says `verification-failed`, so `aval stats` counts it under
`verificationFailures`. Consumers no longer need to inspect `rejected`: the
exit code alone separates miss, error and security event. No new outcome or
exit code; the verify report, predicate and fingerprint are unchanged.
