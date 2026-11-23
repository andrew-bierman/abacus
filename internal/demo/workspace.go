package demo

import (
	"context"

	"github.com/andrew-bierman/abacus/internal/core"
)

// WorkspaceProvider is the demo workspace provider: it places a few of the demo runs and leases as
// terminal sessions so a front end can show the layer without any terminal workspace. It
// contacts nothing and its open plan has no command to run, only the command to show.
//
// It is part of the demo Leases provider (which therefore also provides the workspace
// capability), and NewWorkspace returns it on its own for tests.
type WorkspaceProvider struct{}

// NewWorkspace returns the demo workspace provider.
func NewWorkspace() WorkspaceProvider { return WorkspaceProvider{} }

// demoSessions says which demo item lives where. Two sessions claim "ac-1k2" so the ambiguous
// case is visible too; "T-2" and the rest have no session.
var demoSessions = map[string][]struct{ session, label string }{
	"T-1":    {{"demo:s1", "Lambe · T-1"}},
	"T-3":    {{"demo:s3", "review T-3 notes"}},
	"ac-zoj": {{"demo:s4", "Lambe · ac-zoj"}},
	"ac-00h": {{"demo:s5", "ac-00h"}},
	"ac-1k2": {{"demo:s6", "Lambe · ac-1k2"}, {"demo:s7", "retry ac-1k2"}},
}

// Workspace places the demo items it knows.
func (WorkspaceProvider) Workspace(_ context.Context, items []core.WorkspaceItem) (core.WorkspaceReport, error) {
	rep := core.WorkspaceReport{State: core.Connected}
	for _, it := range items {
		var cands []core.Placement
		for _, s := range demoSessions[it.ID] {
			m := core.MatchLabel
			if s.label == it.ID || s.label == "Lambe · "+it.ID {
				m = core.MatchExact
			}
			cands = append(cands, core.Placement{ID: core.PlacementID(it, it.Host, s.session), Item: it, Host: it.Host, Session: s.session, Label: s.label, Match: m})
		}
		switch len(cands) {
		case 0:
		case 1:
			rep.Placements = append(rep.Placements, cands[0])
		default:
			rep.Ambiguous = append(rep.Ambiguous, core.Ambiguity{Item: it, Candidates: cands})
		}
	}
	return rep, nil
}

// Open is a plan with no rung: a demo never runs a command. The copy line is what a real one
// would show.
func (WorkspaceProvider) Open(p core.Placement) core.OpenPlan {
	plan := core.OpenPlan{Rungs: []core.OpenRung{}, Note: "Demo data: nothing is run."}
	if p.Host != "" {
		plan.Copy = core.ShellJoin([]string{"rex", "-S", p.Host})
	}
	return plan
}
