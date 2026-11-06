---
"aval": patch
---

`pkg/evidence` is semver-stable Go API: within a major version it changes additively only, and CI fails when an exported identifier is removed or changes type or signature without a new major module path. `examples/evidence-consumer` is a separate module that consumes Aval evidence through `pkg/evidence` alone, with its own signature verifier. The policy is in `docs/integrations.md`.
