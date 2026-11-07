# Measure reuse

Goal: find out how often Aval would reuse a result, and whether reuse would
have been right, before you let it skip any test.

```mermaid
flowchart LR
  run([aval run --shadow]) --> v[verify] --> t[run the test anyway]
  t --> same{same result?}
  same -- yes --> ok([reuse would be correct]):::ok
  same -- no --> fh([FALSE HIT, exit 1]):::err
```

## The example

Run in shadow mode and record every run:

```sh
aval run --shadow --record --registry ghcr.io/acme/orders-aval --policy policy.yaml
```

| You see | It means |
|---|---|
| `shadow: reuse of <ref> would have been correct` | safe to turn shadow off for this test |
| `aval: FALSE HIT: ...` (exit 1) | the contract misses an input: fix `scope` |

## Record where you can write

`--record` publishes one run record per run, next to your results. To keep
records apart, write them to a file and publish them with `aval record` to a
registry of your own:

```sh
aval run --record-file record.json --repository acme/orders \
  --registry ghcr.io/acme/orders-aval --policy policy.yaml
aval record --record-file record.json --registry ghcr.io/acme/aval-stats
```

A test job without write access only writes the file; a job with write
access, which runs no tests, runs `aval record`.

## Read the numbers

`aval stats` reads the records back and sums them per test: hits, misses by
cause, verification failures, shadow false hits and time saved.

```sh
aval stats --registry ghcr.io/acme/aval-stats --since 7d
```

`--by repository` needs `aval run --repository <name>`.

```text
run records in ghcr.io/acme/aval-stats since 2026-10-31T12:30:00Z
orders-unit: 1 runs, 1 reused (hit rate 100.0%), test time 0s, saved 1m30s
  not reused: 0 no trusted result, 0 error, 0 verified but run (shadow)
  verification failures: 0, shadow false hits: 0
total: 1 runs, 1 reused (hit rate 100.0%), test time 0s, saved 1m30s
  not reused: 0 no trusted result, 0 error, 0 verified but run (shadow)
  verification failures: 0, shadow false hits: 0
```

`--test` limits it to one test, and `-o json` prints the
[stats report](../artifact-format.md#stats-report-json). `aval stats` writes
nothing and never affects reuse.

??? info "Records are statistics, not evidence"
    Records are unsigned. `aval run` and `aval verify` never read them, so a
    forged record cannot cause reuse. Recording is best-effort: a record that
    cannot be published prints `aval: warning: run record not published` and
    never changes the exit code.

??? example "What the fields mean"
    `reused` is true when the test did not run. `savedDurationMs` is how long
    the reused run took when it was recorded. With `--shadow`, `reused` is
    always false. Every field is in
    [Artifact format: run records](../artifact-format.md#record-json).

    ```json
    --8<-- "pkg/evidence/testdata/run-record-v1.golden.json"
    ```

## Read next

- [Stats](../stats.md): the public numbers
- [Outcomes](../concepts/outcomes.md)
- [Troubleshooting](troubleshooting.md)
