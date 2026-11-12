// Package testutil holds test helpers.
package testutil

import (
	"io/fs"
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

// Equal fails t when got is not want.
func Equal(t *testing.T, got, want string) {
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
