*[attestation]: A signed statement of a test result and the fingerprint of the inputs it ran with.
*[cacheability]: How safe a test is to reuse: hermetic, controlled-environment, external-dependencies or non-deterministic.
*[contract]: aval.yaml: what the test is, how to run it and which files it reads.
*[fingerprint]: One sha256 hash of every input the contract declares.
*[mutable reference]: A tag or range that can point at other content later; pin it or list it in scope.allowMutable.
*[outcome]: The decision on a lookup: verified, miss, verification-failed or error.
*[policy]: Your file that lists trusted signers and their trust levels.
*[scope]: The inputs the contract lists: source files, dependencies and resolved references.
*[signer]: Who signed a result: a CI job (keyless) or a key.
*[trust level]: How much the policy trusts a signer: developer, runner or isolated.
