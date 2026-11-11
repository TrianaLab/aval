---
"aval": minor
---

The GitHub run action reads a private attestations registry: new optional
inputs `registry-username` and `registry-password` (default empty, anonymous
as before) write credentials for the host of the default-branch policy's
`registry` only, in a directory of the step that is deleted when it ends.
aval reads it through the new `AVAL_DOCKER_CONFIG`, a directory whose
`config.json` replaces `$DOCKER_CONFIG`'s for Aval's registry client and that
`runner.command` never gets; the test keeps the job's `DOCKER_CONFIG`. The test
no longer sees the job's `GH_TOKEN`.
