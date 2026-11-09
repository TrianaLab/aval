# Use GitHub Actions

Goal: a workflow on `main` that reuses a verified test result, or runs the
test and publishes a signed one.

```mermaid
flowchart LR
  job([push to main]) --> run[aval run]
  run -- verified --> skip([reuse]):::ok
  run -- anything else --> test[run the test] --> push[aval push] --> reg([registry]):::muted
  test -- "failed or exit 5" --> fail([step fails,<br>nothing pushed]):::err
```

## The example

From Aval's own
[ci.yaml](https://github.com/TrianaLab/aval/blob/main/.github/workflows/ci.yaml).
Set `REPO`, `CONTRACT` and `POLICY`, and [install](../install.md#linux-and-macos) `aval` first.

Permissions:

```yaml
--8<-- ".github/workflows/ci.yaml:gha-permissions"
```

Steps: log in to GHCR, run or reuse, then sign with the workflow's identity
and push:

```yaml
--8<-- ".github/workflows/ci.yaml:gha-steps"
```

- The OIDC token is fetched after the test, so a slow test cannot outlive
  it. `runner.command` never sees it.
- Trust only this workflow on `main`: [Write a trust policy](trust-policy.md#the-example).

## Pull requests

Write scopes mean the job above runs only on `main`. A PR job can still reuse:

- `contents: read`, no push step;
- set `AVAL_REGISTRY` and `AVAL_POLICY` (or the flags);
- read the policy from the default branch, not the PR:
  [protected branch](../trust-model.md#take-the-policy-from-a-protected-branch).

## Read next

- [Sign results](signing.md)
- [Shards and matrices](shards-and-matrices.md)
- [Measure reuse](measure-reuse.md)
