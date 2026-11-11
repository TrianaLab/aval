//go:build unix

package fingerprint

import (
	"strings"
	"syscall"
	"testing"
)

// TestPathsFIFO: a FIFO in a declared directory fails without being
// opened (opening one blocks until a writer comes).
func TestPathsFIFO(t *testing.T) {
	f := newPathsFixture(t)
	f.do(syscall.Mkfifo(f.at(".config/app/fifo"), 0o644))
	if _, err := f.compute(); err == nil || !strings.Contains(err.Error(), "environment.paths ~/.config/app: fifo: only regular files") {
		t.Fatalf("want a fail-closed error, got %v", err)
	}
	f.c.Environment.Paths = []string{"~/.config/app/fifo"}
	if _, err := f.compute(); err == nil || !strings.Contains(err.Error(), "only regular files") {
		t.Fatalf("want a fail-closed error at the entry, got %v", err)
	}
}
