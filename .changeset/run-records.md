---
"aval": minor
---

Run statistics in the registry. `aval run --record` publishes one run record per run, for every outcome, with the test id, fingerprint, outcome, whether the result was reused, the result, the test duration, the saved duration, the aval version, a timestamp and an optional `--ci-run-url`. Records hang off a per-test stats anchor tagged `stats-<test id>`, never off a fingerprint anchor. `run` and `verify` never read them, so a record cannot cause reuse or count toward the candidate cap. Recording is best-effort: a record that cannot be published, for example without write credentials, is a warning and never changes the exit code. `--record-file` writes the record to a file and `aval record` publishes it later. New format identifiers, provisional: `aval.trianalab.io/run-record/v1`, `application/vnd.trianalab.aval.run.v1` (`+json` for the layer) and `application/vnd.trianalab.aval.stats.v1`. `pkg/evidence` adds `RunRecord`, `ParseRunRecord` and these constants.
