package core

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// invisible reports bidi overrides/isolates and zero-width characters (including ZWJ, so a
// ZWJ emoji sequence degrades to its parts): they can reorder or hide text on screen.
func invisible(r rune) bool {
	switch {
	case r >= 0x200b && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0xfeff:
		return true
	}
	return false
}

// Sanitize makes text from outside abacus (tracker titles, daemon messages, tool output)
// safe to print: tab, newline and carriage return become spaces and every other control
// rune, including C1 controls such as CSI, is dropped (as are bidi and zero-width characters), so a hostile title cannot move the
// cursor, rewrite the screen or set the terminal title.
func Sanitize(s string) string {
	// Fast path for the common case: valid UTF-8 with no control runes is returned as is.
	if utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || invisible(r) }) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			b.WriteByte(' ')
		case unicode.IsControl(r), invisible(r):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
