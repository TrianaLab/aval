# Outcomes

Every lookup ends in one of four outcomes. Only `verified` skips the test.

```mermaid
flowchart LR
  start([look up]) --> decide{could Aval<br>decide?}
  decide -- no --> error([error<br>exit 1]):::warn
  decide -- yes --> found{result<br>found?}
  found -- no --> miss([miss<br>exit 4]):::muted
  found -- yes --> valid{verifies?}
  valid -- yes --> verified([verified<br>exit 0]):::ok
  valid -- no --> failed([verification-failed<br>exit 5]):::err
```

The exit codes in the diagram are those of `aval verify`. All of them:

--8<-- "docs/cli-reference.md:exit-codes"

## The four outcomes

| Outcome | What it means | `aval run` does |
|---|---|---|
| `verified` | a trusted result exists for these exact inputs | reuses it: exit 0 for a pass, 3 for a failure |
| `miss` | nothing to reuse: the inputs changed, or nobody ran them yet | runs the test |
| `error` | Aval could not decide, such as a registry it cannot reach | runs the test |
| `verification-failed` | a result exists but does not verify | runs the test, then exits 5 |

## A miss is routine

The normal case for a pull request: you changed an input, so there is nothing
to reuse yet. Aval also reports a miss when:

- there is no `--policy` or `AVAL_POLICY` (or, for `aval run`, no registry,
  or `--no-reuse`)
- the test is `non-deterministic`, or `external-dependencies` and not in the
  policy's `allowedCacheability`
- the inputs hold a [mutable reference](../pinning.md) you did not pin or list
  in `scope.allowMutable`
- the only results are signed below `minTrustLevel`

## A verification failure is a security event

A result under your fingerprint does not verify: a bad signature, a signer not
in your policy, or a claim of other inputs. The cause is a wrong policy or an
attacker. Aval runs the test, prints `aval: SECURITY: ...` and exits 5. Fail
the job and investigate; never hide or retry it.

## What `aval run` prints

```text
miss hello sha256:d52167b4...
reason: localhost:5001/demo/hello-aval: no attestation found for fingerprint
--- run hello: grep -q hello src/hello.txt
--- end hello: passed (exit 0) in 12ms
ran hello: passed (exit 0)
summary: hello ran: passed in 12ms
```

The test's output sits between `--- run` and `--- end`. `summary:` may add `; saved <time>` (display only), `; result pushed`, `; reuse was off` and `; SECURITY`; a failed test shows there and in exit code 3. The test can print a fake `summary:` line, so a script reads the last line of stdout. On a terminal the same steps are glyph lines (`✓` done, `↺` reused, `✗` failed or SECURITY, `⚠` reuse off, `·` neutral) with 8-hex digests, ending in one sentence that says what happened; the `REJECTED` line keeps the whole digest; after a verification failure nothing is green. A pipe, `--plain`, `AVAL_OUTPUT=plain` or `TERM=dumb` give the plain lines above; `NO_COLOR` drops colour only, and Windows gets no colour. Styled warnings go to stderr only when stderr is a terminal too. When stderr is a terminal too, a wait of more than 300ms while Aval looks up and fetches a result, pushes one (`aval run --push`, `aval push`) or reads run records (`aval stats`) shows a spinner on stderr, such as `⠹ looking up localhost:5001… 4s`; it is erased before anything else prints, and Ctrl-C erases it too. Windows gets no spinner.

On stderr, an error that turns reuse off warns `aval: warning: reuse off: <cause>`. A verification failure, or a fingerprint flooded with more attestations than Aval lists, repeats `aval: SECURITY:` as the last line. When the command cannot start, stderr says `aval: running "<command>": ...` and no `--- end` or `summary:` follows.

## In a script

Branch on the exit code of `aval verify`:

```sh
aval verify --registry "$REG" --policy policy.yaml
case $? in
  0) echo "a trusted result exists" ;;
  4) echo "miss: the test must run" ;;
  5) echo "SECURITY: a result failed verification" >&2; exit 1 ;;
  *) echo "error: the test must run" ;;
esac
```

Details: the JSON report in
[the artifact format](../artifact-format.md#verify-report).
