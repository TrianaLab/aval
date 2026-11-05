---
name: aval-review
description: Read-only engineering review of an Aval change for code quality and security, covering SOLID, DRY, KISS and YAGNI, Go idioms, error handling, testability and a security-first pass over inputs, paths, crypto, supply chain and CI. Complements aval-audit, which checks project invariants. Use when the user says "review this implementation", "code review", "check best practices", "security review of this PR", or as the quality half of the review step in aval-autopilot.
license: Apache-2.0
metadata:
  project: aval
compatibility: Needs git, Go and make. gh is optional, for reviewing a PR by URL.
---

# aval-review

Review the change the user names (a PR URL or number, a branch, or by default
`git diff $(git merge-base origin/main HEAD)` plus uncommitted files) as a
senior Go engineer who would be paged at 3am for it. Read-only: report, do not
edit. Project invariants are `aval-audit`'s job; do not repeat them here.
Escalate anything that looks like one to `aval-audit`.

Read every changed file in full, not just the hunks, plus the callers of any
changed exported function (`grep -rn "<Func>(" --include=*.go`).

## 1. Security first

Aval's product is trust, so security findings are blockers by default.

- **Untrusted input:** `aval.yaml`, policy files, registry responses, DSSE
  envelopes, predicates, env vars, file names and contents in scope. Each is
  validated at the boundary, with size limits where an attacker controls the
  size (decoders over `io.LimitReader`, bounded globbing and recursion).
- **Paths:** no traversal out of the contract root (`filepath.Rel` plus a
  `..` check after `Clean`), no following symlinks unless intended, no
  TOCTOU between check and use where it matters.
- **Crypto and verification:** never roll your own. Use sigstore-go, the
  stdlib and constant-time comparisons for secrets. Verification happens
  before any use of the payload. Digests are compared with algorithm prefix,
  and tags are resolved to digests once and then pinned.
- **Errors fail closed:** an error path never ends in "reuse". No `_ =` on
  errors from verification, I/O or decoding. Wrapped errors keep
  `errors.Is`-ability for `ErrNotFound` and `ErrVerification`.
- **Secrets:** no tokens or keys in logs, errors, test fixtures or reports.
  OIDC tokens are not persisted.
- **Command execution:** `runner.command` is argv, executed without a shell,
  with an explicit environment. No string concatenation into `sh -c`.
- **Supply chain:** a new dependency needs a reason a few lines of stdlib
  cannot cover. Check its maintenance, licence (Apache-2.0 compatible) and
  transitive weight. GitHub Actions are pinned by SHA, workflows use least
  `permissions:`, and untrusted PR input (titles, branch names) never reaches
  `run:` through `${{ }}`.
- **Concurrency:** shared state is guarded and goroutines terminate (context
  cancellation). Run `go test -race` on the touched packages.

## 2. Design principles

Flag violations with the cost they impose, not as dogma.

- **KISS / YAGNI:** speculative options, config for values that never change,
  abstractions for a single implementation, "for later" scaffolding. The
  shortest correct code wins. Point at what to delete.
- **DRY:** duplicated logic (not merely similar-looking code) that must change
  together. Prefer reusing an existing helper in the repo. Do not demand
  abstraction for two call sites that may diverge.
- **SOLID, the Go way:**
  - Single responsibility: packages and functions do one thing. `cmd/aval`
    wires; logic lives in `internal/` or `pkg/`.
  - Open/closed: vendor specifics (registries, signers, OIDC issuers) are
    behind small interfaces where a second implementation is real or
    planned in the roadmap, not before.
  - Liskov: implementations honour the interface contract, including error
    semantics.
  - Interface segregation: small interfaces, defined where they are
    consumed.
  - Dependency inversion: core logic receives clocks, env lookups,
    filesystems and clients as parameters so tests do not need globals.
- **API surface:** `pkg/evidence` exports only what consumers need. New
  exported identifiers have doc comments. Unexported is the default.

## 3. Go craft

- Errors: wrap with context (`fmt.Errorf("...: %w", err)`), no panics on
  input, no log-and-return-the-same-error.
- `context.Context` first, on everything that does I/O. No `context.TODO()`
  outside tests.
- Resources are closed (`defer` right after a successful open), and close
  errors are handled where data is written.
- No global mutable state. No `init()` side effects.
- Naming follows Go conventions. Functions stay small (cyclomatic complexity
  at most 15, enforced by `make ci` once Epic 0 lands).
- Comments explain why, not what. No commented-out code. TODOs follow
  `TODO(milestone-N)`.

## 4. Tests

- Tests prove behaviour, not lines. Each acceptance criterion and each error
  branch has a test that would fail if the code broke.
- Table-driven where cases vary by data. No sleeps (use clocks and
  channels). No network except the in-process registry at level 2.
- Placed at the right level (see AGENTS.md). Fuzz targets for new parsers.
- Coverage holds the gate. A test that only executes code without asserting
  anything is a finding.

## 5. Run

Run what exists and report the output, never a claim:

```
make ci
go test -race -count=1 <touched packages>
govulncheck ./...      # if installed or once Epic 0 adds it
```

## Output

```
VERDICT: pass | fix
BLOCKERS:
- file:line — problem — principle or risk — exact edit
WARNINGS:
- ...
NITS:
- ...
CHECKS: <command> → <result>, ...
```

Security issues, failing checks and untested acceptance criteria are blockers.
Principle violations are warnings unless they make the code wrong or unsafe.
Nits never block. If there is nothing to report in a section, write `none`.
