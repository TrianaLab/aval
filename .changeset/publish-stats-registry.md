---
"aval": minor
---

Publish action: new `stats-registry` input (`host[:port]/path`, validated; empty keeps the policy's registry). With `stats: true` it now publishes the run record of every artifact, verified hits and verification failures included, not only those whose result was pushed, so a consumer's runs reach a shared stats registry such as `ghcr.io/trianalab/aval-stats`. A failed record publish is still a warning; exit 5 handling is unchanged.
