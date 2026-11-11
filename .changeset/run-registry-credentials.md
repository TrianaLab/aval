---
"aval": minor
---

The GitHub run action reads a private attestations registry: new optional
inputs `registry-username` and `registry-password` (default empty, anonymous
as before) write credentials for the host of the default-branch policy's
`registry` only, in a `DOCKER_CONFIG` of the step that is deleted when it
ends. The test no longer sees the job's `GH_TOKEN`.
