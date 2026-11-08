---
"aval": patch
---

Docs: the guide "Shards and matrices" shows two ways to split a suite across CI matrix jobs with existing fields: one contract per shard (`aval.shard-1.yaml`, run with `-f`) or one contract with `SHARD_INDEX` and `SHARD_TOTAL` in `environment.env`. New example `examples/uv-monorepo`: a uv workspace with two packages, two shard contracts and coverage restored on reuse. No behaviour or format change.
