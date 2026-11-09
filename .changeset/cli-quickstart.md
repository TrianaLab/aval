---
"aval": patch
---

The quickstart follows the new flow: it removes a leftover registry before starting one, exports `AVAL_REGISTRY` and `AVAL_POLICY` once after `aval init`, and runs `aval run --push`, `aval verify`, `aval run`, a changed input and an untrusted push (`AVAL_POLICY= aval run --push --key other.key`) that ends in exit 5, with no flags repeated. The GIFs are re-recorded in the styled output, and the "Try it" terminal's `help` describes the same setup.
