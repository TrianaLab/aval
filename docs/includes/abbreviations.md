*[attestation]: A signed statement of a test result and the fingerprint of the inputs it ran with.
*[cacheability]: How safe a test is to reuse: hermetic, controlled-environment, external-dependencies or non-deterministic.
*[contract]: aval.yaml: what the test is, how to run it and which files it reads.
*[fingerprint]: One sha256 hash of every input the contract declares.
*[outcome]: The decision on a lookup: verified, miss, verification-failed or error.
*[policy]: Your file that lists which signers you trust, and how much.
*[scope]: The files and dependencies the contract lists as test inputs.
*[signer]: The identity behind a signature: a CI workload or a key.
*[trust level]: How much the policy trusts a signer: developer, runner or isolated.
