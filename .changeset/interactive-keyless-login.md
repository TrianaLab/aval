---
"aval": minor
---

`run --push` and `push` sign keyless from a laptop: without `--key` or `$SIGSTORE_ID_TOKEN` and with a terminal (or with the new `--login` flag), they open the interactive Sigstore login in the browser (`oauth2.sigstore.dev`, client id `sigstore`; `$SIGSTORE_OIDC_ISSUER` and `$SIGSTORE_OIDC_CLIENT_ID` override them) right before signing, after the test ran. The token is used for that signature only and never stored. Without a terminal and without `--login`, a run with no key and no token still fails fast with exit 2. `--key` and `--login` exclude each other.
