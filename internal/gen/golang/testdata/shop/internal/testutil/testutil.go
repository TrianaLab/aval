// Package testutil holds test helpers.
package testutil

import (
	"io/fs"
	"os"
	"strings"
	"testing"
)

// Read returns the trimmed content of file in fsys. The caller picks the
// directory (os.DirFS), so aval gen go sees what it reads.
func Read(t *testing.T, fsys fs.FS, file string) string {
	data, err := fs.ReadFile(fsys, file)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// Load returns the trimmed content of file, a path its caller passes: aval
// gen go adds what the caller passes, where the caller is.
func Load(t *testing.T, file string) string {
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// TempDir returns a new temporary directory.
func TempDir(t *testing.T) string { return t.TempDir() }

// Equal fails t when got is not want.
func Equal(t *testing.T, got, want string) {
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
