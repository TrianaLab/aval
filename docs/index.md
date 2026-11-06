---
# overrides/home.html renders this page: the hero, the terminal, the steps.
# The markdown below is not shown; it is what search indexes for the page.
template: home.html
hide:
  - footer
---

# Run a heavy test once. Reuse the result safely everywhere

Aval signs a passing test result and ties it to the exact inputs it used. When another job has the same inputs and trusts the signer, it skips the test.

1. **List the inputs.** Write the files and tools your test reads in `aval.yaml`. Aval hashes them into one fingerprint.
2. **Run once, sign the result.** On a miss the test runs. Aval signs the result and pushes it to your own OCI registry.
3. **Verify, then skip.** Same fingerprint and a trusted signer? The run is skipped. Anything unclear, the test runs.

If Aval cannot prove reuse is safe, the test runs. A bad signature fails with exit 5, never a silent miss.
