package tui

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// The optional workspace layer in the TUI. Where a terminal workspace is connected (or in the demo)
// the Queues, Leases and Tasks screens show where the selected item lives, and one key goes there.
// Core reads the placements and builds the open plan; this file only draws them and, on an explicit
// keypress, hands the terminal to the first step of a plan core produced. Nothing is read or run
// without a workspace provider, and a demo session never runs anything.

// keyWorkspace is the jump key on Leases and Tasks (Queues keeps its `o`). It is inactive, and so
// absent from the footer, unless the selected row has a placement to show or open.
var keyWorkspace = key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "workspace"))

// workspaceMsg is the answer to one workspace read; tag says which section asked, because the
// shell broadcasts every message to every section.
type workspaceMsg struct {
	tag string
	rep core.WorkspaceReport
}

// wsLayer is one section's view of the workspace. A nil *wsLayer is a section with no layer (every
// method is then a no-op), which is how the unit tests build sections and how a session without a
// provider behaves.
type wsLayer struct {
	tag     string
	prov    core.WorkspaceProvider // nil: nothing to read
	demo    bool                   // fixtures: show the command, never run it
	handoff func([]string) tea.Cmd // hands the terminal to argv; the same runner `o` uses
	rep     *core.WorkspaceReport  // nil: no workspace data, so nothing is shown anywhere
	busy    bool
	pending bool     // this layer started a handoff, so its done message is ours
	note    []string // the framed notice, shown until the next key
}

func newWsLayer(tag string, prov core.WorkspaceProvider, demo bool, handoff func([]string) tea.Cmd) *wsLayer {
	return &wsLayer{tag: tag, prov: prov, demo: demo, handoff: handoff}
}

// workspaceHost is implemented by the sections that can show the layer.
type workspaceHost interface{ setWorkspace(*wsLayer) }

// has reports whether there is workspace data to show.
func (w *wsLayer) has() bool { return w != nil && w.rep != nil }

func (w *wsLayer) isDemo() bool { return w != nil && w.demo }

// itemOf builds the item a row is asked about. Provider text is cleaned the way core cleans the
// report, so the two compare equal; a machine name that cleaning would change is never sent on
// (ok is false), because clipping it could turn one machine into another.
func itemOf(kind core.WorkspaceKind, id, host string) (core.WorkspaceItem, bool) {
	if id == "" || core.Sanitize(host) != host || len([]rune(host)) > core.MaxHostRunes {
		return core.WorkspaceItem{}, false
	}
	return core.WorkspaceItem{Kind: kind, ID: core.ClipRunes(core.Sanitize(id), core.MaxIDRunes), Host: host}, true
}

// itemList is the distinct items to ask about, in order.
func itemList(items ...core.WorkspaceItem) []core.WorkspaceItem {
	var out []core.WorkspaceItem
	seen := map[core.WorkspaceItem]bool{}
	for _, it := range items {
		if it.ID != "" && !seen[it] {
			seen[it] = true
			out = append(out, it)
		}
	}
	return out
}

// fetch reads where items live, once at a time. It returns nil without a provider.
func (w *wsLayer) fetch(items []core.WorkspaceItem) tea.Cmd {
	if w == nil || w.prov == nil || w.busy || len(items) == 0 {
		return nil
	}
	w.busy = true
	prov, tag := w.prov, w.tag
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		return workspaceMsg{tag: tag, rep: core.ReadWorkspaceFrom(ctx, prov, items)}
	}
}

// receive takes a read's answer. An empty report (nothing connected, nothing placed) is no data.
func (w *wsLayer) receive(m workspaceMsg) {
	if w == nil || m.tag != w.tag {
		return
	}
	w.busy = false
	if m.rep.Empty() {
		w.rep = nil
		return
	}
	r := m.rep
	w.rep = &r
}

// find is where an item lives: its placement, or the candidates when more than one place claims
// it. Neither is set for an item the workspace knows nothing about.
func (w *wsLayer) find(it core.WorkspaceItem) (p *core.Placement, cands []core.Placement, more bool) {
	if !w.has() {
		return nil, nil, false
	}
	p, cands = w.rep.For(it)
	if p == nil {
		for _, a := range w.rep.Ambiguous {
			if a.Item == it {
				more = a.Truncated
				break
			}
		}
	}
	return p, cands, more
}

