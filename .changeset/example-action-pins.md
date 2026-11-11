---
"aval": patch
---

`examples/github-monorepo` and the "Integrate Aval in 5 files" guide that
embeds it pin the GitHub actions at the commit that pins v0.8.2, with a
`# v0.8.2` comment, instead of a commit that installed v0.7.0 (#261). After
each pin PR merges, the new `pin-examples` job of `release.yaml` opens a PR
(branch `release-pins/examples`) that moves the examples to the merge
commit, and `pin-examples-ci` dispatches `ci` on it. An architecture test
fails when an example's action pin installs an older release than main pins
(one pin behind is allowed while that PR is open) or when its comment names
another version, and when a doc other than `docs/install.md` names a
hard-coded aval version. `docs/install.md` now pins v0.8.2 in its example
and says the verified-install flags first ship in v0.8.1. No CLI or format
change.
