---
"aval": patch
---

`pkg/evidence`: `ErrNotImplemented`, `Verification.TrustLevel` (use `Verification.Signer.TrustLevel`) and `Runner.Issuer`/`Runner.TrustLevel` are deprecated; nothing is removed. Pushing an attestation without a Sigstore bundle is now refused, since it could never verify. Keyless signing always uses the Sigstore public-good instance, as the CLI already did.
