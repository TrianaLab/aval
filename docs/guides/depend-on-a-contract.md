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

## Workspaces

uv, pnpm and cargo workspaces keep one lockfile at the root. A member's
contract cannot list it: globs cannot use `..`. Two layouts work.

**Contracts at the root**, one per member, named `aval.<member>.yaml`:

```yaml title="aval.api.yaml"
--8<-- "tests/acceptance/testdata/workspace/aval.api.yaml"
```

**A root contract owns the lockfile**, and each member names it in
`scope.contracts`, which may use `..`:

```yaml title="aval.lock.yaml"
--8<-- "tests/acceptance/testdata/workspace/aval.lock.yaml"
```

```yaml title="packages/api/aval.yaml"
--8<-- "tests/acceptance/testdata/workspace/packages/api/aval.yaml"
```

Either way, any lockfile change reruns every member. The same layouts fit
the other tools:

| Tool | Root files to hash | Lockfile check |
|---|---|---|
| uv | `pyproject.toml`, `uv.lock` | `uv lock --check` |
| pnpm | `package.json`, `pnpm-workspace.yaml`, `pnpm-lock.yaml` | `pnpm install --frozen-lockfile` |
| cargo | `Cargo.toml`, `Cargo.lock` | `cargo metadata --locked` |

A member that imports another member lists that member's sources too, or
names its contract.

??? info "Inputs the workspace graph does not show"
    Check each one against the test, and list the ones it reads:

    - shared config at the root: `tsconfig.base.json`, `.npmrc`,
      `conftest.py`, `pytest.ini`, `.cargo/config.toml`, `rust-toolchain.toml`,
      `.python-version`, `.nvmrc`;
    - patches: pnpm `patches/`, cargo `[patch]` sources;
    - build scripts and generated code: `build.rs`, codegen inputs;
    - shared fixtures or test data outside the member;
    - environment variables (`environment.env`).

There is no `aval init`: generating scope from manifests would infer it,
and choosing the scope is the hard part, not typing the YAML.
[Find undeclared inputs](write-a-contract.md#find-undeclared-inputs) checks
what you chose.

## Read next

- [Write a contract](write-a-contract.md)
- [The test contract](../contract.md): every field and rule
