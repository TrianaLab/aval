---
"aval": patch
---

The release gate verifies the e2e attestation with the aval release `ci.yaml`
pins, the one that signed it, instead of a binary built from the tag (#234).
A newer `aval` computes `aval-fingerprint/v2`, so the gate missed (exit 4) on
v0.8.0 and published nothing: the v0.8.0 tag has no GitHub Release, and this
patch ships its changes as v0.8.1.
