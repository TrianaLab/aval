---
"aval": minor
---

`environment.env` accepts prefix patterns such as `APP_*` (#253): every
set variable whose name starts with the prefix is hashed, one `env NAME
<sha256>` line each, so a tool configured by prefix no longer needs every
name listed by hand. `*` may only end the entry; `*`, `*_TOKEN` and patterns
that would match `AVAL_RUN_STACK` (`AVAL_*`) are rejected. Names compare
case-sensitively on Linux and macOS and case-insensitively on Windows. A
pattern also hashes credentials that share the prefix; list exact names when
it covers secrets. `aval run` records the matched variables in its env file,
and `aval push --env-file` accepts the names a pattern matches. Contracts
without a pattern keep their fingerprint; older `aval` binaries reject a
contract that uses one.
