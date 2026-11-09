# CLI output

What exactly does `aval run` print, and where?

## Plain output

Output that is not a terminal (CI logs, a pipe), `--plain`, `AVAL_OUTPUT=plain` or `TERM=dumb` give plain lines:

```text
miss hello sha256:d52167b4...
reason: localhost:5001/demo/hello-aval: no attestation found for fingerprint
--- run hello: grep -q hello src/hello.txt
--- end hello: passed (exit 0) in 12ms
ran hello: passed (exit 0)
summary: hello ran: passed in 12ms
```

- The first line is `<outcome> <test> <digest>`.
- When a value came from `AVAL_REGISTRY` or `AVAL_POLICY`, a `config:` line shows
  the registry and policy in use and where each came from.
- The test's own output sits between `--- run` and `--- end`, untouched.
- `summary:` may add `; saved <time>` (display only), `; result pushed`,
  `; reuse was off` and `; SECURITY`.
- A failed test shows in `summary:` and in exit code 3.
- The test can print a fake `summary:` line, so a script reads the last line of stdout.

## On a terminal

- One glyph line per step: `✓` done, `↺` reused, `✗` failed or SECURITY,
  `⚠` reuse off, `·` neutral.
- Digests show as 8 hex characters. The `REJECTED` line keeps the whole digest.
- One closing sentence says what happened.
- After a verification failure nothing is green.
- `NO_COLOR` drops colour only. Windows gets no colour.
- Styled warnings go to stderr only when stderr is a terminal too.

## Spinner

When stderr is a terminal, a wait of more than 300ms shows a spinner on
stderr, such as `⠹ looking up localhost:5001… 4s`. It covers:

- looking up and fetching a result
- pushing one (`aval run --push`, `aval push`)
- reading run records (`aval stats`)

It is erased before anything else prints, and on Ctrl-C. Windows gets no spinner.

## stderr

| Line | When |
|---|---|
| `aval: warning: reuse off: <cause>` | an error turned reuse off; the test still runs |
| `aval: SECURITY: ...` | a verification failure, or a fingerprint flooded with more attestations than Aval lists; `aval run` repeats it as the last line |
| `aval: running "<command>": ...` | the command cannot start; no `--- end` or `summary:` follows |

Outcomes: [Outcomes](concepts/outcomes.md). Exit codes:
[CLI reference](cli-reference.md#exit-codes).
