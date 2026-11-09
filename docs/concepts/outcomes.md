# Outcomes

What can a lookup end in? One of four outcomes. Only `verified` skips the test.

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

| Outcome | Means | `aval run` does |
|---|---|---|
| `verified` | a trusted result exists for these exact inputs | reuses it: exit 0 for a pass, 3 for a failure |
| `miss` | nothing to reuse yet | runs the test |
| `error` | Aval could not decide, such as an unreachable registry | runs the test |
| `verification-failed` | a result exists but does not verify | runs the test, then exits 5 |

The diagram shows `aval verify` exit codes. All of them:
[exit codes](../cli-reference.md#exit-codes).

## A miss is routine

You changed an input, so there is nothing to reuse. Also a miss:

- no policy, or (for `aval run`) no registry, or `--no-reuse`
- a `non-deterministic` test, or `external-dependencies` the policy does not allow
- an unpinned [mutable reference](../pinning.md) in the inputs
- only signers below `minTrustLevel`

## A verification failure is a security event

A result under your fingerprint does not verify: a bad signature, an unknown
signer, or a claim of other inputs. A wrong policy or an attacker. Aval runs
the test, prints `aval: SECURITY: ...` and exits 5. Fail the job and look.

## Read more

- What `aval run` prints: [CLI output](../output.md)
- Branch on exit codes in CI: [Consume evidence](../integrations.md)
- The JSON report: [verify report](../artifact-format.md#verify-report)
