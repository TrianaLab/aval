---
"aval": patch
---

A signer the trust policy lists but below `minTrustLevel` is now reported as `miss` (exit 4), and with outcome `miss` in the verify report's `rejected` list, instead of `verification-failed` (exit 5). Such an attestation is still never reused, and a false claim from that signer stays `verification-failed`. The report stays `aval.trianalab.io/verify-report/v1`; `rejected[].outcome` may now also be `miss`. Rejected verification failures are printed with the `aval: SECURITY:` prefix even when another candidate verifies. `evidence.Verify` is unchanged.
