package tui

import (
	"math"
	"strconv"
	"testing"
)

func luminance(hex string) float64 {
	c := func(i int) float64 {
		v, _ := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		f := float64(v) / 255
		if f <= 0.03928 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	return 0.2126*c(0) + 0.7152*c(1) + 0.0722*c(2)
}

func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

// The cursor row is a full-row fill that stands out from the page and keeps its text readable, on
// both backgrounds (text colors are the terminal's own default foregrounds).
func TestCursorFillStandsOutAndStaysReadable(t *testing.T) {
	for name, c := range map[string]struct{ fill, page, text string }{
		"dark":  {colSelBG.Dark.TrueColor, "#0d1117", "#c9d1d9"},
		"light": {colSelBG.Light.TrueColor, "#ffffff", "#1f2328"},
	} {
		if r := contrast(c.fill, c.page); r < 1.6 {
			t.Errorf("%s: the cursor fill barely differs from the page (contrast %.2f)", name, r)
		}
		if r := contrast(c.fill, c.text); r < 4.5 {
			t.Errorf("%s: text on the cursor fill is hard to read (contrast %.2f)", name, r)
		}
	}
}
