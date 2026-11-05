---
"aval": patch
---

`aval run` and `aval verify` escape newlines and control characters in reasons printed as text and on stderr, so text from an untrusted attestation cannot start a log line of its own. Aval's own CI now reuses its signed e2e results, and a release requires a verified e2e attestation for its commit.
