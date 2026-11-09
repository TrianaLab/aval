---
"aval": minor
---

`--registry`, `--policy`, `--registry-timeout` and `-f` are now global flags, and `--registry` and `--policy` fall back to `AVAL_REGISTRY` and `AVAL_POLICY` (flag > environment > unset; an empty value counts as unset). There is no config file and no `aval.yaml` field: an architecture test keeps the registry and policy out of the contract and out of any file in the tree. When a value comes from the environment, `aval run` prints one `config:` line on stdout, and `verify`, `push`, `stats` and `record` print it on stderr. Exit codes and the verify report are unchanged.
