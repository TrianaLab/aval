package money

import "testing"

func TestFormat(t *testing.T) {
	if Format(150) != "1.50" {
		t.Fatal(Format(150))
	}
}
