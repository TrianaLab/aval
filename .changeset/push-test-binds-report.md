---
"aval": minor
---

`aval push --test <name>` publishes only the test an artifact is named after
(#210). A result or report for another test, or for inputs other than the
ones push recomputes from the checkout (a changed file or declared env value),
exits 1 and publishes nothing. Behaviour changes: a report that records a
security event (outcome `verification-failed`, or a `rejected` entry that
failed verification) now makes push sign nothing and exit 5, where it used to
publish; and `aval push` never publishes a `non-deterministic` test, it prints
`nothing to push: ...` and exits 0. `aval run --push` is unchanged.