// knows reports whether the workspace has something to show or open for the item.
func (w *wsLayer) knows(it core.WorkspaceItem, ok bool) bool {
	p, c, _ := w.find(it)
	return ok && (p != nil || len(c) > 0)
}

// matchWord is a placement's match in neutral words; an unknown value from a newer provider is
// shown as it came (sanitized).
func matchWord(m core.PlacementMatch) string {
	switch m {
	case core.MatchExact:
		return "exact"
	case core.MatchLabel:
		return "by label"
	case core.MatchHostNotConfigured:
		return "machine not configured"
	}
	return orDash(string(m))
}

// where is the short text of a placement for a list cell.
func where(p *core.Placement) string {
	if p.Label != "" {
		return san(p.Label)
	}
	return orDash(p.Session)
}

// place is the "where" cell of a row: the session, "ambiguous", or a dash for an unplaced row.
func (w *wsLayer) place(th Theme, it core.WorkspaceItem, ok, selected bool) string {
	p, c, _ := w.find(it)
	switch {
	case !ok:
		return "-"
	case p != nil:
		return where(p)
	case len(c) > 0:
		if selected {
			return "ambiguous"
		}
		return th.Text(KindWarn, "ambiguous")
	}
	return "-"
}

// detail is the placement block for a row's detail panel, with its labels padded to pad. It is
// empty without workspace data, so a screen with no workspace is exactly what it was.
func (w *wsLayer) detail(th Theme, it core.WorkspaceItem, ok bool, pad int) []string {
	if !w.has() {
		return nil
	}
	field := func(label, v string) string { return th.Label.Render(padTo(label, pad)) + v }
	out := []string{""}
	p, cands, more := w.find(it)
	switch {
	case !ok || (p == nil && len(cands) == 0):
		out = append(out, field("where", th.Text(KindMuted, "not placed")))
	case p != nil:
		out = append(out, field("where", where(p)))
		if p.Host != "" {
			out = append(out, field("host", san(p.Host)))
		}
		if p.Session != "" && p.Session != p.Label {
			out = append(out, field("session", san(p.Session)))
		}
		if p.Tab != "" {
			out = append(out, field("tab", san(p.Tab)))
		}
		kind := KindMuted
		if p.Match == core.MatchHostNotConfigured {
			kind = KindWarn
		}
		out = append(out, field("match", th.Text(kind, matchWord(p.Match))))
	default:
		out = append(out, field("where", th.Text(KindWarn, fmt.Sprintf("ambiguous: %d claim it", len(cands)))))
		for _, c := range cands {
			line := where(&c)
			if c.Host != "" {
				line += " @ " + san(c.Host)
			}
			out = append(out, "  "+line)
		}
		if more {
			out = append(out, "  "+th.Text(KindMuted, "and more, not all are listed"))
		}
	}
	if r := w.rep; r.State == core.Degraded && r.Reason != "" {
		out = append(out, th.Text(KindWarn, "workspace: "+san(r.Reason)))
	}
	if w.rep.Stale {
		out = append(out, th.Text(KindMuted, "placement from a recent cached read"))
	}
	if w.rep.Truncated {
		out = append(out, th.Text(KindWarn, "some placements were left out"))
	}
	return out
}

// whereCol is the aligned "where" column of a list: it is offered only on an ultra-wide terminal
// with workspace data, and only when it fits without dropping a column or narrowing an identifying
// one (priority 0 and 1). rows are the table's rows without the column and cells its cells; when
// it fits the table with the extra last column comes back, and the caller appends the cells.
func (w *wsLayer) whereCol(tb table, width, iw int, rows [][]string, cells []string) (table, bool) {
	if !w.has() || width < bpUltra || len(rows) != len(cells) || len(rows) == 0 {
		return tb, false
	}
	inner := max(iw-2, 1) // two cells for the selection marker
	with := table{cols: append(append([]column(nil), tb.cols...), column{min: 8, max: 24, prio: 99}), gap: tb.gap}
	rows2 := make([][]string, len(rows))
	for i, r := range rows {
		rows2[i] = append(append([]string(nil), r...), cells[i])
	}
	before, after := tb.fitRows(inner, rows), with.fitRows(inner, rows2)
	if after[len(after)-1] == 0 {
		return tb, false
	}
	for i, c := range tb.cols {
		if c.prio <= 1 && after[i] != before[i] { // the column that is added is dropped first, never these
			return tb, false
		}
	}
	return with, true
}

