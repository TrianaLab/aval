---
"aval": patch
---

The GitHub publish action downloads the `aval-*` artifacts right after its
guard (#260). With none, it prints a notice and exits 0 without installing
cosign or aval, fetching the policy or requesting an OIDC token, so a publish
job that runs on `!cancelled()` after path-filtered test jobs no longer fails
in a repository whose policy is not merged yet. With artifacts and no policy
it still exits 1.
