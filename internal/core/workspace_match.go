package core

import (
	"strings"
	"unicode"
)

// DefaultLabelConventions are the session or tab labels that name an item exactly, as templates
// where {id} stands for the item's id: the bare id, and Lambe's window label "Lambe · <ISSUE>"
// (also written with a colon or a dash). They are data, so a deployment with another naming
// scheme passes its own list instead of changing code.
var DefaultLabelConventions = []string{"{id}", "Lambe · {id}", "Lambe: {id}", "Lambe - {id}"}

// ValidConventions keeps the templates that contain {id}; nil or an empty list gives the defaults.
func ValidConventions(in []string) []string {
	var out []string
	for _, c := range in {
		if c = strings.TrimSpace(c); strings.Contains(c, "{id}") {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return DefaultLabelConventions
	}
	return out
}

func isIDRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' }

func alnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// MatchSessionLabel says how a label names an id: exactly (one of the conventions), by containing
// the id as a whole word, or not at all. An id of one character never matches. A dot followed by a
// letter or digit continues the id (a child id "bd-1.2" is not "bd-1"), while a sentence-ending
// dot does not. Label and id are compared case-insensitively and trimmed; the label is whatever
// the caller has, so match on the full text and clip only what is shown.
func MatchSessionLabel(label, id string, conventions []string) (PlacementMatch, bool) {
	l, want := strings.ToLower(strings.TrimSpace(label)), strings.ToLower(strings.TrimSpace(id))
	if len([]rune(want)) < 2 {
		return "", false
	}
	for _, c := range ValidConventions(conventions) {
		if l == strings.ReplaceAll(strings.ToLower(c), "{id}", want) {
			return MatchExact, true
		}
	}
	for from := 0; ; {
		i := strings.Index(l[from:], want)
		if i < 0 {
			return "", false
		}
		i += from
		end := i + len(want)
		before, after := true, true
		if i > 0 {
			r := []rune(l[:i])
			before = !isIDRune(r[len(r)-1])
		}
		if end < len(l) {
			r := []rune(l[end:])
			after = !isIDRune(r[0])
			if r[0] == '.' && len(r) > 1 && alnum(r[1]) {
				after = false
			}
		}
		if before && after {
			return MatchLabel, true
		}
		from = i + 1
		for from < len(l) && l[from]&0xC0 == 0x80 { // never start inside a rune
			from++
		}
	}
}

// RexBin is the name of the workspace server's command line tool.
const RexBin = "rex"

// HandoffArgv is the command that hands a machine to the terminal workspace: the one source for
// the Queues handoff, the open plan and the demo. bin is the located binary, "" meaning RexBin.
func HandoffArgv(bin, host string) []string {
	if bin == "" {
		bin = RexBin
	}
	return []string{bin, "-S", host}
}
