// Package contract is a fixture: aval.yaml names the trust policy.
package contract

// Contract reads the policy from the tree under test.
type Contract struct {
	Name   string `yaml:"name"`
	Policy string `yaml:"policy,omitempty"`
}
