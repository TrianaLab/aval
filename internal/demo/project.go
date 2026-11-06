package demo

// project is the demo project: a Go module with one test, its contract, the
// trust policy and a README outside the scope. throwaway.pub is added by
// reset. tests/acceptance runs the real CLI over these files.
var project = map[string]string{
	"aval.yaml": `apiVersion: aval.trianalab.io/v1
kind: Test
metadata:
  name: greet
runner:
  command: ["go", "test", "./..."]
scope:
  source: ["**/*.go"]
  dependencies: [go.mod]
environment:
  toolchain: {go: "1.27"}
cacheability: hermetic
`,
	"go.mod": `module example.com/greet

go 1.27
`,
	"greet.go": `package greet

// Hello greets name.
func Hello(name string) string { return "Hello, " + name + "!" }
`,
	"greet_test.go": `package greet

import "testing"

func TestHello(t *testing.T) {
	if got := Hello("Aval"); got != "Hello, Aval!" {
		t.Fatalf("Hello(%q) = %q", "Aval", got)
	}
}
`,
	"README.md": `# greet

A tiny Go module. aval.yaml declares its inputs: every .go file and go.mod.
This README is not one of them, so editing it keeps the fingerprint.
`,
	policyFile: `apiVersion: aval.trianalab.io/v1
kind: Policy
minTrustLevel: developer
identities:
  - publicKey: throwaway.pub
    trustLevel: developer
`,
}
