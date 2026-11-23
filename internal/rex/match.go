package rex

import (
	"strings"
	"unicode"

	"github.com/andrew-bierman/abacus/internal/core"
)

// session is one Rex session with its text already made safe: the label is chosen by whoever
// created the session (any program in the terminal can), so it is never trusted.
type session struct {
	ID    string
	Label string
}

// maxSessions bounds how many sessions of one host are looked at.
const maxSessions = 500

func cleanSession(id, label string) session {
	return session{ID: core.ClipRunes(core.Sanitize(id), core.MaxIDRunes), Label: core.ClipRunes(core.Sanitize(label), core.MaxLabelRunes)}
}

// exactForms are the labels that are exactly the naming convention for an item: the bare id, or
// Lambe's window label "Lambe · <ISSUE>" (also written with a colon or dash).
func exactForms(id string) []string {
	id = strings.ToLower(id)
	return []string{id, "lambe · " + id, "lambe: " + id, "lambe - " + id}
}

func isIDRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' }

// matchLabel says how a label names an id: exactly (the convention), by containing the id as a
// whole word, or not at all. An id of one character never matches.
func matchLabel(label, id string) (core.PlacementMatch, bool) {
	l, want := strings.ToLower(strings.TrimSpace(label)), strings.ToLower(strings.TrimSpace(id))
	if len([]rune(want)) < 2 {
		return "", false
	}
	for _, f := range exactForms(want) {
		if l == f {
			return core.MatchExact, true
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
		}
		if before && after {
			return core.MatchLabel, true
		}
		from = i + 1
		for from < len(l) && !isRuneStart(l[from]) {
			from++
		}
	}
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// place finds where an item lives among a host's sessions. One claimant is a placement; two or
// more is an ambiguity with every candidate, never a silent pick; none is neither.
func place(it core.WorkspaceItem, sessions []session) (*core.Placement, *core.Ambiguity) {
	var cands []core.Placement
	for _, s := range sessions {
		m, ok := matchLabel(s.Label, it.ID)
		if !ok {
			continue
		}
		cands = append(cands, core.Placement{ID: core.PlacementID(it, it.Host, s.ID), Item: it, Host: it.Host, Session: s.ID, Label: s.Label, Match: m})
	}
	switch len(cands) {
	case 0:
		return nil, nil
	case 1:
		return &cands[0], nil
	}
	return nil, &core.Ambiguity{Item: it, Candidates: cands}
}
