---
"aval": minor
---

`aval push --report-file FILE` reads the report written by the `aval run` that
recorded the result (#201). When its outcome is `verified`, push prints
`nothing to push: ...`, signs nothing and exits 0, so split-credential
publishing is one command and a stale result file is not published again.
Any other outcome publishes the result as before. A report for another test id
or fingerprint, an unknown report version or an unreadable report exits 1. No
format changes: the predicate, verify report, fingerprint and exit codes are
unchanged.
