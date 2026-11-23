package tui

import (
	"errors"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
	"github.com/andrew-bierman/abacus/internal/lambe"
	"github.com/andrew-bierman/abacus/internal/query"
	"github.com/andrew-bierman/abacus/internal/taskview"
)

// searchable is implemented by the sections global search reads. A section hands over the last
// report it holds as documents; it never reads anything for search except searchLoad, the one
// read its tab would make on first visit.
type searchable interface {
	// searchStamp changes whenever searchFeed would return something different, so the overlay
	// rebuilds its index only when a section has new data.
	searchStamp() string
	searchFeed() core.SearchFeed
	// searchLoad is that one lazy read for a section that never loaded (nil when there is none).
	searchLoad() tea.Cmd
}

// jumpResult is how a jump ended.
type jumpResult int

const (
	jumpGone    jumpResult = iota // the item is not in the section any more
	jumpOK                        // selected
	jumpCleared                   // selected after clearing the section's own filter that hid it
)

// jumper is implemented by sections that can select a row by the id their listNav uses.
type jumper interface{ jumpTo(id string) jumpResult }

func stamp(state core.SearchState, readAt time.Time, extra string) string {
	return string(state) + "|" + strconv.FormatInt(readAt.UnixNano(), 10) + "|" + extra
}

// failedFeed is the feed of a domain whose last read failed: not connected when the reader said
// so (with no reason shown: a person fixes that in Doctor, not here), degraded otherwise.
func failedFeed(d core.DomainID, err error, poll time.Duration) core.SearchFeed {
	var nc *core.NotConnectedError
	if errors.As(err, &nc) {
		return core.SearchFeed{Domain: d, State: core.SearchNotConnected}
	}
	return core.SearchFeed{Domain: d, State: core.SearchDegraded, Reason: core.Sanitize(err.Error())}
}

// ---- Tasks

func (s *tasksSection) searchStamp() string {
	switch {
	case s.list.view != nil:
		return stamp(core.SearchSearched, s.list.readAt, strconv.FormatBool(s.list.fallback))
	case !s.loaded:
		return stamp(core.SearchLoading, time.Time{}, "")
	case s.err != nil:
		return stamp(core.SearchDegraded, time.Time{}, s.err.Error())
	}
	return stamp(core.SearchLoading, time.Time{}, "")
}

func (s *tasksSection) searchFeed() core.SearchFeed {
	switch {
	case s.list.view != nil:
		return core.SearchFeed{Domain: core.DomainTasks, ReadAt: s.list.readAt, PollEvery: tasksPollEvery, Docs: s.list.view.SearchDocs()}
	case s.loaded && s.err != nil:
		return failedFeed(core.DomainTasks, s.err, tasksPollEvery)
	}
	return core.SearchFeed{Domain: core.DomainTasks, State: core.SearchLoading}
}

func (s *tasksSection) searchLoad() tea.Cmd { return nil } // Init already reads it

// rowShown is whether the list, with its own preset and query, lists the task.
func (s *tasksSection) rowShown(id string) bool {
	for _, r := range s.list.rep.Rows {
		if r.ID == id {
			return true
		}
	}
	return false
}

func (s *tasksSection) jumpTo(id string) jumpResult {
	l := &s.list
	if l.view == nil {
		return jumpGone
	}
	if _, ok := l.view.Row(id); !ok {
		return jumpGone
	}
	l.cur, l.typing = viewList, false
	res := jumpOK
	if !s.rowShown(id) { // the list's own filter hides it: drop the quick filter first, then the query
		res = jumpCleared
		l.preset, l.presetSet = taskview.PresetAll, true
		s.recompute()
		if !s.rowShown(id) {
			l.text, l.q, l.qerr = "", query.Query{}, nil
			s.recompute()
		}
	}
	s.selected = id
	s.reanchor(s.ids(), true)
	s.drilled, l.detailFocus = false, false
	s.moved()
	return res
}

// ---- Board

func (s *boardSection) searchStamp() string {
	switch {
	case !s.loaded:
		return stamp(core.SearchLoading, time.Time{}, "")
	case s.err != nil:
		return stamp(core.SearchDegraded, time.Time{}, s.err.Error())
	}
	return stamp(core.SearchSearched, s.readAt, "")
}

func (s *boardSection) searchFeed() core.SearchFeed {
	switch {
	case !s.loaded:
		return core.SearchFeed{Domain: core.DomainBoard, State: core.SearchLoading}
	case s.err != nil:
		return failedFeed(core.DomainBoard, s.err, boardPollEvery)
	}
	return core.SearchFeed{Domain: core.DomainBoard, ReadAt: s.readAt, PollEvery: boardPollEvery, Docs: core.BoardSearchDocs(s.report)}
}

// searchLoad is the board's first read, the same call the tab makes on first visit.
func (s *boardSection) searchLoad() tea.Cmd {
	if s.loaded || s.busy {
		return nil
	}
	return s.fetch()
}

