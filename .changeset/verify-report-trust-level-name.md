---
"aval": minor
---

`aval verify -o json` (verify-report/v1) gains the optional `signer.trustLevelName` (`developer`, `runner` or `isolated`) next to the numeric `signer.trustLevel`. It is derived from the matched trust-policy entry, never from the predicate. Additive: older reports still parse, and in `pkg/evidence` `MatchIdentity` and `MatchSigner` now set `Identity.TrustLevelName`. Older aval binaries (0.4.0 and earlier) do not recognise a report with this field as Aval output, so such a file in scope counts as an ordinary input for them (a miss, never a false hit) until they upgrade.
