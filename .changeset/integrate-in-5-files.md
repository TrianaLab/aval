---
"aval": patch
---

New guide "Integrate Aval in 5 files" and the example `examples/github-monorepo`
(#217): a monorepo with a Go module, a Node UI, a Dockerfile, a data
directory and an end-to-end suite in 3 shards, put on Aval by adding
`aval.yaml` and `.github/aval/policy.yaml` and editing `ci.yml` and
`.gitignore`. Aval's tests check that the integration is exactly those files,
run every test through the CLI (miss and push, reuse per shard, a fragment
edit reruns only its tests, an unaccepted mutable reference, exit 5 on a
tampered attestation), and CI checks that the run action ignores a trust
policy edited in a pull request. No CLI or format change. Known gap: the
GitHub publish action cannot push a test with `outputs` yet, because the run
action's artifact holds `.aval/<test>/` only.
