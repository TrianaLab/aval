---
"aval": minor
---

The GitHub run action takes `lookup-only: "true"` (#252): it runs `aval
verify` with the default branch's trust policy and trust minimum, sets the
`outcome` output and never runs the test, uploads or writes `.aval/`. A
later setup step can be skipped on `verified`; the run step after it still
decides reuse. A miss succeeds the step, exit 5 fails it with the `aval
SECURITY` annotation, and `lookup-only` with `shadow: true` exits 2.
