# Measure reuse

Goal: find out how often Aval would reuse a result, and whether reuse would
have been right, before you let it skip any test.

```mermaid
flowchart LR
  run([aval run --shadow]) --> v[verify] --> t[run the test anyway]
  t --> same{same result?}
  same -- yes --> ok([reuse would be correct]):::ok
  same -- no --> fh([FALSE HIT, exit 1]):::err
  t --> rec([run record in the registry]):::muted
```

## The example

Run in shadow mode and record every run:

```sh
aval run --shadow --record --registry ghcr.io/acme/orders-aval --policy policy.yaml
```

`--shadow` verifies as usual, then runs the test anyway. If a verified result
exists and agrees with the run, Aval prints
`shadow: reuse of <reference> would have been correct`. If it disagrees, Aval
prints `aval: FALSE HIT: ...` and exits 1. A false hit usually means the contract
misses an input: fix the scope before you turn shadow mode off.

`--record` publishes one run record per run. A record looks like this:

```json
--8<-- "pkg/evidence/testdata/run-record-v1.golden.json"
```

## Record from where you can write

=== "Same job"

    The job that runs the test has write access to the registry:

    ```sh
    aval run --record --registry ghcr.io/acme/orders-aval --policy policy.yaml
    ```

=== "Separate job"

    The test job writes the record to a file. A job with write access, which
    runs no tests, publishes it:

    ```sh
    aval run --record-file record.json --registry ghcr.io/acme/orders-aval --policy policy.yaml
    aval record --record-file record.json --registry ghcr.io/acme/orders-aval
    ```

    Aval's own CI does this on `main` for its suites, into the public
    registry behind the site's [Stats](../stats.md) page.

## Read the numbers

`aval stats` reads the records back and sums them per test: hits, misses by
cause, verification failures, shadow false hits and time saved.

```sh
aval stats --registry ghcr.io/acme/orders-aval --since 7d
```

```text
run records in ghcr.io/acme/orders-aval since 2026-10-31T12:30:00Z
orders-e2e: 2 runs, 0 reused (hit rate 0.0%), test time 1m1s, saved 0s
  not reused: 1 no trusted result, 0 error, 1 verified but run (shadow)
  verification failures: 0, shadow false hits: 0
orders-unit: 1 runs, 1 reused (hit rate 100.0%), test time 0s, saved 1m30s
  not reused: 0 no trusted result, 0 error, 0 verified but run (shadow)
  verification failures: 0, shadow false hits: 0
total: 3 runs, 1 reused (hit rate 33.3%), test time 1m1s, saved 1m30s
  not reused: 1 no trusted result, 0 error, 1 verified but run (shadow)
  verification failures: 0, shadow false hits: 0
```

`--test` limits it to one test, `--by repository` groups by source
repository, and `-o json` prints the
[stats report](../artifact-format.md#stats-report-json). `aval stats` writes
nothing and never affects reuse.

??? info "Records are statistics, not evidence"
    Records are unsigned. `aval run` and `aval verify` never read them, so a
    forged record cannot cause reuse. Recording is best-effort: a record that
    cannot be published prints `aval: warning: run record not published` and
    never changes the exit code.

??? info "What the fields mean"
    `reused` is true when the test did not run. `savedDurationMs` is how long
    the reused run took when it was recorded. With `--shadow`, `reused` is
    always false. Every field is in
    [Artifact format: run records](../artifact-format.md#record-json).

## Read next

- [Outcomes](../concepts/outcomes.md)
- [Troubleshooting](troubleshooting.md)
