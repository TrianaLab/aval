// Package env is a fixture: the policy variable read outside the resolver.
package env

import "os"

// Policy bypasses the one resolver.
func Policy() string { return os.Getenv("AVAL_POLICY") }
