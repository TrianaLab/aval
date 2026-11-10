---
"aval": minor
---

New GitHub composite action `integrations/github/publish` (#212): one step in
the consumer's own publish job signs and pushes every `aval-<test>` artifact
the run action uploaded in the workflow run. It refuses any ref but the
default branch unless `allow-unprotected: true`, always refuses
`pull_request_target` and `workflow_run`, and needs `id-token: write`;
installs the same pinned, verified aval release as the run action; fetches
the trust policy from the default branch and pushes to its `registry`
(credentials default to `github.actor` and `github.token`); downloads the
artifacts into `$RUNNER_TEMP`; and runs `aval push --test <name>` per
artifact with a fresh OIDC token, trying every artifact and exiting with the
worst code (5 is never downgraded). `stats: true` also publishes each pushed
test's run record, best effort. It is a composite action, not a reusable
workflow, so the certificate names the calling workflow; CI checks this on
Aval's `main`. No change to the aval binary, its exit codes or formats.