func (s *boardSection) jumpTo(id string) jumpResult {
	for i, x := range s.ids() {
		if x == id {
			s.cursor, s.drilled = i, false
			s.remember(s.ids())
			return jumpOK
		}
	}
	return jumpGone
}

// ---- Leases

func (s *leasesSection) searchStamp() string {
	switch {
	case !s.loaded:
		return stamp(core.SearchLoading, time.Time{}, "")
	case s.got.unavailable != "":
		return stamp(core.SearchDegraded, time.Time{}, s.got.unavailable)
	}
	return stamp(core.SearchSearched, s.readAt, "")
}

func (s *leasesSection) searchFeed() core.SearchFeed {
	switch {
	case !s.loaded:
		return core.SearchFeed{Domain: core.DomainLeases, State: core.SearchLoading}
	case s.got.unavailable != "" && s.got.howToConnect != "":
		return core.SearchFeed{Domain: core.DomainLeases, State: core.SearchNotConnected}
	case s.got.unavailable != "":
		return core.SearchFeed{Domain: core.DomainLeases, State: core.SearchDegraded, Reason: s.got.unavailable}
	}
	return core.SearchFeed{Domain: core.DomainLeases, ReadAt: s.readAt, PollEvery: leasesPollEvery, Docs: core.LeasesSearchDocs(s.got.report)}
}

func (s *leasesSection) searchLoad() tea.Cmd { return nil }

func (s *leasesSection) jumpTo(id string) jumpResult {
	for i, x := range s.ids() {
		if x == id {
			s.cursor, s.drilled = i, false
			s.remember(s.ids())
			return jumpOK
		}
	}
	return jumpGone
}

// ---- Queues

func (s *lambeSection) searchStamp() string {
	switch {
	case !s.loaded:
		return stamp(core.SearchLoading, time.Time{}, "")
	case s.err != nil:
		return stamp(core.SearchDegraded, time.Time{}, s.err.Error())
	}
	return stamp(core.SearchSearched, s.readAt, "")
}

func (s *lambeSection) searchFeed() core.SearchFeed {
	switch {
	case !s.loaded:
		return core.SearchFeed{Domain: core.DomainQueues, State: core.SearchLoading}
	case errors.Is(s.err, lambe.ErrNoDaemon):
		return core.SearchFeed{Domain: core.DomainQueues, State: core.SearchNotConnected}
	case s.err != nil:
		return core.SearchFeed{Domain: core.DomainQueues, State: core.SearchDegraded, Reason: core.Sanitize(s.err.Error())}
	}
	return core.SearchFeed{Domain: core.DomainQueues, ReadAt: s.readAt, PollEvery: pollEvery, Docs: core.QueuesSearchDocs(lambe.QueuesReportOf(s.view, s.m.IsDemo()))}
}

func (s *lambeSection) searchLoad() tea.Cmd { return nil }

// jumpTo selects the first run of the issue; an attention or history item selects its run too.
func (s *lambeSection) jumpTo(id string) jumpResult {
	find := func() int {
		for i, r := range s.runs() {
			if r.Issue == id {
				return i
			}
		}
		return -1
	}
	res := jumpOK
	i := find()
	if i < 0 && s.filter != "" { // the section's own filter may hide it
		s.filter, s.typing = "", false
		if i = find(); i >= 0 {
			res = jumpCleared
		}
	}
	if i < 0 {
		return jumpGone
	}
	s.cursor, s.drilled = i, false
	s.remember(s.ids())
	return res
}

// ---- a domain with no provider

func (s *notConnectedSection) searchStamp() string { return "not_connected" }
func (s *notConnectedSection) searchFeed() core.SearchFeed {
	return core.SearchFeed{Domain: s.h.Domain, State: core.SearchNotConnected}
}
func (s *notConnectedSection) searchLoad() tea.Cmd { return nil }

// ---- a section behind a setup step

func (s *guidedSection) searchStamp() string {
	if s.guide != nil {
		return "needs_setup"
	}
	if in, ok := s.inner.(searchable); ok {
		return in.searchStamp()
	}
	return "none"
}

func (s *guidedSection) searchFeed() core.SearchFeed {
	if s.guide != nil { // the domain waits for a setup step: it is not connected yet
		return core.SearchFeed{Domain: s.domain, State: core.SearchNotConnected}
	}
	if in, ok := s.inner.(searchable); ok {
		return in.searchFeed()
	}
	return core.SearchFeed{Domain: s.domain, State: core.SearchNotConnected}
}

func (s *guidedSection) searchLoad() tea.Cmd {
	if in, ok := s.inner.(searchable); ok && s.guide == nil {
		return in.searchLoad()
	}
	return nil
}

func (s *guidedSection) jumpTo(id string) jumpResult {
	if j, ok := s.inner.(jumper); ok && s.guide == nil {
		return j.jumpTo(id)
	}
	return jumpGone
}
