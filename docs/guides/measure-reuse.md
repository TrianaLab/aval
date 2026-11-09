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

## Estimate from history

Before any setup, estimate reuse from past commits.
[`scripts/estimate-reuse.sh`](https://github.com/TrianaLab/aval/blob/main/scripts/estimate-reuse.sh)
checks out each commit in a scratch `git worktree`, runs
`aval run --fingerprint-only` there, and counts a fingerprint seen on an
earlier commit as a hit. From the repository root:

```sh
scripts/estimate-reuse.sh services/orders/aval.yaml --max-count=200 main
```

```text
<commit> miss sha256:4800...
<commit> hit sha256:4800...
<commit> miss unacknowledged mutable reference
<commit> miss aval: fingerprint: source glob "gone/**" matched no files
200 commits, 61 would reuse a result (30%, an upper bound)
```

A fingerprint error or an unacknowledged mutable reference is a miss, as
in CI, and a non-deterministic contract never reuses. The number is an
upper bound: it ignores branches, failing tests and trust policy. Shadow
mode measures the real one. There is no `aval estimate`: reading CI
history would tie Aval to one CI vendor's API.

## Measure in shadow mode

Run in shadow mode and record every run:

```sh
aval run --shadow --stats --registry ghcr.io/acme/orders-aval --policy policy.yaml
```

| You see | It means |
|---|---|
| `shadow: reuse of <ref> would have been correct` | safe to turn shadow off for this test |
| `aval: FALSE HIT: ...` (exit 1) | the contract misses an input: fix `scope` |

## Record where you can write

`--stats` publishes one run record per run, next to your results. To keep
records apart, write them to a file and publish them with `aval stats publish`
to a registry of your own:

```sh
aval run --stats-file record.json --source-repo acme/orders \
  --registry ghcr.io/acme/orders-aval --policy policy.yaml
aval stats publish --registry ghcr.io/acme/aval-stats record.json
```

A test job without write access only writes the file; a job with write
access, which runs no tests, runs `aval stats publish`.

## Read the numbers

`aval stats` reads the records back and sums them per test: hits, misses by
cause, verification failures, shadow false hits and time saved.

```sh
aval stats --registry ghcr.io/acme/aval-stats --since 7d
```

`--by repository` needs `--source-repo <name>` on `aval run` or `aval stats publish`.

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
