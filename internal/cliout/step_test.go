package cliout

import (
	"strings"
	"testing"
)

// Step colours ✓ green and · dim, and writes no ESC byte without colour.
func TestStep(t *testing.T) {
	var b strings.Builder
	Step(&b, true, GlyphDone, "wrote a")
	Step(&b, true, GlyphNeutral, "note")
	Step(&b, false, GlyphDone, "plain")
	want := sgrGreen + "✓ wrote a" + sgrReset + "\n" + sgrDim + "· note" + sgrReset + "\n✓ plain\n"
	if b.String() != want {
		t.Fatalf("%q, want %q", b.String(), want)
	}
}
