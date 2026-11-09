# Shards and matrices

Goal: one result and one fingerprint per CI shard, each bound only to what
that shard reads.

```mermaid
flowchart LR
  m([matrix]) --> s1[shard 1: aval run -f aval.shard-1.yaml] --> r1([result 1]):::muted
  m --> s2[shard 2: aval run -f aval.shard-2.yaml] --> r2([result 2]):::muted
  c[change under packages/app] -. reruns .-> s2
```

No new field: `-f`, `scope` and `environment.env` do it. Example:
[`examples/uv-monorepo`](https://github.com/TrianaLab/aval/tree/main/examples/uv-monorepo).

## Pattern A: one contract per shard

Best reuse. Each shard has its own contract, side by side at the root:

```yaml
--8<-- "examples/uv-monorepo/aval.shard-1.yaml"
```

Shard 2 tests `app`, which imports `core`, so `core` is in its scope:

```yaml
--8<-- "examples/uv-monorepo/aval.shard-2.yaml"
```

A change under `packages/app` reruns shard 2 only. A change to `core` or
`uv.lock` reruns both.

## Pattern B: one contract, shard variables

Simpler, less reuse: a change anywhere reruns every shard.

```yaml
--8<-- "internal/fingerprint/testdata/shards/pattern-b.yaml"
```

!!! warning "Declare both variables"
    Leave `SHARD_INDEX` or `SHARD_TOTAL` out of `environment.env` and shards
    share a fingerprint: shard 2 reuses shard 1's result, a false hit.

## The matrix

One job per shard, each with its own `--result-file` and coverage `outputs`:

```yaml
--8<-- "examples/uv-monorepo/workflow.yaml"
```

The same job locally, with `AVAL_REGISTRY` and `AVAL_POLICY` exported as in
the workflow:

```sh
aval run -f aval.shard-2.yaml --result-file .aval/shard-2.json
```

To publish, add the push step from [Use GitHub Actions](github-actions.md).

## Read next

- [Depend on another part of the repo](depend-on-a-contract.md)
- [Use GitHub Actions](github-actions.md)
