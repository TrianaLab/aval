---
"aval": minor
---

Registry I/O is bounded. `run`, `verify` and `push` take `--registry-timeout` (default `1m`). A registry that stalls past the timeout gives outcome `error`, and `aval run` still runs the test. Lookup follows at most 64 referrers pages. A listed attestation whose manifest was deleted (404) counts as absent: the remaining candidates can still be reused, and the outcome is a miss if every candidate is gone. One credential store and one auth cache are shared by every registry call. The keyless trusted root (a TUF fetch when `trustedRoot` is not pinned) is loaded only when a candidate's bundle carries a certificate. After pushing, `push` and `run --push` confirm that the new digest is listed among the referrers, re-pushing up to three times, and fail if it never appears.
