package tui

import (
	"context"
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

var keyHistory = key.NewBinding(key.WithKeys("H"), key.WithHelp("H", "history"))

const (
	histShow  = 12 // events drawn in the detail pane
	histCache = 32 // histories kept for the snapshot on screen
)

// histMsg is the answer to a history read.
type histMsg struct {
	id  string
	key time.Time // the snapshot the cursor task belonged to when the read began
	gen int       // the tracker generation the read was started for
	rep core.TaskHistoryReport
	err error
}

type histEntry struct {
	rep core.TaskHistoryReport
	err error
}

// histState is the history section of the detail pane: off until H, then the history of the cursor
// task, read one task at a time and kept while the snapshot on screen stays the same.
type histState struct {
	src    core.TaskHistorySource
	on     bool
	reveal bool // scroll the detail pane so the History heading is at the top, once
	busy   bool
	key    time.Time // the snapshot the cache belongs to
	cache  map[string]histEntry
}

// toggleHistory shows or hides the history, and brings the detail pane into view.
func (s *tasksSection) toggleHistory() tea.Cmd {
	h := &s.hist
	switch {
	case h.on:
		h.on = false
		return nil
	case h.src == nil:
		s.notice = "history is not available for this tracker"
		return nil
	case len(s.rows()) == 0:
		s.notice = "no task selected"
		return nil
	}
	h.on, h.reveal = true, true
	if isStacked(s.width()) { // as enter does: below the wide layout the detail is its own screen
		s.enter(s.width(), len(s.rows()))
	} else {
		s.list.detailFocus = true
	}
	return nil
}

// histEnsure reads the history of the cursor task when it is shown and not known yet. One read at a
// time, and none for a task already read under the current snapshot.
func (s *tasksSection) histEnsure() tea.Cmd {
	h := &s.hist
	if !h.on || h.src == nil {
		return nil
	}
	if !h.key.Equal(s.list.readAt) { // a new snapshot: what was read before may be out of date
		h.cache, h.key = nil, s.list.readAt
	}
	t, ok := s.current()
	if !ok || h.busy {
		return nil
	}
	if _, have := h.cache[t.ID]; have {
		return nil
	}
	h.busy = true
	src, id, at, gen := h.src, t.ID, h.key, s.gen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		rep, err := src.History(ctx, id, core.HistoryDefaultLimit)
		return histMsg{id: id, key: at, rep: rep, err: err, gen: gen}
	}
}

// histLoaded is whether the history of task id has been read (or failed to be).
func (s *tasksSection) histLoaded(id string) bool { _, ok := s.hist.cache[id]; return ok }

// histReceive stores an answer (dropped when the snapshot moved on while it was being read).
func (s *tasksSection) histReceive(m histMsg) {
	h := &s.hist
	h.busy = false
	if !m.key.Equal(h.key) {
		return
	}
	if h.cache == nil || len(h.cache) >= histCache {
		h.cache = map[string]histEntry{}
	}
	h.cache[m.id] = histEntry{rep: m.rep, err: m.err}
}

// histClear forgets everything read (the tracker was switched).
func (s *tasksSection) histClear() { s.hist.cache, s.hist.busy = nil, false }

// historyLines is the History section of the detail pane for task id, w cells wide: events newest
// first, bounded, every string already sanitized by the parser and sanitized again here.
func (s *tasksSection) historyLines(id string, w int) []string {
	h := &s.hist
	if !h.on {
		return nil
	}
	th := s.th
	out := []string{"", th.Section("History")}
	e, ok := h.cache[id]
	switch {
	case !ok:
		return append(out, th.Text(KindMuted, "  reading…"))
	case e.err != nil:
		return append(out, wrap(th.Text(KindWarn, "  could not read the history: "+san(e.err.Error())), w)...)
	case len(e.rep.Events) == 0:
		return append(out, th.Text(KindMuted, "  no history here"))
	}
	now := nowFn()
	for i, ev := range e.rep.Events {
		if i == histShow {
			more := fmt.Sprintf("  +%d older", len(e.rep.Events)-histShow)
			if e.rep.Truncated {
				more += " (and more in the tracker)"
			}
			out = append(out, th.Text(KindMuted, more))
			break
		}
		age := "-"
		if a := taskview.Age(ev.At, now); a != "" {
			age = a
		}
		text := san(ev.Kind)
		if ev.Actor != "" {
			text += " " + th.Text(KindMuted, san(ev.Actor))
		}
		if ev.Detail != "" {
			text += "  " + san(ev.Detail)
		}
		out = append(out, hang(fmt.Sprintf("  %-4s ", age), text, w)...)
	}
	return out
}
