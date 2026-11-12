package web

import (
	"os"
	"testing"
)

func TestPages(t *testing.T) {
	if _, err := os.ReadDir("../fixtures/pages"); err != nil {
		t.Fatal(err)
	}
}
