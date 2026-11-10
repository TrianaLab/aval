# AGENTS.md

Instructions for any coding agent (and human) working on Aval. This file is the
single source of the project's rules. Skills in `.agents/skills/` and the docs
point here; do not copy these rules elsewhere.

## What Aval is

Verifiable test execution reuse: test results as signed in-toto attestations,
bound to a deterministic fingerprint of declared inputs, published to an OCI
registry, and reused elsewhere only after signature and policy verification.
Read `docs/vision.md` and `docs/trust-model.md` before any non-trivial change.

## Invariants

A change that breaks one of these is wrong, even if tests pass.

1. **No own infrastructure.** No server, database or registry of our own. ORAS,
   Cosign/Sigstore, in-toto/DSSE and OCI 1.1 referrers only. Lookup by tag
   `sha256-<fingerprint>`, consumption always by digest.
2. **Scope is declared in the contract.** `aval.yaml` owns the scope. No
   inference, no runtime tracing (future work: "observed inputs").
3. **Fail closed.** If input equivalence or trust cannot be proven, the test
   runs. Never default to reuse.
4. **Miss is not verification failure.** `evidence.ErrNotFound` / exit 4 /
   outcome `miss` is routine. `evidence.ErrVerification` / exit 5 / outcome
   `verification-failed` is a security event and is never swallowed, downgraded
   or retried into silence.
5. **Trust is policy.** Signer identities and trust levels (developer, runner,
   isolated) are decided by policy, never by claims inside the predicate.
6. **Test identity is not execution fingerprint.** `metadata.name` says what is
   checked; the fingerprint says with which inputs.
7. **Cacheability classes.** `non-deterministic` is never reusable, not even by
   policy opt-in.
8. **Agnostic.** No assumption about language, test framework, CI vendor,
   registry vendor or OIDC issuer in core code. Vendor specifics live behind
   small interfaces and are covered per vendor in tests.
9. **No consumer coupling.** Aval never imports Pacto (or any consumer) and
   never models consumer concepts (readiness, gates, delivery, EvidenceSet).
   Third-party metadata goes in `annotations`.
10. **Versioned formats change additively.** Predicate type, media types, the
    `verify` report, the contract `apiVersion` and the fingerprint manifest
    version (`aval-fingerprint/vN`) only get additive changes within a version.
    Anything else is a new version (see the `aval-format-change` skill).
11. **`pkg/evidence` is public API.** It must not import `internal/` and must
    stay dependency-light.

## Layout

- `cmd/aval` CLI (cobra). Exit codes are constants in `main.go` and are API.
- `pkg/evidence` public types, `Parse`, `Verify`, verify report.
- `internal/contract` `aval.yaml` parsing and validation, glob matching.
- `internal/fingerprint` canonical manifest and hash.
- `internal/attest`, `internal/registry`, `internal/policy` signing, ORAS,
  trust policy.
- `integrations/github` GitHub composite actions on top of the CLI. Core
  (`cmd/`, `internal/`, `pkg/`) never references them.
- `tests/architecture` invariant gates about the repository itself.
- `docs/` design and specs. `examples/go-compose` is its own Go module.

## Commands

`make ci` is the gate. Run it before every push; CI runs the same legs.

