# aval

## 0.8.1

### Patch Changes

- 806588c: New guide "Integrate Aval in 5 files" and the example `examples/github-monorepo`
  (#217): a monorepo with a Go module, a Node UI, a Dockerfile, a data
  directory and an end-to-end suite in 3 shards, put on Aval by adding
  `aval.yaml` and `.github/aval/policy.yaml` and editing `ci.yml` and
  `.gitignore`. Aval's tests check that the integration is exactly those files,
  run every test through the CLI (miss and push, reuse per shard, a fragment
  edit reruns only its tests, an unaccepted mutable reference, exit 5 on a
  tampered attestation), and CI checks that the run action ignores a trust
  policy edited in a pull request. No CLI or format change. Known gap: the
  GitHub publish action cannot push a test with `outputs` yet, because the run
  action's artifact holds `.aval/<test>/` only.
- 6815771: The release gate verifies the e2e attestation with the aval release `ci.yaml`
  pins, the one that signed it, instead of a binary built from the tag (#234).
  A newer `aval` computes `aval-fingerprint/v2`, so the gate missed (exit 4) on
  v0.8.0 and published nothing: the v0.8.0 tag has no GitHub Release, and this
  patch ships its changes as v0.8.1.

## 0.8.0

### Minor Changes

- 7de8cd6: `scope.allowMutable` entries take an optional `files` list of globs (#208).
  One entry accepts its exact `ref` in every file in scope a glob matches,
  instead of one entry per file. `files` excludes `file`, which stays an exact
  path; each glob must match a file in scope, otherwise `aval` exits 2. Format
  change: an additive optional contract field under the same `apiVersion`.
  Existing contracts hash as before; an older `aval` rejects a contract that
  uses `files` (unknown field), which fails closed.
- b0b968a: Every security event Aval detects now exits 5 (#195). A verified result beside
  an attestation that failed verification is no longer reused: `aval run` runs
  the test and exits 5, and `aval verify` exits 5 with the unchanged report
  (outcome `verified`, the bad attestation in `rejected`). More than 64
  attestations for one fingerprint is now outcome `verification-failed` (exit 5
  in `aval run` and `aval verify`) instead of `error`. The run record of such a
  run says `verification-failed`, so `aval stats` counts it under
  `verificationFailures`. Consumers no longer need to inspect `rejected`: the
  exit code alone separates miss, error and security event. No new outcome or
  exit code; the verify report, predicate and fingerprint are unchanged.
- b518636: New GitHub composite action `integrations/github/publish` (#212): one step in
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
- 15ade1b: New GitHub composite action `integrations/github/run` (#211): a CI job runs
  one test through Aval with `uses: TrianaLab/aval/integrations/github/run@<sha>`
  and `with: {test: <name>}` (inputs `file` and `policy` are optional). It
  refuses `pull_request_target`, `workflow_run` and any job with
  `id-token: write`; installs the aval release pinned in its `action.yml`,
  checked against the per-platform sha256, signature and provenance; fetches
  the trust policy, and the registry inside it, from the default branch
  through the contents API on every event (no policy: nothing is reused, with
  a warning); writes the outcome to the step summary; turns exit 5 into an
  `aval SECURITY` error annotation; uploads `.aval/<test>/` as the artifact
  `aval-<test>` on a push to the default branch only; and warns when the
  policy trusts a `refs/pull/` subject at or above `minTrustLevel`. The
  action pins 0.7.0 until the next release, which has `--test` and the
  policy's `registry` (#213). `docs/guides/github-actions.md` is rewritten
  around it. No change to the aval binary, its exit codes or formats.
- ecc519f: `aval init --set` writes a two-file starting layout (#215): `aval.yaml` as a
  `kind: TestSet` with the test and one fragment holding the source globs, and
  `.github/aval/policy.yaml` with one placeholder keyless identity whose
  anchored `subjectRegexp` trusts no signer until `<OWNER>/<REPO>` is replaced.
  Scope and cacheability stay required. An existing file exits 2 unless
  `--force`. No format change.
- 6a91ec3: The installers can pin and require verification for CI (#202).
  `get-aval.sh --sha256 <hex>` (or `AVAL_SHA256`; `<os>_<arch>=<hex>` per
  platform, the entry for the detected platform required) refuses an archive
  with another digest or one that `checksums.txt` does not list with it.
  `--require-verify` (`AVAL_REQUIRE_VERIFY=1`) makes a missing `cosign` an
  error, `--require-provenance` (`AVAL_REQUIRE_PROVENANCE=1`) does the same for
  `gh`; both need `--version` and exclude `--no-verify`. `install.ps1` reads the
  same variables. Interactive installs behave as before. `docs/install.md`
  has a CI recipe, and Aval's own CI installs its pinned release this way.
- 4f70316: `aval run` and `aval verify` take `--min-trust-level` (or
  `AVAL_MIN_TRUST_LEVEL`) to raise the trust policy's `minTrustLevel` for one
  run (#214), so one policy can trust developer keys on PRs and require
  `runner` on main. A value below the policy's minimum, or an unknown level,
  exits 2. A result below the raised minimum is a miss (exit 4), never exit 5.
  The verify report is unchanged. `runner.command` does not see
  `AVAL_MIN_TRUST_LEVEL`.
- e5cf98f: The trust policy takes an optional `registry` field (#209). `aval run` and
  `aval verify` use it when neither `--registry` nor `AVAL_REGISTRY` is set, and
  the `config:` line names the source that won (`registry=<x> (policy <file>)`).
  Reading the policy from a protected branch now carries the registry too.
  A policy `registry` with credentials (`user:password@`) exits 2 without
  printing them.
  
  Format change (additive): `kind: Policy` gains an optional field under the
  same `apiVersion`. Older aval releases reject a policy that uses it (unknown
  field), which fails closed; policies without it work everywhere.
- 83ba4b5: `aval push --report-file FILE` reads the report written by the `aval run` that
  recorded the result (#201). When its outcome is `verified`, push prints
  `nothing to push: ...`, signs nothing and exits 0, so split-credential
  publishing is one command and a stale result file is not published again.
  Any other outcome publishes the result as before. A report for another test id
  or fingerprint, an unknown report version or an unreadable report exits 1. No
  format changes: the predicate, verify report, fingerprint and exit codes are
  unchanged.
- 1c8baae: `aval push --test <name>` publishes only the test an artifact is named after
  (#210). A result or report for another test, or for inputs other than the
  ones push recomputes from the checkout (a changed file or declared env value),
  exits 1 and publishes nothing. Behaviour changes: a report that records a
  security event (outcome `verification-failed`, or a `rejected` entry that
  failed verification) now makes push sign nothing and exit 5, where it used to
  publish; and `aval push` never publishes a `non-deterministic` test, it prints
  `nothing to push: ...` and exits 0. `aval run --push` is unchanged.
- 8efc307: New optional contract field `scope.root` (#200): the path from the
  contract's directory up to the root of its tree, `..` segments only (`..`,
  `../..`). A contract such as `aval/unit.yaml` with `root: ..` behaves as if
  its file sat in the root: globs, `outputs`, `allowMutable[].file` and `files`, manifest
  and finding paths, and `execution.outputs.files[].path` are relative to the
  root; `runner.command` and `environment.probe` run there; the
  `--result-file`, `--report-file` and `--stats-file` skip set is computed
  against it. `scope.contracts` paths stay relative to the contract file. The
  root must lie inside the git working tree that holds the contract, otherwise
  `aval run`, `explain`, `verify` and `push` exit 2. `aval explain` prints the resolved
  root. Globs still cannot contain `..`.
  
  Format changes: additive change to the contract schema
  (`aval.trianalab.io/v1` stays). Contracts without `scope.root` keep their
  fingerprint byte for byte; setting or changing it changes the `contract`
  manifest line and so the fingerprint. The manifest version
  (`aval-fingerprint/v2`) does not change. Older `aval` binaries reject a
  contract that uses the field.
- a4e260c: `-t/--test <name>` on `aval run`, `explain`, `verify` and `push` selects one
  test of a contract file (#207). A TestSet of more than one test without
  `--test` exits 2 and lists the names; on a `kind: Test` the flag is optional
  and must equal `metadata.name`. With `--test` the output files default to
  `.aval/<name>/{result,report,record}.json`, and `aval run` removes a stale
  `.aval/<name>/` first; without it nothing changes. `aval explain --test
  <name> --expand` prints the expanded standalone `kind: Test`, and
  `aval explain --all [-o json]` fingerprints every test without running
  anything, reports every failing test by name and exits 2 (or 1). A TestSet
  expands to at most 1024 tests in total, plain tests included.
  
  Format changes: additive CLI flags only. No new exit code; the verify report,
  predicate and manifest version do not change.
- 013ebd5: The fingerprint skips every entry named exactly `.git`, whatever its type
  (#199). A linked worktree or a submodule has a `.git` file that holds a
  machine-specific path; a `**` source glob used to hash it, so the main
  checkout, each worktree and each machine got a different fingerprint for the
  same commit and scope. They now share one, and the `exclude: ["**/.git"]`
  workaround is no longer needed. A `.git` symlink is skipped instead of failing
  the fingerprint. The match stays case-sensitive (`.GIT` is still an input). An
  exact glob such as `source: ["pkg/.git"]` now matches no file and fails the
  fingerprint (exit 1, `matched no files`). `outputs` skip a `.git` file too, so
  it is never published.
  
  Format changes: the fingerprint manifest moves from `aval-fingerprint/v1` to
  `aval-fingerprint/v2`. Every existing attestation becomes a miss once, so each
  test runs again and publishes a v2 result; there is no fallback lookup by v1.
- 26058a5: New contract kind `kind: TestSet` (#206): many tests in one file, with
  `defaults`, named `fragments` a test pulls in with `use`, and a `matrix`
  that creates one test per value. Merge order is defaults, the used
  fragments, then the test; lists are unioned, so nothing inherited is
  removed, and a toolchain, probe or `scope.root` key set to two values exits
  2 naming both sources. `matrix` substitutes `{var}` into `metadata.name` and
  `runner.command` only. Each test expands to a standalone `kind: Test` and
  fingerprints byte for byte like it. Unknown or unused fragments, a fragment
  that uses another or sets `exclude`, `runner`, `metadata`, `outputs`,
  `matrix` or `cacheability`, an unknown `{var}`, duplicate expanded names and
  a `scope.contracts` entry that points at a TestSet exit 2. `aval run`,
  `explain`, `verify` and `push` take a TestSet of one test; selecting one test
  of many comes with #207.
  
  Format changes: additive new kind under `aval.trianalab.io/v1`. Older
  `aval` binaries reject `kind: TestSet` with exit 2 (fail closed). The
  manifest version (`aval-fingerprint/v2`) does not change.

### Patch Changes

- f8dbb8a: `aval run` no longer exits 1 when an `environment.probe` fails without
  `--push` (#197). It still looks nothing up (outcome `error`), runs the test
  and records nothing to `--result-file`, so `aval push` cannot publish the
  result; it now warns `result not recorded: <reason>` and exits with the
  test's code (0 passed, 3 failed). With `--push` it still exits 1
  (`result not published: <reason>`), or 3 when the test failed. No new exit
  code or outcome; the verify report, predicate and fingerprint are unchanged.
- fb575db: `aval run` no longer loops without end when `runner.command` starts `aval run`
  on the same contract again, directly or through other contracts (#196). It
  passes `AVAL_RUN_STACK` to `runner.command`, and a nested run of a contract
  already in it exits 2; the outer run records the test as failed (exit 3). A
  nested run of a different contract still works. `environment.env` may no
  longer list `AVAL_RUN_STACK`. No format changes: the fingerprint, predicate
  and verify report are unchanged.
- 21f9727: After each release, the release workflow opens a PR that moves the aval
  version and per-platform sha256 pinned in `integrations/github/run` and
  `integrations/github/publish` to that release, from its `checksums.txt`
  (#213). Pinning either action by commit SHA pins the aval binary too, and one
  Dependabot bump upgrades both. `tests/release` fails when a pin does not
  match the pinned release's `checksums.txt`. No change to the aval binary,
  its exit codes or formats.
- 9cc6596: The fingerprint no longer refuses a symlink that `scope.exclude` covers by
  name and contents, such as `node_modules/.bin/tool` under `pkg/**` with
  `**/node_modules/**` excluded (#198). The symlink is skipped, not followed,
  and stays out of the manifest. A symlink a `dependencies` glob reaches, or
  one excluded only by name or only by its contents, is still an error. No
  format changes: every tree that fingerprinted before keeps its fingerprint.

## 0.7.0

### Minor Changes

- f5c436b: New `aval explain [--digest]`: it prints `fingerprint manifest for <test> (aval-fingerprint/v1)`, the manifest, and `fingerprint sha256:...` as the last line; `--digest` prints only the digest. It never looks up a result and never runs the test; a fingerprint error exits 1 with `aval: fingerprint: ...`, as `run --explain` does. `run --explain` and `run --fingerprint-only` keep their behaviour. New `aval stats publish FILE` publishes a run record and replaces `aval record`. Flags are named for what they hold; the old names keep working, hidden from help, and print a deprecation notice on stderr only (stdout, files and exit codes are unchanged). Deprecated in 0.7.0, removable no earlier than 0.8.0:
  
  - `aval run --result` and `aval push --result`: use `--result-file`
  - `aval run --report`: use `--report-file`
  - `aval run --record`: use `--stats`
  - `aval run --record-file`: use `--stats-file`
  - `aval run --repository` and `aval record --repository`: use `--source-repo`
  - `aval record --record-file FILE`: use `aval stats publish FILE`
  
  Messages that name these flags use the new names (for example `--stats requires --registry`), and `aval record` without a file now says `a run record file is required`. The browser demo accepts `aval explain` and `aval explain --digest`.
- f5c436b: `--registry` and `--policy` fall back to `AVAL_REGISTRY` and `AVAL_POLICY` (flag > environment > unset; an empty value counts as unset). There is no config file and no `aval.yaml` field: an architecture test keeps the registry and policy out of the contract and out of any file in the tree. When a value comes from the environment, `aval run` prints one `config:` line on stdout, and `verify`, `push`, `stats` and `record` print it on stderr. `runner.command` no longer sees `AVAL_REGISTRY` and `AVAL_POLICY`, like the signing token. `aval record` without `--record-file` now says `--record-file is required`. Exit codes and the verify report are unchanged.
- f5c436b: Error messages say what to do (#167). Exit codes, outcomes and the verify report are unchanged: a report's `reason` keeps the raw error.
  
  - A registry that cannot be dialled or does not answer in time reads `cannot reach <host> (<cause>); is the registry running? check AVAL_REGISTRY or --registry`: in `aval run`'s reuse-off warning, on stderr for `aval verify` (stdout unchanged), and for `push`, `run --push` and `stats publish`.
  - `--registry` with a scheme says `drop https://: try --registry <host/name>` (exit 2, as before).
  - A public key passed as `--key` says `<x>.pub is a public key; pass the private key (<x>.key)`.
  - Signing without a key, a token or a terminal lists the three ways to sign, one indented line each.
  - `aval push` without a saved result adds `run aval run first, or use aval run --push` (exit 1, as before).
  - `aval run --push --key` with a policy that does not list the key warns `aval: warning: key <file> (key id <x>) is not in <policy>; consumers with this policy will reject the result`. The result is still published.
  - Contract errors print as `aval: <file>: invalid contract:` and one indented, escaped line per problem, in YAML terms: `line 8: scope.sources: unknown field; did you mean source?`, `line 6: runner.command: got a string ..., want a list of strings`. The browser demo prints them the same way.
- f5c436b: `aval --help` groups commands into Core, Publishing and Statistics, and every command has an example. A mistyped command gets a suggestion (`aval rn` and `aval help rn` print `unknown command "rn"; did you mean "run"?` and exit 2; `aval help rn` used to print the root help and exit 0). A mistyped flag gets the closest flag of that command and a pointer to its help (`unknown flag: --registy; did you mean --registry? see aval run --help`). `aval run --help` is shorter. The exit code constants are unchanged; `aval help <typo>` now exits 2 (usage) instead of 0.
- f5c436b: `aval init --name NAME --source GLOB... --cacheability CLASS --key NAME|--keyless [--force] -- <command>` writes `aval.yaml` (validated before it is written), with `--key` a signing key pair (`NAME.key` and `NAME.pub`) and `policy.yaml` trusting it at trust level developer, and adds the key and `.aval/` to `.gitignore`. Nothing is inferred: `--source` and `--cacheability` have no default, init lists the files the globs match, rejects a glob that matches nothing and never runs the test. It never replaces a file without `--force` (exit 1); with `--force` it removes the old file and creates a new one, so a symlink is replaced, not written through, and a failed write removes the key pair it wrote. A file name in the preview or an answer is escaped before it reaches the terminal. When a value is missing it asks numbered questions only if stdin and stdout are terminals; otherwise it exits 2 and names the missing flags. The quickstart now starts with `aval init`.
- f5c436b: `aval keygen [name]` writes a signing key without openssl: `name.key` (unencrypted PKCS#8 ECDSA P-256 PEM, mode 0600) and `name.pub` (PKIX PEM, the file a policy trusts with `publicKey`), with `aval` as the default name (a trailing `.key` is dropped; an empty name is a usage error, exit 2). It prints the key id the verify report shows for this signer and refuses to replace either file without `--force` (exit 1). The key hints of `aval run --push` and `aval push` now point to `aval keygen`, and the quickstart and the uv-monorepo example no longer need openssl.
- f5c436b: Plain output of `aval run` v2 (CI logs and pipes). Every line CI greps today keeps its shape: `<outcome> <test> <digest>`, `signer:`, `reference:`, `reason:`, `rejected:`, `ran <test>: <result> (exit N)`, `reused <result> result`, `pushed`, `restored`, `recorded`, `config:`. `aval verify` text and `-o json` are unchanged, and so are all exit codes. Changed lines:
  
  - Added: `--- run <test>: <command>` before and `--- end <test>: <passed|failed> (exit N) in <time>` after the test's own output, which passes through untouched.
  - Added: a closing `summary: <test> <ran|reused>: <passed|failed>[ in <time>][; saved <time>][; result pushed][; reuse was off][; SECURITY]`. `saved` is the reused run's signed `finishedAt` minus `startedAt`, for display only.
  - Added on stderr: `aval: warning: reuse off: <cause>` when an unreadable policy, a registry error or inputs that cannot be fingerprinted turn reuse off. The test still runs.
  - When the test command cannot start, `--- run` is not closed and no `summary:` follows; stderr says `aval: running "<command>": ...` and the exit code stays 1.
  - A fingerprint flooded with more attestations than Aval lists (#135) is a security event in `aval run` too: no reuse-off warning, `; SECURITY` in the summary and the flooding SECURITY line repeated last on stderr. The exit code is unchanged.
  - On a verification failure `aval run` prints the `aval: SECURITY:` line when it finds it and again as the last stderr line, after any other error.
  - The SECURITY lines link to `https://aval.run/<version>/trust-model/` (`latest` for dev, snapshot and pre-release builds) instead of the GitHub file.
  - Removed: `test <name> fingerprint <digest>` (the outcome line carries the digest), the `cacheability <class>: ...` note when the reason already names the class, and `aval: test failed` / `aval: reused result failed: test failed` (the summary and exit 3 say it).
  - Reworded reasons in `aval run`: `no registry configured: pass --registry to reuse verified results` is now `reuse off: no registry (set AVAL_REGISTRY or --registry)`, `reuse disabled (--no-reuse)` is now `reuse off: --no-reuse`, and a missing policy is `reuse off: no trust policy (set AVAL_POLICY or --policy)`. The outcome stays `miss`. `aval verify` keeps `no trust policy configured: pass --policy to reuse verified results`.
  - The browser demo prints the same lines.
  - The quickstart GIFs still show the old output until they are re-recorded (#181).
- f5c436b: `aval run` on a terminal prints the styled output of Epic 13 (style A): one line per step behind a glyph (`✓` done, `↺` reused, `✗` failed or SECURITY, `⚠` reuse off, `·` neutral), labelled detail lines, digests as 8 hex characters (whole on the `REJECTED` line), and one closing sentence. A flooded fingerprint and a non-deterministic contract get their own closing sentence. After a verification failure nothing is green and no `✓` appears; the SECURITY block goes to stderr when found, and stderr still ends with the plain `aval: SECURITY:` line. `NO_COLOR` drops the colour only, Windows gets no colour, and stderr is styled only when it is a terminal too. Plain output is unchanged and applies when stdout is not a terminal, with `TERM=dumb`, `AVAL_OUTPUT=plain` (any case) or the new global `--plain` flag. The browser demo accepts `aval run --plain`. Exit codes, `-o json`, the report file and `aval verify` are unchanged. The browser demo uses the styled form without colour.
- f5c436b: `aval verify -o json` (verify-report/v1) gains the optional `signer.trustLevelName` (`developer`, `runner` or `isolated`) next to the numeric `signer.trustLevel`. It is derived from the matched trust-policy entry, never from the predicate. Additive: older reports still parse, and in `pkg/evidence` `MatchIdentity` and `MatchSigner` now set `Identity.TrustLevelName`. Older aval binaries (0.4.0 and earlier) do not recognise a report with this field as Aval output, so such a file in scope counts as an ordinary input for them (a miss, never a false hit) until they upgrade.

### Patch Changes

- f5c436b: The quickstart follows the new flow: it removes a leftover registry before starting one, exports `AVAL_REGISTRY` and `AVAL_POLICY` once after `aval init`, and runs `aval run --push`, `aval verify`, `aval run`, a changed input and an untrusted push (`AVAL_POLICY= aval run --push --key other.key`) that ends in exit 5, with no flags repeated. The GIFs are re-recorded in the styled output, and the "Try it" terminal's `help` describes the same setup.
- f5c436b: On a terminal, Aval shows a spinner on stderr when looking up and fetching a result, pushing one (`aval run --push`, `aval push`) or reading run records (`aval stats`) takes more than 300ms, for example `⠹ looking up localhost:5001… 4s`. It runs only in styled mode with stderr a terminal, is erased before the decision or the test's output prints, erases itself before a warning, and leaves the terminal clean on Ctrl-C. Windows gets no spinner. Plain output, exit codes and `aval verify` are unchanged.
- 6677ad3: Docs site: the install command on the home page has a copy button.
- f5c436b: Shorter docs: each page answers one question in its first line and opens with a diagram or a terminal snippet; guides and concept pages use short bullets and tables. Edge cases moved to two new Reference pages: CLI output (plain and terminal output, spinner, stderr lines) and Mutable reference scan (the full scanner rules and limits from Pin your dependencies). No behaviour changes.
- 58d3f3c: Docs site: on phones narrower than about 430px the version pill no longer overlaps the theme toggle in the header; it is hidden there.

## 0.6.1

### Patch Changes

- a0395af: `aval upgrade` names the HTTP status when GitHub answers the latest-release lookup with something other than a redirect (for example `504 Gateway Timeout` during a GitHub incident), instead of `no Location header in response`. Nothing is downloaded or replaced in that case, as before.

## 0.6.0

### Minor Changes

- 750feac: New `aval version` (prints `aval <version> (<go version> <os>/<arch>)`) and `aval upgrade [--version vX.Y.Z] [--check]`. `upgrade` resolves the latest release from the GitHub Releases redirect (no API call), verifies the keyless Sigstore signature of `checksums.txt` against Aval's release workflow at that tag, checks the archive's sha256 against its exact entry, and only then replaces the running binary atomically on Unix (on Windows the old one is kept as `aval.exe.old` and moved back if the swap fails). Without `--version` it refuses to move to an older release. Any failed check replaces nothing and exits 1; downloads are capped at 256 MiB (1 MiB for `checksums.txt` and its bundle).

## 0.5.0

### Minor Changes

- 028329d: A contract can list `outputs` (globs such as `coverage.xml`, `junit.xml`): `run --push` and `push` publish those files with the result, and a reused result restores them, so a coverage gate or a junit report downstream keeps working when the test is skipped. Outputs must not be inputs or the contract file, each glob must match a regular file, and the caps are 256 files, 16 MiB each and 64 MiB in total; otherwise nothing is published (exit 1). On reuse the files are fetched by the digests the signature covers; a missing, changed or undeclared output is a verification failure (exit 5). Format changes, all additive: contract field `outputs` (omitted when empty, so existing fingerprints do not change; older `aval` binaries reject a contract that uses it), predicate field `execution.outputs` within `execution/v2`, and the OCI identifiers `application/vnd.trianalab.aval.outputs.v1` and `application/vnd.trianalab.aval.output.v1`. Outputs are published to the registry: do not list files that hold secrets.
- b3758d5: The mutable-reference scanner now reports container images named in test code that uses testcontainers: a quoted `name:tag` without a digest in a `*.go`, `*.py`, `*.java`, `*.kt`, `*.ts`, `*.js` or `*.cs` file that mentions testcontainers. Adopters may see new findings that turn hits into misses for `hermetic` and `controlled-environment` tests: pin the image by digest, or accept it in `scope.allowMutable`.
- 318966e: New optional `environment.probe` in `aval.yaml` (additive to `aval.trianalab.io/v1`; older `aval` binaries reject contracts that use it): a map from an `environment.toolchain` key to an argv such as `python: [python3, --version]`. `aval run` runs each probe before the lookup, without a shell, with a 10 s timeout and without `$SIGSTORE_ID_TOKEN`, and requires the declared value in the output as a version token (`3.13` matches `Python 3.13.1`, not `Python 3.130`). A mismatch, a probe that cannot start, a non-zero exit, a timeout or output over 4 KiB skips the lookup (outcome `error`), runs the test, records and publishes nothing and exits 1. The observed output goes into `execution.environment` as `probe.<key>`. `verify`, `push`, `--fingerprint-only` and the browser demo never run probes. Fix: a toolchain key named `os` or `arch` no longer overwrites the real values in `execution.environment`.

### Patch Changes

- 554569c: Adoption guides: find undeclared inputs with strace, fs_usage, Process Monitor or a scope-only copy (Write a contract); map uv, pnpm and cargo workspaces to contracts (Depend on another part of the repo); estimate reuse from past commits with `scripts/estimate-reuse.sh`, which runs `aval run --fingerprint-only` over a scratch worktree and counts fingerprint errors and unacknowledged mutable references as misses (Measure reuse).
- 176cfc2: Docs: the guide "Shards and matrices" shows two ways to split a suite across CI matrix jobs with existing fields: one contract per shard (`aval.shard-1.yaml`, run with `-f`) or one contract with `SHARD_INDEX` and `SHARD_TOTAL` in `environment.env`. New example `examples/uv-monorepo`: a uv workspace with two packages, two shard contracts and coverage restored on reuse. No behaviour or format change.

## 0.4.0

### Minor Changes

- 4c8029a: `run --push` and `push` sign keyless from a laptop: without `--key` or `$SIGSTORE_ID_TOKEN` and with a terminal (or with the new `--login` flag), they open the interactive Sigstore login in the browser (`oauth2.sigstore.dev`, client id `sigstore`; `$SIGSTORE_OIDC_ISSUER` and `$SIGSTORE_OIDC_CLIENT_ID` override them) right before signing, after the test ran. The token is used for that signature only and never stored. Without a terminal and without `--login`, a run with no key and no token still fails fast with exit 2. `--key` and `--login` exclude each other.

### Patch Changes

- b619b24: More than 64 attestations for a fingerprint now prints an `aval: SECURITY:` line naming the cap, as a possible flooding attempt; the outcome stays `error` and the test runs. Aval's own CI and release jobs fail when a verify report lists a verification failure in `rejected`, even when a trusted result verified.

## 0.3.0

### Minor Changes

- 4b68dd5: `aval stats --registry <repo> [--test <id>] [--since <duration|date>] [--by test|repository] [--output text|json]` reads the run records in the registry and aggregates them on demand: runs, hit rate, misses by cause, verification failures, shadow false hits, and total and saved test time, per test (or per source repository with `--by repository`) and in total. It writes nothing. Records that are unreadable, oversized, malformed or inconsistent are skipped and counted; reading stops after 1000 records or 64 pages per listing, with a warning. New format, provisional: `aval.trianalab.io/stats-report/v1` (`pkg/evidence` adds `StatsReport`, `TestStats`, `RepositoryStats`, `RunStats`, `MissCauses`, `StatsReportVersion` and `StatsUnknownRepository`). Run records gain the optional `verifiedResult` and `repository` (`--repository` on `aval run` and `aval record`; records without it count as `unknown`), and `evidence.ParseRunRecord` now rejects inconsistent records (`reused: true` requires outcome `verified` and `testDurationMs` 0; `verifiedResult` requires outcome `verified`), so `aval record` refuses a hand-edited record that claims a reuse that never happened. Docs explain pruning old records with ORAS.
- 6351105: Registry I/O is bounded. `run`, `verify` and `push` take `--registry-timeout` (default `1m`). A registry that stalls past the timeout gives outcome `error`, and `aval run` still runs the test. Lookup follows at most 64 referrers pages. A listed attestation whose manifest was deleted (404) counts as absent: the remaining candidates can still be reused, and the outcome is a miss if every candidate is gone. One credential store and one auth cache are shared by every registry call. The keyless trusted root (a TUF fetch when `trustedRoot` is not pinned) is loaded only when a candidate's bundle carries a certificate. After pushing, `push` and `run --push` confirm that the new digest is listed among the referrers, re-pushing up to three times, and fail if it never appears.
- 7cd688f: Aval detects mutable references in a test's inputs and refuses to reuse them. A static, offline scanner reads the files in the contract scope and `runner.command`, one detector per ecosystem: container images without a digest (Compose and Kubernetes `image:`, Dockerfile `FROM` and `COPY --from`, `docker run` and `podman run`, compose files the command uses but the scope does not hold), Go (`go.mod` without `go.sum`, `go install pkg@latest`), npm, yarn, pnpm and bun (`package.json` without a lockfile, unpinned `npm i` and `npx`), Python (unpinned `requirements*.txt` entries, missing hashes, Poetry and Pipenv without a lockfile, unpinned `pip install`), Rust (`Cargo.toml` without `Cargo.lock`), Java (Maven and Gradle `SNAPSHOT`, `LATEST`, `RELEASE`, dynamic versions and ranges), Ruby (`Gemfile` without `Gemfile.lock`), PHP (`composer.json` without `composer.lock`), OS packages (`apt-get install` and `apk add` without versions), remote scripts (`curl … | sh`, remote `ADD` without `--checksum`) and git (`git clone` without a commit, submodules that track a branch). For `hermetic` and `controlled-environment` contracts any unacknowledged finding makes the lookup a `miss` (exit 4 for `verify`) whose reason lists each reference, the test runs, and `run --push` and `push` do not publish (exit 1). `external-dependencies` contracts get the findings as information; `non-deterministic` is unchanged. `aval run` prints each finding as a `mutable:` line (`--fingerprint-only` prints them on stderr). Contract format change, additive within `aval.trianalab.io/v1`: the optional `scope.allowMutable` list (`ref` and `reason`) acknowledges a reference by the exact text Aval reports. It is part of the contract hash; contracts without it keep their fingerprint, and the manifest stays `aval-fingerprint/v1`. Older `aval` binaries reject contracts that use the new field. A contract whose scope holds a mutable reference that earlier versions reused now misses until the reference is pinned or acknowledged. Guide: "Pin your dependencies" (`docs/pinning.md`).
- 0e259b7: Aval resolves declared artifacts to immutable ids at fingerprint time. The optional `scope.resolve` list takes `kind: reference` entries for container images (`image`, tag to manifest or index digest), npm, PyPI, Go modules (through the module proxy, `h1:` hash), Maven, crates.io, RubyGems, git (ref to commit with `git ls-remote`) and plain URLs (sha256 of the download, up to 256 MiB). Each resolves a tag, a version range or a branch to its digest, checksum or commit, which goes into the manifest as a `resolve <kind> <reference> <version> <id>` line: a repushed tag, a new release inside a range or a moved branch changes the fingerprint. Resolution is opt-in and stateless, bounded by a 2-minute deadline; a reference that cannot be resolved fails the fingerprint, so nothing is looked up, the test runs and nothing is published, and the reason names the reference. `aval run --explain` prints the time each lookup took on stderr. `scope.resolve` never clears a finding of the mutable-reference scanner: it makes a move change the fingerprint, and the reference still blocks reuse until it is pinned in place or accepted with `scope.allowMutable`. `scope.resolve` binds the named artifact, not its transitive dependencies. Registries come from `$npm_config_registry`, `$GOPROXY`, `$AVAL_PYPI_URL`, `$AVAL_MAVEN_URL`, `$AVAL_CRATES_INDEX` and `$AVAL_RUBYGEMS_URL`; credentials from the Docker config, `~/.npmrc` tokens, `~/.netrc` machine entries and git, sent only over https or to a loopback host. Format changes, additive: the contract field within `aval.trianalab.io/v1` (older binaries reject contracts that use it), and the `resolve` manifest lines within `aval-fingerprint/v1`, which only contracts with the field have; every other manifest is unchanged. Guide: "Resolve references" (`docs/resolve/`).
- 8d4b8d8: Run statistics in the registry. `aval run --record` publishes one run record per run, for every outcome, with the test id, fingerprint, outcome, whether the result was reused, the result, the test duration, the saved duration, the aval version, a timestamp and an optional `--ci-run-url`. Records hang off a per-test stats anchor tagged `stats-<test id>`, never off a fingerprint anchor. `run` and `verify` never read them, so a record cannot cause reuse or count toward the candidate cap. Recording is best-effort: a record that cannot be published, for example without write credentials, is a warning and never changes the exit code. `--record-file` writes the record to a file and `aval record` publishes it later. New format identifiers, provisional: `aval.trianalab.io/run-record/v1`, `application/vnd.trianalab.aval.run.v1` (`+json` for the layer) and `application/vnd.trianalab.aval.stats.v1`. `pkg/evidence` adds `RunRecord`, `ParseRunRecord` and these constants.
- ff2a2c8: A contract can depend on other contracts in the same repository. The optional `scope.contracts` list takes paths to other `aval.yaml` files, relative to the contract (`../backend/aval.yaml`). Each referenced contract is fingerprinted from its own directory and scope, and its fingerprint goes into the manifest as a `contract <path> <name> <fingerprint>` line, transitively: a change inside a referenced contract's scope makes the dependent miss and run, a change outside it does not. References must stay inside the git working tree of the contract (the nearest directory with a `.git` entry); a missing, invalid, symlinked or cyclic reference, or more than 8 levels or 64 contracts, fails the fingerprint, so the test runs. A referenced contract's mutable references block the dependent's reuse unless the referenced contract's own `scope.allowMutable` accepts them. `aval run --explain` prints the dependency tree after the manifest. Format changes, additive: the contract field within `aval.trianalab.io/v1` (older binaries reject contracts that use it), and the `contract <path>` manifest lines within `aval-fingerprint/v1`, which only contracts with the field have; every other manifest is unchanged. Guide: "Depend on another part of the repo".
- 6b94c21: One-line installers: `scripts/get-aval.sh` (Linux and macOS, `curl | bash`) and `scripts/install.ps1` (Windows, `irm | iex`). Both refuse a download whose sha256 does not match the release's `checksums.txt`, and verify the release signature (cosign) and SLSA provenance (`gh attestation verify`) when those tools are installed. See docs/install.md.

### Patch Changes

- 0553f1e: `scope.allowMutable` entries take an optional `file:` (the path a finding prints, relative to the contract, or `runner.command`): the entry then accepts the reference in that file only. A ref-only entry still accepts a reference that one file reports; when several files report it (generic refs such as `resources[0]` or `patches[0]` repeat across kustomizations), nothing is accepted and the finding asks for `file:`. With `scope.contracts`, each contract's entries apply to its own findings. The scanner also reports kustomize `helmCharts` entries other than a local chart in the scope, and patches given by a path outside the scope; checks instructions behind `ONBUILD`; uses the `# escape=` character when it splits heredoc words; and no longer reports a `range` loop's key variable used as a key directly under `labels:` or `annotations:` in a Helm template. Since the previous release, image refs in JSON files (`"image": "postgres:16",`) no longer end in `",`: update an acknowledgement written with the trailing characters to the bare reference (`postgres:16`).
- 2df8ee2: A signer the trust policy lists but below `minTrustLevel` is now reported as `miss` (exit 4), and with outcome `miss` in the verify report's `rejected` list, instead of `verification-failed` (exit 5). Such an attestation is still never reused, and a false claim from that signer stays `verification-failed`. The report stays `aval.trianalab.io/verify-report/v1`; `rejected[].outcome` may now also be `miss`. Rejected verification failures are printed with the `aval: SECURITY:` prefix even when another candidate verifies. `evidence.Verify` is unchanged.
- e5497e1: `--registry-timeout` help no longer claims "push has its own" deadline on `aval push`, `aval record` and `aval stats`; only `aval run` says lookup and fetch share one. Shorter `aval run`, `aval record` and `aval stats` help. The docs site is shorter and single-sourced: the README links into the site instead of copying the quickstart, and the resolver pages and vision move under Reference and Project.
- 7fae6b0: The fingerprint no longer hashes Aval's own outputs: `.aval` directories at any depth, and the `--result` and `--report` paths of the command when they fall inside the contract root. A broad scope such as `scope.source: ["**"]` now keeps its fingerprint across `aval run`s, and `aval push` accepts the result `aval run` recorded. The manifest format stays `aval-fingerprint/v1`: trees with no Aval output in scope keep byte-identical manifests and digests (the committed v1 golden and Aval's own contracts are unchanged). Only manifests that listed a file under `.aval/` or a `--result`/`--report` file change, to the manifest of the same tree without those files. Such a manifest held a result from an earlier run, so it changed on every run and was not reusable in practice; results published from a clean checkout (no Aval output yet) keep their fingerprint and stay reusable. `.aval/` is now reserved for Aval: a file the test reads must not live there.
- 91a57b8: `run --push` and `push` check their preconditions before anything runs and exit 2 (usage) instead of failing with exit 1 after the test: a `--key` that cannot be read or is not an unencrypted ECDSA P-256 PEM key (the error names the requirement and the `openssl` command to create one; cosign keys are encrypted and not supported), or no `--key` and no `$SIGSTORE_ID_TOKEN`. `run`, `verify` and `push` reject a `--registry` that is not an OCI repository (a scheme, no repository, a tag or a digest) with exit 2 and an example. A `--result` or `--report` path that the contract scope includes and that holds a real input (anything other than an Aval result or report) exits 2 before the test runs, because skipping it would drop that input from the fingerprint; `run` also refuses to overwrite any existing file at those paths that is not exactly an Aval result or report. Exit codes keep their values; only these cases move from exit 1 to the existing exit 2.
- c7c5888: Docs site: live run statistics. The home metric strip and a new Stats page (Project > Stats) read `stats.json`, which the docs workflow rebuilds twice an hour and on every push from `aval stats --by repository --since 30d` over the public stats registry `ghcr.io/trianalab/aval-stats`, without rebuilding any page; a failed refresh keeps the previous file, and a missing or stale file shows a neutral state. Aval's own CI now publishes its run records there. The Stats page explains how to keep statistics in your own registry and how a repository joins the public aggregate.
- f14b7b4: Docs site: the mobile menu (hamburger) now slides in the site navigation on every page. On inner pages the panel opened under the dark overlay; on the home page there was no panel at all. `make docs-nav-smoke`, part of `make ci-docs`, opens the menu at a phone width on the home page and an inner page and checks the panel is on screen with links.
- 133adf0: The README now has an Install section and a local quickstart (registry:2, an openssl P-256 key, `aval run --push --key`, `aval verify`) that reaches `verified`; an acceptance test runs it as written. A missing `aval.yaml` and a pre-freeze `aval.dev/` `apiVersion` (contract or policy) now print a next step. `aval verify --help` lists exit codes 1 and 2, and `docs/cli-reference.md` has one exit-code table for every command. New docs: registry credentials, the network and HOME needs of keyless verification and how to pin `trustedRoot`, and the 64-candidate cap with its cleanup.
- ed37d7e: New docs Quickstart page: copy-paste steps with the expected output from an empty folder to a verified reuse, a changed input and a rejected attestation (exit 5), with terminal recordings of real runs and a "what just happened" diagram; an acceptance test runs the page and checks every output it shows. `make docs-demos` re-records the GIFs from VHS tapes. The Install page now shows a manual release download with cosign and checksum checks.
- 5871b37: Readable errors and reasons. Output escapes only control and non-printable characters (quotes and backslashes print as they are), and each cause of a joined error gets its own `aval:` line (an invalid contract) or is separated by `; ` in the report reason (an invalid policy). A reason is printed once, in the report: stderr no longer repeats it, and a verification failure adds a single `aval: SECURITY: ...` line that links to `docs/trust-model.md`. A signer missing from the policy gets the reason `signer key id "<id>" is not in the trust policy (trust policy <file>)` instead of the Sigstore error. Skipped lookups (no policy, no registry, `--no-reuse`, a cacheability class that is never or not allowed to be reused) say only why they did not look; they stay misses. No `attest.Sign:` or `registry.Lookup` prefixes reach users. Text-mode `verify` prints `signer:` (with the trust level name) and `reference:` lines for a verified result. `non-deterministic` is called "never reusable" everywhere. The JSON verify report is unchanged except for the wording of `reason`, where joined causes are now separated by `; `.
- cb81760: `scope.resolve` detects registry overrides as the tools read them. pip variables match in any spelling pip reads (`PIP_` then any letter case, `-` equal to `_`, one optional leading `--`: `PIP_index-url`, `PIP___INDEX_URL`). `~/.npmrc` is read as ini, like npm: quotes stripped from keys and values, `;` and `#` comments, keys under a `[section]` ignored, `${VAR}` expanded in keys and values as npm does (an undefined variable stays literal); a line Aval cannot read (including one with U+2028 or U+2029, or a quoted key or value that is JSON other than a string) fails the fingerprint and sends no token. A floating Maven version fails when `<latest>` or `<release>` is a SNAPSHOT or not in `<versions>`. Errors show only the host of a URL, so a token in a registry path (`https://npm-proxy.fury.io/<token>/user/`) is masked too.
- 9082a6c: `pkg/evidence`: `ErrNotImplemented`, `Verification.TrustLevel` (use `Verification.Signer.TrustLevel`) and `Runner.Issuer`/`Runner.TrustLevel` are deprecated; nothing is removed. Pushing an attestation without a Sigstore bundle is now refused, since it could never verify. Keyless signing always uses the Sigstore public-good instance, as the CLI already did.
- 9556f78: The mutable-reference scanner adds parser-based detection to its line-by-line pass, which still runs on every file it ran on before, so it finds everything it found before. Dockerfiles named as such (`Dockerfile`, `Containerfile`, `Dockerfile.*`, `Dockerfile-dev`, `Dockerfile_windows`, `*.dockerfile`) are also split the way BuildKit does: a comment line ending in `\` no longer swallows the next instruction, continued lines join with nothing (`FROM db:1\` then `6` is `db:16`), comment and blank lines inside a continuation are dropped, a leading `# escape=` sets the continuation character, and heredoc bodies are kept apart. YAML is also decoded: `image:` keys are matched by their decoded value (`"ima\x67e":`, `? image`, `!!binary`, tagged, anchored or alias keys, nested sequences), after an anchor or an empty flow value, or after a lone CR, NEL or LS line break. The scanner also reports kustomize `images:` and `imageTags:` entries with `newName`, `newTag` or `tagSuffix` and no digest, kustomize patches, replacements, transformers, generators and `helmCharts` that set an image (by ref, such as `patches[0]`), `kind: ImageTagTransformer`, images in Helm `*.tpl` helpers, `RUN --mount=...,from=` images, JSON manifests under `templates/` or listed in kustomize `resources`, Compose `build.dockerfile_inline` and `docker-image://` `additional_contexts`, and every name of a multi-name or lowercase Dockerfile `ARG`. What Aval reports because it cannot tell (a templated key in a Helm template, a YAML key with an unknown tag or an undecodable escape, an undecodable heredoc name, a `FROM` image with a variable that is not a global `ARG` with a value, a kustomize resource that is remote or outside the scope (`resources[2]`), a tagged key in a template the parser rejects, a Dockerfile BuildKit refuses) is in the `container` ecosystem, so `scope.allowMutable` can acknowledge it; only an unreadable or oversized file stays in `scan`. A shell continuation at the very end of a file is no longer dropped. docs/pinning.md now says the scanner is a best-effort safety net and lists its known limits.
- 59adaf5: The installers are served from the docs site: `curl -fsSL https://aval.run/install.sh | bash` and `irm https://aval.run/install.ps1 | iex`. The site root redirects to the latest release (to `main` before the first one), and the outdated-version banner shows only on releases older than `latest`, never on `main`.
- 6a2a118: `aval stats` finds run records on registries that do not sort tags, such as GHCR: it lists every tag and filters by the `stats-` prefix instead of starting the listing after `stats`. Up to 64 pages of tags are read (6400 tags at a registry page size of 100); past that the report carries a truncation warning.
- 843f7a3: A symlink in the contract directory under which a `source` or `dependencies` glob could match files (for example `pkg/sub -> /elsewhere` with `**/*.go`), now fails the fingerprint with an error naming the path. Before, it was skipped, so edits behind it kept the same fingerprint and a stale result could be reused. A symlink no glob can reach is still ignored, and a contract directory that is itself a symlink fingerprints like its target.
- 27d7391: Try Aval in the browser: click the terminal on the home page of the docs site (or its "Try it live" button) and it runs the real Aval compiled to WebAssembly, with a simulated test, an in-memory registry and a throwaway key generated in the browser. It supports `aval run` (a miss, then a verified reuse), `aval run --explain`, `aval verify -o json`, `edit <file>`, `tamper` (the next verify fails with exit 5), `reset` and `help`. Nothing is fetched until the visitor clicks the terminal or presses a key in it, and without WebAssembly the page keeps the recorded transcript. Keyless signing is not available in the browser.

## 0.2.0

### Minor Changes

- 0148c76: Formats are frozen. The contract and policy `apiVersion` is now `aval.trianalab.io/v1` and the verify report `version` is `aval.trianalab.io/verify-report/v1`; `aval.dev/v1alpha1` contracts and policy files are rejected, so change that line in yours. The contract hash covers `apiVersion`, so results published before are misses and each test runs once. The predicate type (`https://aval.trianalab.io/attestation/execution/v2`), media and artifact types (`application/vnd.trianalab.aval.execution.v2`, `application/vnd.trianalab.aval.fingerprint.v1`) and predicate `schemaVersion` `v2` are unchanged and now frozen, so attestations written by v0.1.0 keep a valid, readable format. Compatibility rules (additive changes within a version, what needs a new version, how consumers treat unknown fields and values, deprecation) are in `docs/artifact-format.md`, and golden files guard the JSON shape of the statement, predicate and verify report.

### Patch Changes

- c17dc26: `aval run` and `aval verify` escape newlines and control characters in reasons printed as text and on stderr, so text from an untrusted attestation cannot start a log line of its own. Aval's own CI now reuses its signed e2e results, and a release requires a verified e2e attestation for its commit.
- 59b9b66: `pkg/evidence` is semver-stable Go API: within a major version it changes additively only, and CI fails when an exported identifier is removed or changes type or signature without a new major module path. `examples/evidence-consumer` is a separate module that consumes Aval evidence through `pkg/evidence` alone, with its own signature verifier. The policy is in `docs/integrations.md`.
- bbf9e55: Fingerprinting fails closed in two more cases: a `scope.source` glob that matches no file (a typo, or a name that differs on disk by case or Unicode form), and a file excluded only because a trailing `**` in `exclude` matched zero segments (`build/**` against a file named `build`). Trees that fingerprinted before and still do keep the same fingerprint. A table-driven false-hit corpus now pins every kind of relevant and irrelevant change.

## 0.1.0

### Minor Changes

- a6d2b1a: First release: signed `aval` binaries for Linux, macOS and Windows with checksums, SBOMs and SLSA provenance. `aval --version` prints the release version.

### Patch Changes

- 59664a3: Dependency updates: `oras.land/oras-go/v2` 2.6.4 to 2.7.0; GitHub Actions `actions/checkout` to v7.0.1, `actions/setup-go` to v7.0.0, `actions/upload-artifact` to v7.0.2, `actions/download-artifact` to v8.0.2 and `actions/dependency-review-action` to v5.0.0 (Node 24 runtime).
