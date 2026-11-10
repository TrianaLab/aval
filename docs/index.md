---
# overrides/home.html renders this page: the hero, the terminal, the steps.
# The markdown below is not shown; it is what search indexes for the page.
template: home.html
hide:
  - footer
  - toc
---

# Run a heavy test once. Reuse the result safely everywhere

Aval signs a test result and ties it to the exact inputs it used. A job with the same inputs and a trusted signer skips the test.

1. **List the inputs.** Write the files and tools your test reads in `aval.yaml`. Aval hashes them into one fingerprint.
2. **Run once, sign the result.** On a miss the test runs. With `--push`, Aval signs the result and stores it in your OCI registry.
3. **Verify, then skip.** Same fingerprint, trusted signer: the test is skipped. Anything else: the test runs.

A result that fails verification is a security event, never a quiet miss.

Already have CI for a monorepo? [Integrate Aval in 5 files](guides/integrate-in-5-files.md).
