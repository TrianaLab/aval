---
"aval": patch
---

Fingerprinting fails closed in two more cases: a `scope.source` glob that matches no file (a typo, or a name that differs on disk by case or Unicode form), and a file excluded only because a trailing `**` in `exclude` matched zero segments (`build/**` against a file named `build`). Trees that fingerprinted before and still do keep the same fingerprint. A table-driven false-hit corpus now pins every kind of relevant and irrelevant change.
