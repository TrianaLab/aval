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

## Publish from GitHub Actions

The [publish action](guides/github-actions.md#split-credentials) sends the run
record of every artifact in the workflow run, verified hits and verification
failures included, to a registry of another repository's package:

```yaml
- uses: TrianaLab/aval/integrations/github/publish@<sha> # pin by commit SHA
  with:
    stats: "true"
    stats-registry: ghcr.io/trianalab/aval-stats
```

- `stats-registry` is `host[:port]/path`, with no scheme, credentials or `@`;
  anything else exits 2 before anything is pushed. Empty, records go to the
  policy's `registry`.
- The registry credentials (`github.token` by default) go to it only when it
  is on the host of the policy's `registry`.
- The job's `packages: write` reaches another repository's package only when
  that package grants the publishing repository write access (package
  settings, "Manage Actions access"). Until then each record is a warning.
- A record that cannot be published never fails the step, and a verification
  failure still exits 5.
