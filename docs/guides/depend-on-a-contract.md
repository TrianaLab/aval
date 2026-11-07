# Depend on another part of the repo

Goal: in a monorepo, rerun the frontend tests when the backend's inputs
change, without copying the backend's globs.

```mermaid
flowchart LR
  f[frontend/aval.yaml] -- scope.contracts --> b[backend/aval.yaml]
  b --> bs(["backend scope: api/**"])
  f --> fs(["frontend scope: src/**, test.sh"])
  bs -. change .-> miss([frontend misses and runs]):::err
```

## The example

The frontend names the backend's contract:

```yaml title="frontend/aval.yaml"
--8<-- "tests/acceptance/testdata/monorepo/frontend/aval.yaml"
```

```yaml title="backend/aval.yaml"
--8<-- "tests/acceptance/testdata/monorepo/backend/aval.yaml"
```

The frontend's fingerprint now includes the backend's fingerprint:

- a change in the backend's scope makes the frontend miss and run;
- a change outside it (a backend README) keeps the reuse;
- if the backend names another contract, that one counts too.

`aval run --explain` shows the reference:

```text
contract ../backend/aval.yaml backend sha256:<backend fingerprint>
depends on ../backend/aval.yaml (backend) sha256:<backend fingerprint>
```

## What to know

- Paths are relative to the contract and stay inside its git working tree.
  Only local files: no contract from another repository.
- If Aval cannot read or fingerprint the referenced contract, the test runs.
- A mutable reference in the backend blocks the frontend's reuse too. Only
  the backend's own `scope.allowMutable` can accept it.

Limits and errors: [`scope.contracts`](../contract.md).

## A contract, or an API file?

Name the contract when the test runs the other part's code, like frontend
end-to-end tests against the real backend.

When the test only uses the interface (a generated client, a mocked
backend), list the API file (OpenAPI, `.proto`) in `scope.dependencies`
instead. Only an interface change reruns the test. Globs cannot leave the
contract's directory, so keep the file in the frontend's tree.

## Read next

- [Write a contract](write-a-contract.md)
- [The test contract](../contract.md): every field and rule
