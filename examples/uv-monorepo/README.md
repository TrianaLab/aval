# Example: uv workspace, two shards

A monorepo with a [uv](https://docs.astral.sh/uv/) workspace: `packages/core`
and `packages/app`, which depends on `core`, share one `uv.lock`. Its pytest
suite is split into two shards, one contract each, and each shard writes its
coverage as a declared output, which a reused result restores.

| File | What it is |
|---|---|
| `aval.shard-1.yaml` | shard 1: `core`'s tests, scope `packages/core` |
| `aval.shard-2.yaml` | shard 2: `app`'s tests, scope `packages/app` and `core`'s code |
| `workflow.yaml` | a GitHub Actions matrix with one job per shard |

Both contracts sit at the workspace root because a glob cannot leave its
contract's directory and every shard reads the root `pyproject.toml` and
`uv.lock`. A change under `packages/app` reruns shard 2 only; a change to
`core`'s code or to the lock reruns both. Guide:
[Shards and matrices](../../docs/guides/shards-and-matrices.md).

`tests/acceptance/uv_monorepo_test.go` runs this story against an
in-process registry in Aval's CI.

## Run it locally

You need uv, Docker and `aval` 0.7.0 or later (`keygen`). Work
in a copy, so the virtual environment uv creates stays out of the Aval
checkout:

```sh
cp -R examples/uv-monorepo /tmp/uv-monorepo && cd /tmp/uv-monorepo
docker run -d --name aval-registry -p 5001:5000 registry:2
aval keygen
printf 'apiVersion: aval.trianalab.io/v1\nkind: Policy\nminTrustLevel: developer\nidentities:\n  - publicKey: aval.pub\n    trustLevel: developer\n' > policy.yaml
REPO=localhost:5001/demo/uv-monorepo-aval
```

Run each shard, sign and publish its result and coverage:

```sh
for n in 1 2; do
  aval run -f aval.shard-$n.yaml --result-file .aval/shard-$n.json --registry $REPO --push --key aval.key
done
```

Delete the coverage and run again: both shards reuse their results and
restore `coverage-shard-1.xml` and `coverage-shard-2.xml` without running
pytest:

```sh
rm coverage-shard-*.xml
for n in 1 2; do
  aval run -f aval.shard-$n.yaml --result-file .aval/shard-$n.json --registry $REPO --policy policy.yaml
done
```

Change a file only shard 2 reads: shard 1 is still verified, shard 2 is a
miss (exit 4):

```sh
echo "# change" >> packages/app/src/app/__init__.py
aval verify -f aval.shard-1.yaml --registry $REPO --policy policy.yaml
aval verify -f aval.shard-2.yaml --registry $REPO --policy policy.yaml
```

Clean up with `docker rm -f aval-registry`.
