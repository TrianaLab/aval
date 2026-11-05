---
name: aval-implement
description: Implements an Aval roadmap issue end to end, from reading the issue and its acceptance criteria through failing tests, code, docs, validation, audit and a conventional commit, keeping the GitHub project board in sync. Use when the user says "implement #N", "work on issue N", "pick up the next story", "do issue N of epic X", or otherwise asks to deliver a TrianaLab/aval roadmap issue.
license: Apache-2.0
metadata:
  project: aval
compatibility: Needs git, Go, make and an authenticated gh CLI with access to TrianaLab/aval and project 4.
---

# aval-implement

Deliver one roadmap issue. The issue's acceptance criteria are the definition
of done. Rules live in `AGENTS.md`; this skill is only the process. Read
`AGENTS.md` before starting if it is not already in context.

## 1. Resolve the issue

Take the issue number the user gave.

1. Run `gh issue view <N> --repo TrianaLab/aval --comments`.
2. Find its parent epic (sub-issue parent, or the epic linked in the body) and
   read it with `gh issue view`. Skim initiative #1 for context.
3. Extract the acceptance criteria as a checklist. If there are none, or they
   are ambiguous, ask the user before writing code.
4. **Epic 6 stop.** If the issue belongs to Epic 6 or lives in
   `TrianaLab/pacto`, stop: say that Epic 6 work is implemented in the Pacto
   repo and this skill does not implement it (invariant 9 forbids Aval
   modelling consumer concepts).
5. Read every doc the issue or epic links, plus whatever of `docs/vision.md`,
   `docs/trust-model.md`, `docs/contract.md`, `docs/artifact-format.md` the
   change touches. Check the roadmap milestone the issue belongs to.

## 2. Mark it In Progress

GitHub writes use `edu-diaz` (see "Work tracking" in `AGENTS.md`).

1. Record the active account: `gh api user --jq .login`.
2. `gh auth switch -u edu-diaz`.
3. Find the IDs: item via `gh project item-list 4 --owner TrianaLab
   --format json --limit 500`, project node ID via `gh project view 4
   --owner TrianaLab --format json --jq .id`, Status field and option IDs
   via `gh project field-list 4 --owner TrianaLab --format json` (the
   `options` array). Set Status to In Progress with `gh project item-edit`. If the issue
   is not on the board, add it with `gh project item-add` first.
4. Switch back to the recorded account, even if a step failed.

## 3. Branch and plan

- Branch from an up-to-date `main`: `feat/<N>-<slug>` (slug: 2-5 kebab-case
  words from the title). Use `fix/` for a bug issue.
- Plan the smallest change that meets every acceptance criterion. List the
  files to touch and which invariants (`AGENTS.md`, "Invariants") are in play.
  Anything outside the criteria is out of scope; note it as a follow-up.
- If the plan needs a new dependency, a new versioned format, or breaks an
  invariant, stop and ask the user.

## 4. Tests first

- Pick the test level from the `AGENTS.md` "Tests" table by what the test
  proves. Prefer level 1.
- If the right level's directory (`tests/integration/`, `tests/acceptance/`)
  does not exist yet, say so; write the test at the closest existing level
  only if it still proves the criterion, otherwise ask the user.
- Write the tests, run them, and confirm they fail for the expected reason.

## 5. Implement

- Make the tests pass with the minimum code. Keep vendor specifics behind the
  existing interfaces (invariant 8); keep `pkg/evidence` free of `internal/`
  (invariant 11). Exit codes in `cmd/aval/main.go` are API.
- Error paths: miss and verification failure stay distinct (invariant 4);
  anything unprovable fails closed (invariant 3).
- Deferred work gets `TODO(milestone-N): ...` naming the roadmap milestone.

## 6. Docs and TODOs

- Update every doc that describes the changed behaviour, including the
  roadmap status in `docs/vision.md` if a milestone exit criterion is now met.
  Docs must not claim behaviour the code lacks.
- Search for the `TODO(milestone-N)` markers this change resolves (for example
  `grep -rn "TODO(milestone-" .`) and remove those, plus any lint exclusions
  they justified (e.g. in `.golangci.yml`).
- If you added a `make` target, add it to the `AGENTS.md` "Commands" table.

## 7. Specialised skills

- Change to fingerprinting, contract parsing, verification or policy: run
  `aval-false-hit` and add its cases.
- Change to any versioned format (invariant 10), a verify report `Outcome`,
  or an `Exit*` constant in `cmd/aval/main.go`: run `aval-format-change`.

## 8. Validate and audit

- Run `aval-check` at the level the change needs (quick for local-only code;
  registry, e2e or dogfood once milestones 2-3 have landed). At minimum,
  `make ci` must pass.
- Targets that Epic 0 or milestone 2 will add (coverage gate, integration,
  acceptance, fuzzing, registry, signing): if the target exists, run it;
  otherwise note it as not yet available in the summary. Never claim a check
  ran when it did not.
- Run `aval-audit` on the diff and fix every blocker.
- Walk the acceptance checklist and mark each item met or not met, with the
  test or command that proves it.

## 9. Commit

- Conventional commit (`feat:`, `fix:`, `test:`, `docs:` ...), English,
  concise, referencing the issue.
- Add `Closes #<N>` only if every acceptance criterion is met; otherwise
  `Refs #<N>` and list what remains.
- Do not push or open a PR without asking the user. When they agree, push and
  open the PR with the `edu-diaz` account, then switch back.

## 10. After merge

When the user confirms the PR is merged, as `edu-diaz` (then switch back):

1. Set the project item Status to Done.
2. Tick the story in the parent epic's checklist (`gh issue edit <epic>
   --body-file ...` after editing the body). If that completes the epic, tell
   the user rather than closing the epic yourself.

## Report

End with: issue and branch, acceptance criteria met or not, checks run and
checks not yet available, commit SHA, and the next action the user must take
(push, PR, or post-merge update).
