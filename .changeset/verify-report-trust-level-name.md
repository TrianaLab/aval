---
"aval": minor
---

`aval verify -o json` (verify-report/v1) gains the optional `signer.trustLevelName` (`developer`, `runner` or `isolated`) next to the numeric `signer.trustLevel`. It is derived from the matched trust-policy entry, never from the predicate. Additive: older reports still parse, and in `pkg/evidence` `MatchSigner` now sets `Identity.TrustLevelName`.
