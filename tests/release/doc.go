// Package release holds level 5 tests: the release produces what it claims.
// They read GoReleaser's dist/ (AVAL_RELEASE_DIST): `make test-release`
// builds a snapshot dry-run first; the release workflow points them at the
// tagged build (AVAL_RELEASE_TAG) before anything is published.
package release
