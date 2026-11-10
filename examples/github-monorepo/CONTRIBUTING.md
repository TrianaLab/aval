# Contributing

Run the suites with `make test`, `make ui`, `make e2e SHARD=<1|2|3>` and
`make test-external`.

CI runs every suite except `test-external` through Aval: a job reuses a
signed result for the same inputs instead of running the suite. The tests
and their inputs are in `aval.yaml`; a pull request that changes an input
runs the tests that read it.
