// Package envconst is a fixture: the policy variable read through the
// resolver's constant outside the resolver.
package envconst

import "os"

const policyEnv = "AVAL_" + "POLICY"

// Policy bypasses the one resolver.
func Policy() string { return os.Getenv(policyEnv) }
