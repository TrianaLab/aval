package greet

import "testing"

func TestHello(t *testing.T) {
	if got := Hello("gopher"); got != "hello, gopher" {
		t.Fatalf("Hello: %q", got)
	}
}