| Target | What it runs |
|---|---|
| `make ci-static` | gofmt check, `go vet`, golangci-lint, shellcheck, `make lint-actions` |
| `make lint-actions` | actionlint on the workflows and zizmor (offline) on `integrations/github`; needs `pip install --require-hashes -r .github/requirements-lint.txt` |
| `make ci-gates` | level 3: architecture/invariant tests (`make test-architecture`) |
| `make ci-engine` | level 1: unit tests with `-race` and the 100% coverage gate (`make coverage-check`), the example modules (`examples/go-compose`, `examples/evidence-consumer`) |
| `make ci-false-hit` | false-hit suite: `internal/fingerprint` and `internal/contract` tests, uncached (corpus, manifest golden per `aval-fingerprint/vN`, append-only goldens against `AVAL_GOLDEN_BASE` or `origin/main`, glob tables) |
| `make ci-e2e` | levels 2 and 4 (`make test-integration test-acceptance`) through `bin/aval run -f aval.e2e.yaml`; reuses a verified result only when `AVAL_REGISTRY` and `AVAL_POLICY` are set. CI runs the same `aval run` with the pinned, verified release instead (`.github/aval/install.sh`), not `bin/aval` built from the PR |
| `make ci-release` | level 5: release dry-run (GoReleaser snapshot of `.goreleaser.yaml`, needs syft) checked by `tests/release` (`make test-release`) |
| `make ci-fuzz` | every `Fuzz*` target for `FUZZTIME` (default 10s) each (`make fuzz`); crashers land in `testdata/fuzz/` |
| `make ci-security` | govulncheck (pinned version) on both modules against the go.mod toolchain (`make vuln`); needs network |
| `make ci-docs` | markdownlint, vale, CLI reference drift (`make docs-lint`; regenerate with `make docs-cli`), strict MkDocs build (`make docs-site`), headless Chrome smoke test of the "Try it" terminal (`make docs-demo-smoke`), mobile drawer smoke (`make docs-nav-smoke`), docs deploy logic (`make docs-deploy-test`) |
| `make docs-nav-smoke` | opens the hamburger menu of the built `site/` at 390x844 on the home page and an inner page, light and dark, and checks the nav panel is on screen with links; needs Chrome (`CHROME` overrides), `SHOTS=<dir>` keeps screenshots |
| `make docs-deploy-test` | `scripts/deploy-docs.sh` and `scripts/publish-stats.sh` against a scratch copy with a local origin: default version, `latest` alias, root installers, the "Try it" WebAssembly, `stats.json`, the outdated banner hidden on `latest` and `main` and shown on an older release (headless Chrome); needs mike, jq and Chrome |
| `make docs-site` | the MkDocs site into `site/` with `--strict`, after `make docs-wasm`; needs `pip install --require-hashes -r docs/requirements.txt` |
| `make docs-wasm` | the home page's "Try it" terminal (`cmd/aval-wasm`) into `docs/assets/demo/aval.wasm`; fails over 3 MB gzipped or when the vendored `wasm_exec.js` is not the toolchain's |
| `make docs-demo-smoke` | `scripts/demo-smoke.mjs`: headless Chrome against `site/` types `aval run` twice (verified), then `tamper` and `aval verify` (exit 5); needs Node and Chrome |
| `make coverage` | unit tests plus an HTML report in `coverage.html` |
| `make build` | `bin/aval` |
| `make docs-demos` | re-records the quickstart GIFs from `docs/assets/tapes/*.tape` with `bin/aval` and a throwaway registry (needs vhs, Docker) |
| `make brand` | renders the brand PNGs in `docs/assets/images/` from their SVGs (pinned resvg-js CLI via npx) |

Epic 0 grows `make ci` to Pacto's standard (coverage gate, integration,
local acceptance, security, docs lint, fuzzing). When a target lands, add it to
this table.

## Tests

Every test belongs to exactly one level, chosen by what it proves:

| Level | Proves | Lives in |
|---|---|---|
| 1 Unit | one package in isolation | beside the code |
| 2 Integration | real components wired together (e.g. in-process registry), nothing over a reachable network | `tests/integration/` |
| 3 Architecture | rules about the repository itself | `tests/architecture/` |
| 4 Local acceptance | a whole user story through the CLI, no cluster | `tests/acceptance/` |
| 5 Release | the release produces what it claims | `tests/release/` |

Prefer level 1. Every change to fingerprinting, contract parsing, verification
or policy comes with false-hit cases (see `aval-false-hit`). Semantic
assertions go in Go, not in shell. Target: 100% statement coverage outside
`tests/`, `examples/` and `cmd/aval/main()`.

## Work tracking

- Repo `TrianaLab/aval`, project https://github.com/orgs/TrianaLab/projects/4
  (fields: Status, Priority P0-P2, Phase). Initiative is issue #1; epics are
  sub-issues of it, stories are sub-issues of epics. Epic 6 stories live in
  `TrianaLab/pacto`.
- `TODO(milestone-N)` marks deferred work and must name the roadmap milestone.
- GitHub writes use the `edu-diaz` account (`gh auth switch -u edu-diaz`), then
  switch back to the previously active account. Unattended runs
  (`aval-autopilot`) never switch: they prefix each write with
  `GH_TOKEN=$(gh auth token -u edu-diaz)`.

## Style

- Code, docs, commits and issues in English.
- Conventional commits (`feat:`, `fix:`, `docs:`, `test:`, `ci:`, `chore:`).
- Prose is concise and concrete: no marketing words, no filler. Docs must not
  claim behaviour the code does not have; mark future work with its milestone.
- Media types, predicate type, verify report version and `apiVersion` are
  frozen under the `trianalab.io` namespace (`docs/artifact-format.md`
  "Versioning and compatibility"). They change only through the
  `aval-format-change` skill.

## Skills

Portable skills (Agent Skills format) live in `.agents/skills/`. Each has a
symlink in `.claude/skills/` so Claude Code discovers it; `make ci-gates`
fails if a link is missing.

| Skill | Use it to |
|---|---|
| `aval-implement` | implement a roadmap issue end to end |
| `aval-check` | run the validation ladder (quick, registry, e2e, dogfood) |
| `aval-audit` | review a diff against these invariants before pushing |
| `aval-review` | engineering and security review (SOLID, DRY, KISS, YAGNI, secure by default) |
| `aval-false-hit` | add adversarial cases for fingerprint and trust changes |
| `aval-format-change` | change any versioned format safely |
| `aval-dogfood-report` | measure reuse in Aval's own CI (hits, misses, false hits) |
| `aval-autopilot` | implement Epics 0-5 unattended, one fresh subagent per step |
