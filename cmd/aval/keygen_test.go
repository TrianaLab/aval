package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/pkg/evidence"
)

func TestKeygen(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	code, out, errOut := runCLI("keygen")
	if code != ExitOK || errOut != "" {
		t.Fatalf("keygen: exit %d, stderr %q", code, errOut)
	}
	pub, _ := os.ReadFile(filepath.Join(dir, "aval.pub"))
	kid, err := evidence.PublicKeyID(string(pub))
	if err != nil {
		t.Fatal(err)
	}
	want := "wrote aval.key (private: keep it secret, do not commit it) and aval.pub\n" +
		"key id: " + kid + "\n" +
		"next: trust aval.pub in your policy (publicKey: aval.pub) and sign with --key aval.key\n"
	if out != want {
		t.Fatalf("stdout:\n%s\nwant:\n%s", out, want)
	}
	// The key it wrote passes the run and push key check.
	if err := checkSigner("aval.key", "", false); err != nil {
		t.Fatal(err)
	}

	// No overwrite without --force: exit 1, files kept.
	key, _ := os.ReadFile("aval.key")
	code, out, errOut = runCLI("keygen", "aval")
	if code != ExitError || out != "" || !strings.Contains(errOut, "aval.key already exists; pass --force to replace it") {
		t.Fatalf("second keygen: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	if again, _ := os.ReadFile("aval.key"); string(again) != string(key) {
		t.Fatal("aval.key overwritten without --force")
	}
	if code, _, errOut = runCLI("keygen", "--force"); code != ExitOK {
		t.Fatalf("keygen --force: exit %d, %s", code, errOut)
	}
	if again, _ := os.ReadFile("aval.key"); string(again) == string(key) {
		t.Fatal("--force did not replace aval.key")
	}
}

// A name, a trailing .key, an empty name and too many arguments.
func TestKeygenNames(t *testing.T) {
	t.Chdir(t.TempDir())
	code, out, _ := runCLI("keygen", "ci")
	if code != ExitOK || !strings.HasPrefix(out, "wrote ci.key ") {
		t.Fatalf("keygen ci: exit %d, %q", code, out)
	}
	if code, out, _ = runCLI("keygen", "ci2.key"); code != ExitOK || !strings.HasPrefix(out, "wrote ci2.key ") {
		t.Fatalf("keygen ci2.key: exit %d, %q", code, out)
	}
	for _, empty := range []string{"", ".key"} {
		if code, _, _ = runCLI("keygen", empty); code != ExitUsage {
			t.Fatalf("keygen %q: exit %d, want %d", empty, code, ExitUsage)
		}
	}
	if code, _, _ = runCLI("keygen", "a", "b"); code != ExitUsage {
		t.Fatalf("two names: exit %d, want %d", code, ExitUsage)
	}
}

func TestKeyHintsPointToKeygen(t *testing.T) {
	err := checkSigner(filepath.Join(t.TempDir(), "missing.key"), "", false)
	if err == nil || !strings.Contains(err.Error(), "aval keygen") {
		t.Fatalf("bad key: %v", err)
	}
	if err := checkSigner("", "", false); err == nil || !strings.Contains(err.Error(), "aval keygen") {
		t.Fatalf("no key: %v", err)
	}
}
