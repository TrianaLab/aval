package architecture

import (
	"strings"
	"testing"
)

// The brand SVGs stay hand-written vectors (#72): a viewBox so they scale, a
// <title> for screen readers, and no embedded raster.
func TestBrandSVGs(t *testing.T) {
	svgs := trackedFiles(t, "docs/*.svg")
	if len(svgs) == 0 {
		t.Fatal("no SVG files under docs/")
	}
	for _, f := range svgs {
		s := readFile(t, f)
		for _, want := range []string{"viewBox=", "<title>"} {
			if !strings.Contains(s, want) {
				t.Errorf("%s: missing %s", f, want)
			}
		}
		for _, bad := range []string{"<image", "data:"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s: embeds a raster (%s)", f, bad)
			}
		}
	}
}
