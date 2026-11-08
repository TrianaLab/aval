# ![Aval](docs/assets/images/wordmark.svg#gh-light-mode-only)![Aval](docs/assets/images/wordmark-dark.svg#gh-dark-mode-only)

Run a heavy test once. Reuse the result anywhere the inputs match.

Aval hashes the inputs you declare in `aval.yaml` into a fingerprint and,
with `--push`, signs the test result and stores it in your own OCI registry. A job with the same
fingerprint checks the signature against its trust policy and skips the test.
If Aval cannot prove reuse is safe, the test runs.

No server, no database: only the OCI registry you already have.

## Install

```sh
curl -fsSL https://aval.run/install.sh | bash
```

Windows: `irm https://aval.run/install.ps1 | iex`. Other options:
[Install](https://aval.run/main/install/).

## Get started

- [Quickstart](https://aval.run/main/quickstart/): a miss, a reuse, a changed
  input and a rejected result, in five minutes on your laptop.
- [Use it in GitHub Actions](https://aval.run/main/guides/github-actions/).
- [How Aval works](https://aval.run/main/concepts/) and the
  [CLI reference](https://aval.run/main/cli-reference/).

## Status

Pre-alpha. The contract and verify report are frozen at `aval.trianalab.io/v1`
and the predicate at `execution/v2`
([compatibility rules](https://aval.run/main/artifact-format/#versioning-and-compatibility));
see the [roadmap](https://aval.run/main/vision/#roadmap).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md), [SECURITY.md](SECURITY.md) and the
[Go + Compose](examples/go-compose) and [uv monorepo](examples/uv-monorepo)
examples.

## License

Apache-2.0. See [LICENSE](LICENSE).
