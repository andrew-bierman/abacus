package rex

import (
	"github.com/andrew-bierman/abacus/internal/core"
)

// session is one Rex session with its text already made safe: the label is chosen by whoever
// created the session (any program in the terminal can), so it is never trusted.
type session struct {
	ID    string
	Label string // what is shown: bounded
	text  string // the whole label, sanitized: what matching looks at, so clipping never hides an id
}

// maxSessions bounds how many sessions of one host are looked at.
const maxSessions = 500

func cleanSession(id, label string) session {
	text := core.Sanitize(label)
	return session{ID: core.ClipRunes(core.Sanitize(id), core.MaxIDRunes), Label: core.ClipRunes(text, core.MaxLabelRunes), text: text}
}

// place finds where an item lives among a host's sessions. One claimant is a placement; two or
// more is an ambiguity with every candidate, never a silent pick; none is neither.
func place(it core.WorkspaceItem, sessions []session, conventions []string) (*core.Placement, *core.Ambiguity) {
	var cands []core.Placement
	for _, s := range sessions {
		m, ok := core.MatchSessionLabel(s.text, it.ID, conventions)
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
