---
"aval": minor
---

Plain output of `aval run` v2 (CI logs and pipes). Every line CI greps today keeps its shape: `<outcome> <test> <digest>`, `signer:`, `reference:`, `reason:`, `rejected:`, `ran <test>: <result> (exit N)`, `reused <result> result`, `pushed`, `restored`, `recorded`, `config:`. `aval verify` text and `-o json` are unchanged, and so are all exit codes. Changed lines:

- Added: `--- run <test>: <command>` before and `--- end <test>: <passed|failed> (exit N) in <time>` after the test's own output, which passes through untouched.
- Added: a closing `summary: <test> <ran|reused>: <passed|failed>[ in <time>][; saved <time>][; result pushed][; reuse was off][; SECURITY]`. `saved` is the reused run's signed `finishedAt` minus `startedAt`, for display only.
- Added on stderr: `aval: warning: reuse off: <cause>` when an unreadable policy, a registry error or inputs that cannot be fingerprinted turn reuse off. The test still runs.
- On a verification failure `aval run` prints the `aval: SECURITY:` line when it finds it and again as the last stderr line, after any other error.
- The SECURITY lines link to `https://aval.run/<version>/trust-model/` (`latest` for dev builds) instead of the GitHub file.
- Removed: `test <name> fingerprint <digest>` (the outcome line carries the digest), the `cacheability <class>: ...` note when the reason already names the class, and `aval: test failed` / `aval: reused result failed: test failed` (the summary and exit 3 say it).
- Reworded reasons in `aval run`: `no registry configured: pass --registry to reuse verified results` is now `reuse off: no registry (set AVAL_REGISTRY or --registry)`, `reuse disabled (--no-reuse)` is now `reuse off: --no-reuse`, and a missing policy is `reuse off: no trust policy (set AVAL_POLICY or --policy)`. The outcome stays `miss`. `aval verify` keeps `no trust policy configured: pass --policy to reuse verified results`.
- The browser demo prints the same lines.
