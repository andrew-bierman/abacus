package core

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Sanitize makes text from outside abacus (tracker titles, daemon messages, tool output)
// safe to print: tab, newline and carriage return become spaces and every other control
// rune, including C1 controls such as CSI, is dropped, so a hostile title cannot move the
// cursor, rewrite the screen or set the terminal title.
func Sanitize(s string) string {
	// Fast path for the common case: valid UTF-8 with no control runes is returned as is.
	if utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			b.WriteByte(' ')
		case unicode.IsControl(r):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
