# Stats

How much does Aval reuse in real CI? Live numbers from every repository that
records runs to `ghcr.io/trianalab/aval-stats`, Aval's own CI included.

Live statistics are not available right now: `stats.json` is missing, malformed
or more than 6 hours old.
{ data-aval-stats="page" }

Run records are unsigned statistics. They never affect reuse.

## Where the numbers come from

Twice an hour, and on every push to `main`, the docs workflow runs:

```sh
aval stats --registry ghcr.io/trianalab/aval-stats \
  --by repository --since 30d -o json
```

and publishes the [stats report](artifact-format.md#stats-report-json) as
`stats.json` at the site root. If a refresh fails, the previous file stays.

To keep statistics for your own repositories, see
[Measure reuse](guides/measure-reuse.md). To add a TrianaLab repository to
these numbers, see
[CONTRIBUTING.md](https://github.com/TrianaLab/aval/blob/main/CONTRIBUTING.md#join-the-public-stats).
