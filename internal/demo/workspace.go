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

// demoSessions are the sessions the demo "server" has, matched to items by the same core matcher
// the real adapter uses, so the demo shows exactly what a real read would. Two sessions claim
// "dm-1k2" so the ambiguous case is visible, "T-3" is found only by a word in a label, and "T-2"
// and the rest have no session.
var demoSessions = []struct{ id, label string }{
	{"demo:s1", "Lambe · T-1"},
	{"demo:s3", "review T-3 notes"},
	{"demo:s4", "Lambe · dm-zoj"},
	{"demo:s5", "dm-00h"},
	{"demo:s6", "Lambe · dm-1k2"},
	{"demo:s7", "retry dm-1k2"},
	{"demo:s8", "Lambe · ac-09"},
}

// Workspace places the demo items it knows.
func (WorkspaceProvider) Workspace(_ context.Context, items []core.WorkspaceItem) (core.WorkspaceReport, error) {
	rep := core.WorkspaceReport{State: core.Connected}
	for _, it := range items {
		var cands []core.Placement
		for _, s := range demoSessions {
			if m, ok := core.MatchSessionLabel(s.label, it.ID, nil); ok {
				cands = append(cands, core.Placement{ID: core.PlacementID(it, it.Host, s.id), Item: it, Host: it.Host, Session: s.id, Label: s.label, Match: m})
			}
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
// would show: the handoff to the machine, or the bare command for a session on this one.
func (WorkspaceProvider) Open(p core.Placement) core.OpenPlan {
	argv := []string{core.RexBin}
	if p.Host != "" {
		argv = core.HandoffArgv("", p.Host)
	}
	return core.OpenPlan{Rungs: []core.OpenRung{}, Copy: core.ShellJoin(argv), Note: "Demo data: nothing is run."}
}
