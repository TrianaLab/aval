---
name: aval-dogfood-report
description: Measures how much Aval reuses test results in its own CI (TrianaLab/aval GitHub Actions) and writes a markdown report with hit rate, misses by cause, verification failures, false hits and minutes that would have been saved, then compares the numbers against the risks in docs/vision.md and ends with a recommendation. Use when the user asks for a "dogfood report", "how is Aval doing in its own CI", "hit rate", "reuse metrics", "shadow mode results", "did we get any false hits", or wants an update for the dogfooding issue.
license: Apache-2.0
metadata:
  project: aval
compatibility: Needs the gh CLI authenticated with read access to TrianaLab/aval.
---

# aval-dogfood-report

Read-only measurement. The report is the deliverable; nothing is changed in
the repo. Rules and exit codes live in `AGENTS.md` and `cmd/aval/main.go`;
the report schema lives in `docs/artifact-format.md` ("Verify report").

## 1. Inputs

- **Period**: take it from the user's request (e.g. "last 2 weeks", a date
  range, "since the last report"). Default to the last 14 days and say so.
- **Branch filter**: all runs by default; if the user names a branch (often
  `main`), restrict to it.

## 2. Check what exists before measuring

Do not invent data. Determine which sources are real today:

1. List workflows and jobs: `gh workflow list -R TrianaLab/aval` and read
   `.github/workflows/`. Look for a dogfood job (Epic 3 dogfooding issue)
   and for shadow mode (milestone 2), i.e. a job that runs
   `aval verify --output json` and then still runs the real suite.
2. Find the dogfooding issue: `gh issue list -R TrianaLab/aval --search
   "dogfood in:title" --state all`. Note its number and status.
3. Classify:
   - **Full**: dogfood job emits the verify report plus the real test result
     per run. Do all of section 3.
   - **Partial**: only precursors exist (e.g. a fingerprint reproducibility
     job, `aval run --fingerprint-only` across runs). Report only those:
     whether the same commit yields the same fingerprint, how often the
     fingerprint changed between consecutive `main` runs. Skip hit/miss math.
   - **None**: say so plainly, link the dogfooding issue, list what is
     blocking it (milestone 2 registry/signing, shadow mode), and stop after
     a short report.

## 3. Collect per run (full mode)

1. `gh run list -R TrianaLab/aval --created "<from>..<to>" --limit 1000
   --json databaseId,headSha,headBranch,event,conclusion,createdAt,workflowName`
2. For each run, prefer structured output: `gh run download <id>` for the
   artifact holding the verify report and the real result. Fall back to the
   job summary or `gh run view <id> --log` only if no artifact exists, and say
   which runs used the fallback.
3. Per run record: `outcome`, `reason`, `fingerprint`, verified `result` (if
   any), real suite result, real suite duration, policy flag present or not.

Skip cancelled runs and runs where the dogfood job never started; count them
separately.

## 4. Compute

| Metric | Definition |
|---|---|
| Runs | runs where the dogfood job produced a report |
| Hit rate | `verified` / runs |
| Miss: scope change | `miss` with a policy and a lookup performed (fingerprint not published) |
| Miss: no policy | `miss` because `--policy` was absent (lookup skipped) |
| Miss: registry error | `error` outcome (exit 1, or 6 while stubbed) — fail-closed, counted as a miss |
| Verification failures | `verification-failed` / exit 5 |
| False hits | `verified` whose `result` differs from the real suite result |
| Minutes saved (would-be) | sum of real heavy-suite duration on hits |

If a cause is ambiguous from `reason`, put it under "unclassified" and quote
the reason; do not guess.

## 5. Escalations (do not wait for the report)

- **Any verification failure** is a security event (AGENTS.md invariant 4).
  Stop, tell the user immediately with run id, fingerprint, reference and
  reason. Never fold it into an average.
- **Any false hit**: for each one, propose an `aval-false-hit` case (what
  input changed that the scope did not capture) and draft a bug issue with
  run ids, both results and both fingerprints. Ask the user before filing.

## 6. Interpret

Compare against `docs/vision.md` "Risks":

- **Savings small if inputs always change**: a low hit rate dominated by
  scope-change misses means the scope is too wide or the suite touches
  everything. Show which paths changed most often if the explain manifest is
  available.
- **Scope cost vs savings**: weigh minutes saved against contract churn
  (`git log --since <from> -- aval.yaml '**/aval.yaml'`).
- **No-policy and registry-error misses** are setup issues, not signal about
  reuse; report them but keep them out of the hit-rate conclusion.

End with one recommendation: keep as is, narrow scope (name the globs), fix
setup (policy, registry), or stop dogfooding a given suite. State the
evidence behind it in one or two sentences.

## 7. Report format

```markdown
# Aval dogfood report: <from> to <to>
Mode: full | partial | none. Data sources: <artifacts / logs / jobs>.

| Runs | Hit rate | Scope-change | No policy | Registry error | Verif. failures | False hits | Minutes saved |
|---|---|---|---|---|---|---|---|

## Security events
## False hits
## Against vision risks
## Recommendation
## Gaps
<targets/jobs not yet available, runs skipped, fallbacks used>
```

## 8. Optional: post it

Offer to post the report as a comment on the dogfooding issue. Ask the user
first; post only on an explicit yes. GitHub writes follow AGENTS.md "Work
tracking" (account switch and switch back).
