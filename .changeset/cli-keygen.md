---
"aval": minor
---

`aval keygen [name]` writes a signing key without openssl: `name.key` (unencrypted PKCS#8 ECDSA P-256 PEM, mode 0600) and `name.pub` (PKIX PEM, the file a policy trusts with `publicKey`), with `aval` as the default name. It prints the key id the verify report shows for this signer and refuses to replace either file without `--force` (exit 1). The key hints of `aval run --push` and `aval push` now point to `aval keygen`, and the quickstart no longer needs openssl.
