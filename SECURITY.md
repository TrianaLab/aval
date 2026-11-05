# Security policy

Aval decides whether a test result may be reused instead of running the test.
A bug that lets a result be reused when it should not (a false hit, a
signature or policy bypass, a fingerprint collision, a verification failure
reported as a miss) is a security issue, not just a bug.

## Supported versions

Aval is pre-1.0. Only the latest commit on `main` (and, once releases exist,
the latest release) receives fixes.

## Reporting a vulnerability

Do not open a public issue or pull request.

- Preferred: GitHub private vulnerability reporting, from the repository's
  **Security** tab, **Report a vulnerability**
  (https://github.com/TrianaLab/aval/security/advisories/new).
- Or email the maintainer at edudiazasencio@gmail.com with the subject
  `[aval security]`.

Include the affected version or commit, the steps or contract that reproduce
it, and the impact you see. You will get an acknowledgement within 7 days. A
fix is developed privately and published with a GitHub security advisory that
credits the reporter, unless you ask not to be named.

## What CI checks

- `govulncheck` on every pull request, every push to `main` and daily
  (`make ci-security`).
- `dependency-review` on every pull request.
- Dependabot for Go modules and GitHub Actions; patch updates merge
  automatically once `ci` passes, everything else waits for review.
- Every action in `.github/workflows` is pinned by commit SHA
  (`tests/architecture` enforces it).
