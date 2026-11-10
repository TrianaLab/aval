---
"aval": minor
---

New optional contract field `scope.root` (#200): the path from the
contract's directory up to the root of its tree, `..` segments only (`..`,
`../..`). A contract such as `aval/unit.yaml` with `root: ..` behaves as if
its file sat in the root: globs, `outputs`, `allowMutable[].file` and `files`, manifest
and finding paths, and `execution.outputs.files[].path` are relative to the
root; `runner.command` and `environment.probe` run there; the
`--result-file`, `--report-file` and `--stats-file` skip set is computed
against it. `scope.contracts` paths stay relative to the contract file. The
root must lie inside the git working tree that holds the contract, otherwise
`aval run`, `explain`, `verify` and `push` exit 2. `aval explain` prints the resolved
root. Globs still cannot contain `..`.

Format changes: additive change to the contract schema
(`aval.trianalab.io/v1` stays). Contracts without `scope.root` keep their
fingerprint byte for byte; setting or changing it changes the `contract`
manifest line and so the fingerprint. The manifest version
(`aval-fingerprint/v2`) does not change. Older `aval` binaries reject a
contract that uses the field.
