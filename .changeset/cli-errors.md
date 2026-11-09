---
"aval": minor
---

Error messages say what to do (#167). Exit codes, outcomes and the verify report are unchanged: a report's `reason` keeps the raw error.

- A registry that cannot be dialled or does not answer in time reads `cannot reach <host> (<cause>); is the registry running? check AVAL_REGISTRY or --registry`: in `aval run`'s reuse-off warning, on stderr for `aval verify` (stdout unchanged), and for `push`, `run --push` and `stats publish`.
- `--registry` with a scheme says `drop https://: try --registry <host/name>` (exit 2, as before).
- A public key passed as `--key` says `<x>.pub is a public key; pass the private key (<x>.key)`.
- Signing without a key, a token or a terminal lists the three ways to sign, one indented line each.
- `aval push` without a saved result adds `run aval run first, or use aval run --push` (exit 1, as before).
- `aval run --push --key` with a policy that does not list the key warns `aval: warning: key <file> (key id <x>) is not in <policy>; consumers with this policy will reject the result`. The result is still published.
- Contract errors print as `aval: <file>: invalid contract:` and one indented, escaped line per problem, in YAML terms: `line 8: scope.sources: unknown field; did you mean source?`, `line 6: runner.command: got a string ..., want a list of strings`. The browser demo prints them the same way.