// canJump reports whether the jump key is active for the row: there is workspace data and the row
// has a placement (or an ambiguity) to show or open.
func (w *wsLayer) canJump(it core.WorkspaceItem, ok bool) bool { return w.knows(it, ok) }

// jumpBinding is the jump key, enabled only when canJump.
func (w *wsLayer) jumpBinding(it core.WorkspaceItem, ok bool) key.Binding {
	b := keyWorkspace
	b.SetEnabled(w.canJump(it, ok))
	return b
}

// runnable reports whether a plan's first step may be handed the terminal. It must be an
// interactive step core produced for this placement: the located workspace tool, "-S" and the
// placement's own machine, nothing else. Anything else is shown for the user to copy.
func runnable(p *core.Placement, r core.OpenRung) bool {
	return r.Interactive && p.Host != "" && p.Match != core.MatchHostNotConfigured &&
		len(r.Argv) == 3 && filepath.Base(r.Argv[0]) == core.RexBin && r.Argv[1] == "-S" && r.Argv[2] == p.Host
}

// jump is an explicit keypress on a row. When the placement is unambiguous and its plan's first
// step is an interactive one (and this is not a demo), that step alone is run through the handoff.
// Every other outcome is a framed notice: ambiguous with the candidates, or the command to copy.
// Nothing here ever tries a second step.
func (w *wsLayer) jump(it core.WorkspaceItem, ok bool) tea.Cmd {
	if !w.knows(it, ok) {
		return nil
	}
	p, cands, more := w.find(it)
	if p == nil {
		w.note = []string{fmt.Sprintf("%d places claim %s, so none was opened. Pick one yourself:", len(cands), it.ID)}
		for _, c := range cands {
			line := "  " + where(&c)
			if c.Host != "" {
				line += " @ " + san(c.Host)
			}
			w.note = append(w.note, line)
			if c.Open != nil && c.Open.Copy != "" {
				w.note = append(w.note, "    copy: "+c.Open.Copy)
			}
		}
		if more {
			w.note = append(w.note, "  and more, not all are listed")
		}
		return nil
	}
	plan := p.Open
	if plan == nil {
		plan = &core.OpenPlan{}
	}
	if !w.demo && w.handoff != nil && len(plan.Rungs) > 0 && runnable(p, plan.Rungs[0]) {
		w.pending = true
		return w.handoff(append([]string(nil), plan.Rungs[0].Argv...))
	}
	var lines []string
	switch {
	case w.demo:
		lines = append(lines, "Demo data: nothing is opened.")
	case len(plan.Rungs) > 0:
		lines = append(lines, "This step cannot be run from here.")
	default:
		lines = append(lines, "Nothing to open from here.")
	}
	if plan.Note != "" && !w.demo { // a demo's own note would only repeat the headline
		lines = append(lines, plan.Note)
	}
	if plan.Copy != "" {
		lines = append(lines, "copy: "+plan.Copy)
	}
	w.note = lines
	return nil
}

// noteCopy sets the notice for a command the user would run themselves (a demo `o`).
func (w *wsLayer) noteCopy(headline string, argv []string) {
	if w != nil {
		w.note = []string{headline, "copy: " + core.ShellJoin(argv)}
	}
}

// clear drops the notice; any key does.
func (w *wsLayer) clear() {
	if w != nil {
		w.note = nil
	}
}

// done reports whether a finished handoff is this layer's, and consumes it.
func (w *wsLayer) done() bool {
	if w == nil || !w.pending {
		return false
	}
	w.pending = false
	return true
}

// noteBox is the framed notice for a body w wide and h tall: wrapped, never cut, and when the body
// is too short to hold it the box says to enlarge the terminal.
func (w *wsLayer) noteBox(th Theme, width, h int) ([]string, bool) {
	if w == nil || len(w.note) == 0 {
		return nil, false
	}
	var body []string
	for _, l := range w.note {
		body = append(body, wrap(san(l), innerW(width))...)
	}
	body = append(body, "", th.Text(KindMuted, "any key closes"))
	return reviewBox(th, "Workspace", body, width, h), true
}
