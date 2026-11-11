# Use GitHub Actions

Goal: each CI job runs its test through Aval in three lines of YAML, and one
job on `main` signs and publishes every result.

```mermaid
flowchart LR
  job([unit job]) --> act[aval run action]
  act -- verified --> skip([reuse]):::ok
  act -- anything else --> test[run the test]
  test -- "push to main" --> art([artifact aval-unit]) --> pub[publish action: aval push] --> reg([registry]):::muted
  act -- "exit 5" --> fail([step fails, SECURITY]):::err
```

## The example

A suite job:

```yaml
--8<-- "docs/includes/github-actions.yml:run"
```

The action [`integrations/github/run`](https://github.com/TrianaLab/aval/blob/main/integrations/github/run/action.yml) takes `test` (required), `file` (default `aval.yaml`), `policy` (default `.github/aval/policy.yaml`), `shadow` (`true` adds `--shadow`), `lookup-only` (see [Skip setup on a hit](#skip-setup-on-a-hit)), `min-trust-level` (default `auto`), `registry-username` and `registry-password` (default empty: the registry is read anonymously; see [Private registry](#private-registry)). In order, it:

1. Refuses `pull_request_target` and `workflow_run`, and fails when the job has `id-token: write`: a suite job can never sign.
2. Installs the aval release pinned in its `action.yml`, checked against the per-platform sha256, the release signature and the provenance ([Install](../install.md)). Pinning the action by commit SHA pins aval too.
3. Fetches the policy from the default branch through the contents API with `github.token`, on every event, never from the checkout. The registry comes from the policy's `registry` field ([Write a trust policy](trust-policy.md)); the job's `AVAL_REGISTRY` and `AVAL_POLICY` are ignored; `AVAL_MIN_TRUST_LEVEL` is not, since it only raises the policy's minimum. With no policy nothing is reused, the test runs and a warning is logged. `GH_TOKEN` is unset after the fetch, so `github.token` is not in the test's environment.
4. Runs `aval run -f <file> --test <test>` with the CI run URL, the repository and, on a push to the default branch, `--outputs-dir`. It passes `--min-trust-level` as the highest of the `min-trust-level` input, the job's `AVAL_MIN_TRUST_LEVEL` and the policy's `minTrustLevel`, never lower than the policy's. Under `auto` that is `runner` on the default branch (any event), so `main` never goes green on a developer result, which `aval push` would not publish: it is a miss and the test runs. Pull requests and other branches keep the policy's minimum. `min-trust-level: developer` opts out; a value other than `auto`, `developer`, `runner` or `isolated` exits 2 before the test runs. The log and step summary show the effective minimum.
5. Writes the outcome to the step summary and the `outcome` output. Exit 5 sets the outcome to `verification-failed`, becomes an `aval SECURITY` error annotation and fails the step; a miss is routine.
6. On a push to the default branch only, uploads `.aval/<test>/` as the artifact `aval-<test>`. A re-run of the job replaces the earlier attempt's `aval-<test>`, so publish signs the result of the attempt that just ran. Run each test in only one job per workflow run: two jobs uploading the same name race.
7. Warns when the policy trusts a `refs/pull/` subject at or above `minTrustLevel`.

The policy is a separate file on the default branch, so `publicKey` paths in it are not fetched; use keyless identities. The action needs `/usr/bin/jq` (GitHub's Ubuntu images, macOS 15 and later). Neither action writes `GITHUB_PATH` or `GITHUB_ENV`: the cosign that checks the aval release is pinned by sha256 and installed in `$RUNNER_TEMP`, off the job's `PATH`, so the test and later steps see the job's own tools.

!!! note "Pinned release"
    Pinning an action by commit SHA pins its aval release too, moved by a PR
    after each release. Until a pin with `--outputs-dir` (after 0.8.1,
    milestone 14), a test with `outputs` fails with exit 1; others work.

## Skip setup on a hit

Setup steps (toolchains, caches, browsers) run before the test, also when its result is reused. A lookup-only step lets them be skipped:

```yaml
--8<-- "docs/includes/github-actions.yml:lookup"
```

With `lookup-only: "true"` the action does steps 1 to 3 and 7 (same default-branch policy, same trust minimum), then runs `aval verify -f <file> --test <test> -o json` instead of `aval run`: it never runs the test, uploads or writes `.aval/<test>/`. The `outcome` output is `verified`, `miss`, `verification-failed` or `error`. A miss (no policy, no attestation, a signer below the minimum) succeeds the step; exit 5 fails it with the `aval SECURITY` annotation; exits 1 and 2 fail it. A value other than `true` or `false`, or `shadow: true` beside it, exits 2. The final run step stays: it verifies again, restores `outputs` and gives the job its exit code. The lookup only skips setup, it never decides reuse: if it says `verified` and the run step then misses (a registry error, a result no longer trusted), the test runs without its setup and fails. The job goes red, never green by mistake.

## Private registry

A private attestations package (GHCR creates them private) cannot be read anonymously: every lookup is outcome `error` (not `miss`, never `verification-failed`) and the test always runs. Pass `registry-username: ${{ github.actor }}` and `registry-password: ${{ github.token }}` to the run action, and set the suite job's `permissions` to `packages: read` (with `contents: read`): `github.token` has the job's permissions, so that is what makes it read-only. The action then writes the credentials for the host of the default-branch policy's `registry` only, in a directory of the step under `$RUNNER_TEMP` that starts from the job's `auths` (not its `credsStore` or `credHelpers`) and is deleted when the step ends. aval reads it through `AVAL_DOCKER_CONFIG`, which the test never gets: the test keeps the job's `DOCKER_CONFIG`. No policy, no valid `registry`, or a pinned aval without `AVAL_DOCKER_CONFIG` (0.8.2 and older, milestone 15) writes none and warns.

!!! warning "Same job, same user"
    Neither the token nor `AVAL_DOCKER_CONFIG` is in the test's environment, but the test runs in the same job as the same user, so a pull request's code can still reach the job's files and processes. Keep the token read-only (`packages: read`, never `packages: write`), never pass a write credential, and never give the suite job `id-token: write` (the action refuses it).

## Split credentials

One job signs what the suite jobs uploaded, with the workflow's identity:

```yaml
--8<-- "docs/includes/github-actions.yml:publish"
```

The action [`integrations/github/publish`](https://github.com/TrianaLab/aval/blob/main/integrations/github/publish/action.yml)
takes `file`, `policy`, `registry-username`, `registry-password` (default
`github.actor` and `github.token`), `stats` and `allow-unprotected` (both
default `false`). In order, it:

1. Refuses a ref other than the default branch unless `allow-unprotected:
   true`, always refuses `pull_request_target` and `workflow_run`, and fails
   without `id-token: write`.
2. Downloads this run's `aval-*` artifacts (so name no other artifact
   `aval-*`) into `$RUNNER_TEMP`. With none, for example when path filters
   skipped every test job, it prints a notice and exits 0: no install, no
   policy fetch, no OIDC token, so a publish job on `!cancelled()` costs
   seconds and passes before the policy is merged.
3. Installs the same pinned, verified aval release as the run action.
4. Fetches the policy from the default branch and logs in to the host of its
   `registry`, where results go. No policy or no `registry` fails the step.
5. For each `aval-<name>`, fetches a fresh OIDC token (audience `sigstore`)
   and runs `aval push --test <name>`. It tries them all, then exits with the
   worst code; 5 is `aval SECURITY`, never downgraded.
6. With `stats: true`, publishes each pushed test's run record; a failure
   is a warning.

What `aval push` does with each artifact:

- An artifact `aval-a` that holds test `b`, or a result that does not match the fingerprint push
  computes from the checkout (`environment.env` from the artifact's [`env.txt`](../artifact-format.md#env-file),
  as hashed in the suite job), exits 1 and publishes nothing; the other artifacts are still pushed.
- A reused result (report outcome `verified`, also under `--shadow`) and a
  `non-deterministic` test print `nothing to push: ...` and exit 0.
- A `miss` or an `error` publishes the result, failures included: on a tie
  in trust level a failure outranks a later flaky pass.
- A report that records a security event signs nothing and exits 5.
- The artifact holds `result.json`, `report.json`, `record.json`, `env.txt` and
  `outputs/<path>`, the files the `outputs` globs matched; push publishes
  those, never the checkout's. No `outputs/` for a test with outputs, or a
  symlink, extra file, input, `.git` or `.aval` in it, exits 1, publishing nothing.
- Trust only this workflow on `main`: [Write a trust policy](trust-policy.md#the-example).

### Why a composite action

The publish action is a composite action, not a reusable workflow: its steps
run in the caller's job, so the Fulcio certificate names the calling workflow
(`https://github.com/acme/app/.github/workflows/ci.yaml@refs/heads/main`). A
reusable workflow would put Aval's workflow in every caller's certificate, and
one `subjectRegexp` would trust them all. Aval's CI checks this.

## Pull requests

A pull request runs the same suite job and reuses what `main` published. Its
own policy edits change nothing, because the policy comes from the default
branch. The workflow file itself still comes from the PR, so a hostile PR can
make its own check green; required code-owner review is what protects `main`
([protected branch](../trust-model.md#take-the-policy-from-a-protected-branch)).

## Read next

- [Sign results](signing.md)
- [Shards and matrices](shards-and-matrices.md)
- [Measure reuse](measure-reuse.md)
