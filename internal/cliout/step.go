package cliout

import "io"

// The style A glyphs other commands print with Step.
const (
	GlyphDone    = glyphDone
	GlyphNeutral = glyphNeutral
)

// stepSGR is each Step glyph's colour: ✓ green, · dimmed, as in a run.
var stepSGR = map[string]string{glyphDone: sgrGreen, glyphNeutral: sgrDim}

// Step writes one styled step line outside a run (aval init): glyph and
// text, coloured when color is set.
func Step(w io.Writer, color bool, glyph, text string) {
	Run{Out: w, Color: color}.step(stepSGR[glyph], glyph, text)
}
