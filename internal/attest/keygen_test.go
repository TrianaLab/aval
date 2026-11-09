package attest

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TrianaLab/aval/pkg/evidence"
)

func TestWriteKeyPair(t *testing.T) {
	name := filepath.Join(t.TempDir(), "dev")
	kid, err := WriteKeyPair(name, false)
	if err != nil {
		t.Fatal(err)
	}
	for f, mode := range map[string]os.FileMode{name + ".key": 0o600, name + ".pub": 0o644} {
		st, err := os.Stat(f)
		if err != nil || st.Mode().Perm() != mode {
			t.Fatalf("%s: mode %v, want %v (%v)", f, st.Mode().Perm(), mode, err)
		}
	}
	k, err := loadKey(name + ".key")
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := os.ReadFile(name + ".pub")
	if pkid, _ := evidence.PublicKeyID(string(pub)); string(k.hint) != kid || pkid != kid {
		t.Fatalf("key id %q, signing hint %q, public key id %q", kid, k.hint, pkid)
	}
	key, _ := os.ReadFile(name + ".key")
	if !strings.HasPrefix(string(key), "-----BEGIN PRIVATE KEY-----\n") {
		t.Fatalf("want an unencrypted PKCS#8 PEM, got %q", key)
	}

	// --force replaces both, and a loosened mode does not survive.
	if err := os.Chmod(name+".key", 0o644); err != nil {
		t.Fatal(err)
	}
	kid2, err := WriteKeyPair(name, true)
	if err != nil || kid2 == kid {
		t.Fatalf("force: %q %v", kid2, err)
	}
	if st, _ := os.Stat(name + ".key"); st.Mode().Perm() != 0o600 {
		t.Fatalf("force: mode %v", st.Mode().Perm())
	}
}

// Either file existing stops it before anything is written.
func TestWriteKeyPairRefuses(t *testing.T) {
	for _, f := range []string{".key", ".pub"} {
		other := filepath.Join(t.TempDir(), "k")
		if err := os.WriteFile(other+f, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := WriteKeyPair(other, false); err == nil || !strings.Contains(err.Error(), "--force") {
			t.Fatalf("existing %s: want a --force error, got %v", f, err)
		}
		if b, _ := os.ReadFile(other + f); string(b) != "keep" {
			t.Fatalf("existing %s overwritten", f)
		}
		if _, err := os.Stat(other + map[string]string{".key": ".pub", ".pub": ".key"}[f]); !os.IsNotExist(err) {
			t.Fatalf("existing %s: the other file was written", f)
		}
	}
}

func TestWriteKeyPairErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteKeyPair(filepath.Join(dir, "missing", "k"), false); err == nil {
		t.Fatal("want an error for a missing directory")
	}
	// --force refuses a directory in the way before removing the old key.
	if err := os.MkdirAll(filepath.Join(dir, "d.pub", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "d.key"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteKeyPair(filepath.Join(dir, "d"), true); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("want a directory error, got %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "d.key")); string(b) != "old" {
		t.Fatal("old d.key lost although d.pub could not be replaced")
	}
	// --force cannot remove a file in a read-only directory.
	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ro, "r.key"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
	if _, err := WriteKeyPair(filepath.Join(ro, "r"), true); err == nil {
		t.Fatal("want an error when --force cannot remove the old file")
	}
	// A parent that cannot be searched is not reported as "already exists".
	if err := os.Chmod(ro, 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteKeyPair(filepath.Join(ro, "r"), false); err == nil || strings.Contains(err.Error(), "already exists") {
		t.Fatalf("unreadable parent: got %v", err)
	}
}

// A file planted after the check is refused, not written through, and the
// name.key already written is removed so a rerun is not blocked.
func TestWriteKeyPairPlantedFile(t *testing.T) {
	name := filepath.Join(t.TempDir(), "p")
	real := openNew
	t.Cleanup(func() { openNew = real })
	openNew = func(path string, mode os.FileMode) (*os.File, error) {
		if strings.HasSuffix(path, ".pub") {
			if err := os.WriteFile(path, []byte("planted"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return real(path, mode)
	}
	if _, err := WriteKeyPair(name, false); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("want ErrExist, got %v", err)
	}
	if b, _ := os.ReadFile(name + ".pub"); string(b) != "planted" {
		t.Fatalf("planted file overwritten: %q", b)
	}
	if _, err := os.Stat(name + ".key"); !os.IsNotExist(err) {
		t.Fatal("orphan p.key left behind")
	}
}
