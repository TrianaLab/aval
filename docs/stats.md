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

To keep statistics for your own repositories, see
[Measure reuse](guides/measure-reuse.md). To add a TrianaLab repository to
these numbers, see
[CONTRIBUTING.md](https://github.com/TrianaLab/aval/blob/main/CONTRIBUTING.md#join-the-public-stats).
