// Package demo provides fixture providers for Tasks, Leases and the Board, so `abacus tui --demo`
// shows every tab without a tracker, a hub or a board. The data is invented. Nothing here runs a
// tool or touches the network or the disk, and a "write" is applied to nothing.
package demo

import (
	"context"
	"fmt"

	"github.com/andrew-bierman/abacus/internal/core"
)

// TasksReport is the demo Tasks summary.
func TasksReport() core.TasksReport {
	return core.TasksReport{Header: core.Header{Source: core.SourceLive}, Counts: core.TaskCounts{Total: 14, Open: 6, InProgress: 3, Blocked: 2, Deferred: 1, Closed: 2, Ready: 3},
		Recent: []core.Task{
			{ID: "ac-12", Title: "wire the lease heartbeat", Status: "in_progress", Priority: 0},
			{ID: "ac-09", Title: "queue attention digest", Status: "open", Priority: 2},
			{ID: "ac-03", Title: "retire the old poller", Status: "closed", Priority: 3}}}
}

// InsightsReport is the demo graph analysis for Tasks.
func InsightsReport() core.InsightsReport {
	return core.InsightsReport{Header: core.Header{Source: core.SourceGraph, State: core.Connected}, TotalActionable: 3, TotalBlocked: 4,
		TopPicks:     []core.Pick{{ID: "ac-12", Title: "wire the lease heartbeat", Unblocks: 2}},
		Blockers:     []core.Pick{{ID: "ac-12", Title: "wire the lease heartbeat", Unblocks: 2}},
		CriticalPath: []core.Ranked{{ID: "ac-12", Value: 3}, {ID: "ac-09", Value: 2}},
		Cycles:       [][]string{{"ac-20", "ac-21", "ac-20"}}}
}

// LeasesReport is the demo lease table.
func LeasesReport() core.LeasesReport {
	return core.LeasesReport{Header: core.Header{Source: core.SourceLive}, Leases: []core.Lease{
		{Resource: "dm-zoj", State: core.LeaseHeld, Holder: "bot-scheduler-01", ExpiresAt: "12m"},
		{Resource: "dm-00h", State: core.LeaseHeld, Holder: "agent-notebook-1", ExpiresAt: "3m"},
		{Resource: "dm-1k2", State: core.LeaseStale, Holder: "claude-mac"},
		{Resource: "ac-9x1", State: core.LeaseFree}}}
}

// BoardReport is the demo message board.
func BoardReport() core.BoardReport {
	return core.BoardReport{Header: core.Header{Source: core.SourceLive},
		Channels: []core.BoardChannel{{Path: "dev/web", Threads: 3}, {Path: "work", Threads: 3}, {Path: "dev", Threads: 1}, {Path: "ops"}},
		Threads: []core.BoardThread{
			{Root: core.BoardMessage{ID: "15", Channel: "dev/web", Author: "ada", At: "2026-11-22T20:12:05Z", Text: "checkout times out for carts over 40 items, started after the 14:02 deploy"}, Replies: 2,
				Last: &core.BoardMessage{ID: "17", Channel: "dev/web", Author: "ada/claude", At: "2026-11-22T20:14:40Z", Text: "rolled back to d4e5f6; watching the error rate"}},
			{Root: core.BoardMessage{ID: "2", Channel: "dev", Author: "ada", At: "2026-11-22T19:01:00Z", Text: "deploys are green again"}}},
		Tasks: []core.BoardTask{
			{ID: "16", Channel: "work", Text: "fix CVE-123 in acme/web", State: "claimed", Claimant: "codex via ada", Until: "2026-11-22T21:09:29Z"},
			{ID: "6", Channel: "work", Text: "review the fix", State: "ready"}}}
}

// module is the part every demo provider shares: it reports as ready, says it is demo data and
// has one passing check.
type module struct{ id, name, version string }

func (m module) ID() string   { return m.id }
func (m module) Name() string { return m.name }
func (m module) Detect(context.Context) core.Detection {
	return core.Detection{State: core.Ready, Version: m.version, Source: core.SourceDemo}
}
func (m module) Doctor(context.Context) []core.Check {
	return []core.Check{{Name: "demo data", Status: core.StatusOK, Detail: "fixtures, nothing contacted"}}
}
func (m module) Status(context.Context) core.Status { return core.Status{Summary: "demo data"} }

// IsDemo marks the module as fixtures, so a front end can keep a demo session away from real state.
func (module) IsDemo() bool { return true }

// Leases provides the Leases domain from fixtures.
type Leases struct {
	module
	WorkspaceProvider // the demo workspace capability rides on the demo Leases provider
}

// NewLeases returns the demo Leases provider.
func NewLeases() Leases { return Leases{module: module{"beads-lease", "Leases (demo)", "demo"}} }

func (Leases) Leases(context.Context) (core.LeasesReport, error) { return LeasesReport(), nil }

// Board provides the Board domain from fixtures.
type Board struct{ module }

// NewBoard returns the demo Board provider.
func NewBoard() Board { return Board{module{"board", "Board (demo)", "demo"}} }

func (Board) Board(context.Context) (core.BoardReport, error) { return BoardReport(), nil }

// Tasks provides the Tasks domain, its graph insights and a write path that changes nothing.
type Tasks struct{ module }

// NewTasks returns the demo Tasks provider.
func NewTasks() Tasks { return Tasks{module{"bd", "Tasks (demo)", "demo"}} }

func (Tasks) Tasks(context.Context) (core.TasksReport, error)       { return TasksReport(), nil }
func (Tasks) Insights(context.Context) (core.InsightsReport, error) { return InsightsReport(), nil }

// BuildTaskArgv shows the command a real change would run, so the confirm screen reads true.
func (Tasks) BuildTaskArgv(ch core.TaskChange) ([]string, error) {
	a := []string{"bd", string(ch.Op)}
	if ch.ID != "" {
		a = append(a, ch.ID)
	}
	if ch.Title != "" {
		a = append(a, "--title="+ch.Title)
	}
	if ch.Priority != nil {
		a = append(a, fmt.Sprintf("--priority=%d", *ch.Priority))
	}
	if ch.Status != "" {
		a = append(a, "--status="+ch.Status)
	}
	return a, nil
}

// RunTask applies the change to nothing and reports a made-up id.
func (Tasks) RunTask(context.Context, []string) (core.TaskResult, error) {
	return core.TaskResult{ID: "demo-1", Title: "demo change (nothing was written)"}, nil
}

func (Tasks) TrackerDir() string { return "" }

// ReadTask re-reads a fixture task, so a confirm screen can check it has not changed.
func (Tasks) ReadTask(_ context.Context, id string) (core.Task, error) {
	for _, t := range TasksReport().Recent {
		if t.ID == id {
			return t, nil
		}
	}
	return core.Task{}, fmt.Errorf("no such issue %s", id)
}
