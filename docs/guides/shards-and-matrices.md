# Shards and matrices

Goal: a test suite split into shards across CI matrix jobs gets one result and
one fingerprint per shard, each bound only to what that shard reads.

```mermaid
flowchart LR
  m([matrix]) --> s1[shard 1: aval run -f aval.shard-1.yaml] --> r1([result 1]):::muted
  m --> s2[shard 2: aval run -f aval.shard-2.yaml] --> r2([result 2]):::muted
  c[change under packages/app] -. reruns .-> s2
```

No new field: shards use `-f`, `scope` and `environment.env`. The example is
[`examples/uv-monorepo`](https://github.com/TrianaLab/aval/tree/main/examples/uv-monorepo),
a uv workspace with two packages, which Aval's CI runs.

## Pattern A: one contract per shard

Each shard has its own contract, `metadata.name` and scope. A file holds one
contract and `-f` takes one path, so put the shard contracts side by side,
each under its own name. Globs cannot contain `..`: a contract lives in the
directory that holds all its inputs, here the workspace root.

```yaml
--8<-- "examples/uv-monorepo/aval.shard-1.yaml"
```

Shard 2 tests `app`, which imports `core`, so `core`'s code is in its scope:

```yaml
--8<-- "examples/uv-monorepo/aval.shard-2.yaml"
```

A change under `packages/app` reruns shard 2 only. A change to `core` or to
`uv.lock` reruns both. If shared code lives outside a directory you want for
a shard, bind it with `scope.contracts` instead of widening the globs:
[Depend on another part of the repo](depend-on-a-contract.md).

## Pattern B: one contract, shard variables

One contract for every shard, with the shard index and count as declared
environment variables:

```yaml
--8<-- "internal/fingerprint/testdata/shards/pattern-b.yaml"
```

Each shard gets its own fingerprint, but over the whole suite's scope, so a
change anywhere reruns every shard. Simpler to set up, less reuse.

!!! warning "Declare both variables"
    Leave `SHARD_INDEX` or `SHARD_TOTAL` out of `environment.env` and shards
    share a fingerprint: shard 2 reuses shard 1's result without running its
    own tests, a false hit.

## The matrix

One job per shard. Shards that share a checkout write different `--result`
files, and each declares its own coverage file under `outputs`, restored
on reuse:

```yaml
--8<-- "examples/uv-monorepo/workflow.yaml"
```

The same job locally, for shard 2:

```sh
aval run -f aval.shard-2.yaml --result .aval/shard-2.json --registry "$REPO" --policy "$POLICY"
```

To publish, add the push step from [Use GitHub Actions](github-actions.md)
to each job. On pull requests, read the policy from a protected branch:
[Take the policy from a protected branch](../trust-model.md#take-the-policy-from-a-protected-branch).

??? info "Not built: several contracts in one call, templated contracts"
    `aval run` takes one contract and reports one result. Your CI already runs
    the matrix jobs and aggregates their status. A contract with
    placeholders would make the scope computed instead of declared.

## Read next

- [Write a contract](write-a-contract.md)
- [Depend on another part of the repo](depend-on-a-contract.md)
- [Use GitHub Actions](github-actions.md)
