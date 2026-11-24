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

// block is one terminal pane with its label made safe, like a session.
type block struct {
	ID   string
	text string
}

// maxBlockSessions bounds how many sessions of one host have their panes read, and maxBlocks how
// many panes of one session are looked at.
const (
	maxBlockSessions = 24
	maxBlocks        = 200
)

// place finds where an item lives among a host's sessions: a session named for it, or a pane named
// for it (a pane opened for the item carries its id as its label). A pane is the more exact claim,
// so a session that holds one is reported with that pane and its own name is not a second claim.
// One claimant is a placement; two or more is an ambiguity with every candidate, never a silent
// pick; none is neither.
func place(it core.WorkspaceItem, sessions []session, blocks map[string][]block, conventions []string) (*core.Placement, *core.Ambiguity) {
	var cands []core.Placement
	for _, s := range sessions {
		found := false
		for _, b := range blocks[s.ID] {
			m, ok := core.MatchSessionLabel(b.text, it.ID, conventions)
			if !ok {
				continue
			}
			found = true
			cands = append(cands, core.Placement{ID: core.PlacementID(it, it.Host, s.ID+"/"+b.ID), Item: it, Host: it.Host, Session: s.ID, Label: s.Label, Block: b.ID, Match: m})
		}
		if found {
			continue
		}
		if m, ok := core.MatchSessionLabel(s.text, it.ID, conventions); ok {
			cands = append(cands, core.Placement{ID: core.PlacementID(it, it.Host, s.ID), Item: it, Host: it.Host, Session: s.ID, Label: s.Label, Match: m})
		}
	}
	switch len(cands) {
	case 0:
		return nil, nil
	case 1:
		return &cands[0], nil
	}
	return nil, &core.Ambiguity{Item: it, Candidates: cands}
}
