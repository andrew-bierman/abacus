package core

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// invisible reports characters that print as nothing or reorder what is around them: every
// format character (Unicode category Cf: bidi overrides and isolates, zero-width space, joiners
// including ZWJ so a ZWJ emoji sequence degrades to its parts, word joiner, soft hyphen, BOM,
// tag characters) plus the combining grapheme joiner and the Hangul fillers that render blank.
func invisible(r rune) bool {
	switch r {
	case 0x034f, 0x115f, 0x1160, 0x3164, 0xffa0:
		return true
	}
	return unicode.Is(unicode.Cf, r)
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

// SanitizeText is Sanitize for stored message text: line breaks are kept (CRLF and CR become
// LF) so the data keeps its shape, tabs become spaces, and every other control and invisible
// rune is dropped. Renderers flatten or wrap per line.
func SanitizeText(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = Sanitize(l)
	}
	return strings.Join(lines, "\n")
}
