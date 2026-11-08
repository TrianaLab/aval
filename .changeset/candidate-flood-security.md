---
"aval": patch
---

More than 64 attestations for a fingerprint now prints an `aval: SECURITY:` line naming the cap, as a possible flooding attempt; the outcome stays `error` and the test runs. Aval's own CI and release jobs fail when a verify report lists a verification failure in `rejected`, even when a trusted result verified.
