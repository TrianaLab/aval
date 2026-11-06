---
"aval": patch
---

The README now has an Install section and a local quickstart (registry:2, an openssl P-256 key, `aval run --push --key`, `aval verify`) that reaches `verified`; an acceptance test runs it as written. A missing `aval.yaml` and a pre-freeze `aval.dev/` `apiVersion` (contract or policy) now print a next step. `aval verify --help` lists exit codes 1 and 2, and `docs/cli-reference.md` has one exit-code table for every command. New docs: registry credentials, the network and HOME needs of keyless verification and how to pin `trustedRoot`, and the 64-candidate cap with its cleanup.
