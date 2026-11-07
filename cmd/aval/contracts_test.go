package main

import (
	"bytes"
	"testing"

	"github.com/TrianaLab/aval/internal/fingerprint"
)

// --explain prints the scope.contracts tree after the manifest.
func TestExplainPrintsContractsTree(t *testing.T) {
	dep := func(p, name, digest string, sub ...fingerprint.Dependency) fingerprint.Dependency {
		return fingerprint.Dependency{Path: p, Name: name, Fingerprint: fingerprint.Fingerprint{Digest: digest, Contracts: sub}}
	}
	fp := fingerprint.Fingerprint{Manifest: "m\n", Contracts: []fingerprint.Dependency{
		dep("../backend/aval.yaml", "backend", "sha256:b", dep("../shared/aval.yaml", "shared", "sha256:s")),
		dep("../docs/aval.yaml", "docs", "sha256:d"),
	}}
	var out, warn bytes.Buffer
	if err := printFingerprint(&out, &warn, fp, nil, runFlags{explain: true}); err != nil {
		t.Fatal(err)
	}
	want := "m\n" +
		"depends on ../backend/aval.yaml (backend) sha256:b\n" +
		"  depends on ../shared/aval.yaml (shared) sha256:s\n" +
		"depends on ../docs/aval.yaml (docs) sha256:d\n"
	if out.String() != want {
		t.Fatalf("stdout %q, want %q", out.String(), want)
	}
}
