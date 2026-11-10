package version

import "testing"

func TestString(t *testing.T) {
	if got := String(); got != "vdev" {
		t.Fatalf("String: %q", got)
	}
}
