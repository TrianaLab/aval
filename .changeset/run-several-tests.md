---
"aval": minor
---

The GitHub run action takes several tests in one job: `test` accepts names and glob patterns (`test: go.*`). They run one after the other with one aval install; the step exits with the worst code after running them all, 5 never downgraded. `outcome` is the worst outcome and a new `outcomes` output holds each one. On main the tests share one artifact `aval-.<id>`, which the publish action pushes test by test. A single test works as before.
