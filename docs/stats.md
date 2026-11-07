# Stats

Live reuse numbers from every repository that records its runs to
TrianaLab's public stats registry, `ghcr.io/trianalab/aval-stats`. Aval's own
CI is one of them.

Live statistics are not available right now: `stats.json` is missing, malformed
or more than 6 hours old.
{ data-aval-stats="page" }

Run records are unsigned statistics. They never affect reuse.

## Where the numbers come from

Twice an hour, and on every push to `main`, the docs workflow reads the
registry anonymously:

```sh
aval stats --registry ghcr.io/trianalab/aval-stats \
  --by repository --since 30d -o json
```

It publishes the [stats report](artifact-format.md#stats-report-json), plus
`generatedAt`, as `stats.json` at the root of this site. This page and the
home page read that file in your browser. If a refresh fails, the previous
file stays.

## Keep statistics of your own

Run records go to the registry you choose. `aval run --record` writes them
next to your attestations; to keep them apart, write the record to a file and
publish it with `aval record` to a registry of your own:

```sh
aval run --record-file record.json --repository acme/orders \
  --registry ghcr.io/acme/orders-aval --policy policy.yaml
aval record --record-file record.json --registry ghcr.io/acme/aval-stats
aval stats --registry ghcr.io/acme/aval-stats --by repository
```

`--repository` names the source repository, so `--by repository` can tell
your repositories apart. [Measure reuse](guides/measure-reuse.md) has the
details.

## Join the public aggregate

A repository appears here once it publishes run records to
`ghcr.io/trianalab/aval-stats`:

1. A TrianaLab maintainer grants the repository write access to the package
   (package settings, "Manage Actions access"). GHCR offers this only to
   repositories of the TrianaLab organization; to ask, open an issue.
2. A job on `main` with `packages: write`, which runs no tests, publishes the
   records the test jobs wrote:

    ```sh
    aval record --record-file record.json --repository "$GITHUB_REPOSITORY" \
      --registry ghcr.io/trianalab/aval-stats
    ```

    Without `--repository` here or on `aval run`, the repository shows as
    `unknown`. Aval's own `dogfood-record` job in `ci.yaml` does this.
