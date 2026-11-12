// Package testutil holds test helpers.
package testutil

import (
	"os"
	"strings"
	"testing"
)

// Read returns the trimmed content of file.
func Read(t *testing.T, file string) string {
	data, err := os.ReadFile(file)
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
