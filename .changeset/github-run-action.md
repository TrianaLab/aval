---
"aval": minor
---

New GitHub composite action `integrations/github/run` (#211): a CI job runs
one test through Aval with `uses: TrianaLab/aval/integrations/github/run@<sha>`
and `with: {test: <name>}` (inputs `file` and `policy` are optional). It
refuses `pull_request_target`, `workflow_run` and any job with
`id-token: write`; installs the aval release pinned in its `action.yml`,
checked against the per-platform sha256, signature and provenance; fetches
the trust policy, and the registry inside it, from the default branch
through the contents API on every event (no policy: nothing is reused, with
a warning); writes the outcome to the step summary; turns exit 5 into an
`aval SECURITY` error annotation; uploads `.aval/<test>/` as the artifact
`aval-<test>` on a push to the default branch only; and warns when the
policy trusts a `refs/pull/` subject at or above `minTrustLevel`. The
action pins 0.7.0 until the next release, which has `--test` and the
policy's `registry` (#213). `docs/guides/github-actions.md` is rewritten
around it. No change to the aval binary, its exit codes or formats.
