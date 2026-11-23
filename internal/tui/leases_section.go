package tui

import (
	"context"
	"errors"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/andrew-bierman/abacus/internal/core"
)

// leasesPollEvery is deliberately slow: the Leases read shells out to a shared production
// hub, so a 5s poll would be too heavy.
const leasesPollEvery = 30 * time.Second

// leasesTickMsg is the Leases refresh timer. It has its own type because the shell broadcasts
// every message to every section.
type leasesTickMsg struct{}

// leasesRead is one read of the Leases domain: the typed report, or why there is none.
type leasesRead struct {
	report       core.LeasesReport
	unavailable  string // set when there is no report
	howToConnect string // set with unavailable when the fix is something the user can do
}

type leasesMsg struct{ read leasesRead }

// readLeases reads the Leases domain through its provider.
func readLeases(ctx context.Context, p core.LeaseProvider) leasesRead {
	r, err := p.Leases(ctx)
	var nc *core.NotConnectedError
	switch {
	case errors.As(err, &nc):
		return leasesRead{unavailable: core.Sanitize(nc.Reason), howToConnect: core.Sanitize(nc.HowToConnect)}
	case err != nil:
		return leasesRead{unavailable: core.Sanitize(err.Error())}
	}
	return leasesRead{report: r}
}

// leasesSection renders the typed LeasesReport as a table in a framed list panel with the selected
// lease in a detail panel beside it. The lease's own state and tone decide each badge; no line of
// text is parsed.
type leasesSection struct {
	read   func(ctx context.Context) leasesRead
	got    leasesRead
	loaded bool
	tick   func() tea.Cmd // injectable so tests never sleep
	th     Theme
	geom
	listNav          // ids are the lease resources
	ws      *wsLayer // the optional workspace layer; nil without one
}

func (s *leasesSection) setWorkspace(w *wsLayer) { s.ws = w }

// leaseItem is the workspace item of a lease: its resource.
func leaseItem(l core.Lease) (core.WorkspaceItem, bool) {
	return itemOf(core.WorkspaceLease, l.Resource, "")
}

func (s *leasesSection) wsItems() []core.WorkspaceItem {
	var items []core.WorkspaceItem
	for _, l := range s.leases() {
		if it, ok := leaseItem(l); ok {
			items = append(items, it)
		}
	}
	return itemList(items...)
}

// selectedItem is the workspace item under the cursor.
func (s *leasesSection) selectedItem() (core.WorkspaceItem, bool) {
	if ls := s.leases(); s.cursor < len(ls) {
		return leaseItem(ls[s.cursor])
	}
	return core.WorkspaceItem{}, false
}

func newLeasesSection(read func(context.Context) leasesRead) *leasesSection {
	return &leasesSection{read: read, th: DefaultTheme(), tick: func() tea.Cmd {
		return tea.Tick(leasesPollEvery, func(time.Time) tea.Msg { return leasesTickMsg{} })
	}}
}

func (s *leasesSection) SetTheme(t Theme) { s.th = t }
func (s *leasesSection) Capturing() bool  { return false }

func (s *leasesSection) leases() []core.Lease { return s.got.report.Leases }

// ids is one stable id per row: the lease resource.
func (s *leasesSection) ids() []string {
	ls := s.leases()
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Resource
	}
	return out
}

func (s *leasesSection) Bindings() []key.Binding {
	enter, back := s.bindings(s.width(), len(s.leases()))
	it, ok := s.selectedItem()
	return append(moveBindings(), enter, back, s.ws.jumpBinding(it, ok))
}

func (s *leasesSection) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		return leasesMsg{read: s.read(ctx)}
	}
}

func (s *leasesSection) refresh() tea.Cmd { return s.fetch() }

func (s *leasesSection) Init() tea.Cmd { return tea.Batch(s.fetch(), s.tick()) }

func (s *leasesSection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case leasesMsg:
		s.got, s.loaded = m.read, true
		s.reanchor(s.ids(), false) // pin: a lease that vanishes and comes back is selected again
		s.settle(len(s.leases()))
		return s, s.ws.fetch(s.wsItems())
	case workspaceMsg:
		s.ws.receive(m)
	case handoffDoneMsg:
		if s.ws.done() && m.err != nil {
			s.ws.note = []string{"The workspace did not open: " + core.Sanitize(m.err.Error())}
		}
	case leasesTickMsg:
		return s, tea.Batch(s.fetch(), s.tick())
	case tea.WindowSizeMsg:
		s.resize(m)
		s.fit(s.width())
	case tea.KeyMsg:
		s.ws.clear()
		switch {
		case key.Matches(m, keyWorkspace):
			it, ok := s.selectedItem()
			return s, s.ws.jump(it, ok)
		case key.Matches(m, keyUp):
			s.up(s.ids())
		case key.Matches(m, keyDown):
			s.down(s.ids())
		case key.Matches(m, keyEnter):
			s.enter(s.width(), len(s.leases()))
		case key.Matches(m, keyBack):
			s.back()
		}
	}
	return s, nil
}

// leaseCols are the table's columns: state, lease, holder, expiry. The state and the lease id keep
// their whole width (min equals max; fitRows still shrinks them to the content), so an identifying
// field is never clipped to make room for a lower-priority one. A higher prio number drops first.
var leaseCols = table{cols: []column{
	{min: 12, max: 12, prio: 0},
	{min: 24, max: 24, prio: 0},
	{min: 8, prio: 2},
	{min: 6, max: 10, prio: 3, right: true},
}, gap: 2}

// listTitle is the list panel's title: the counts and, for a reduced source, where it came from.
func (s *leasesSection) listTitle() string {
	r := s.got.report
	return titled("Leases · "+san(r.Summarize()), string(r.Source))
}

func (s *leasesSection) listLines(g duoGeom) []string {
	th := s.th
	ls := s.leases()
	if len(ls) == 0 {
		return []string{th.Text(KindMuted, "no leases held")}
	}
	rows := make([][]string, len(ls))
	for i, l := range ls {
		resource := san(l.Resource)
		if i == s.cursor {
			resource = th.Header.Render(resource)
		}
		rows[i] = []string{th.Badge(toneKind(l.Tone()), san(string(l.State))), resource, th.Tint(l.Holder, san(l.Holder)), san(l.Expiry())}
	}
	tb := leaseCols
	if s.ws.has() {
		cells := make([]string, len(ls))
		for i, l := range ls {
			it, ok := leaseItem(l)
			cells[i] = s.ws.place(th, it, ok, i == s.cursor)
		}
		if wtb, ok := s.ws.whereCol(tb, s.width(), g.listInner(), rows, cells); ok {
			tb = wtb
			for i := range rows {
				rows[i] = append(rows[i], cells[i])
			}
		}
	}
	widths := tb.fitRows(max(g.listInner()-2, 1), rows) // two cells for the selection marker
	lines := make([]string, len(rows))
	for i, r := range rows {
		marker := "  "
		if i == s.cursor {
			marker = "▸ "
		}
		lines[i] = marker + tb.row(widths, r)
	}
	return windowLines(th, lines, s.cursor, g.inner, &s.top)
}

func (s *leasesSection) detailLines() []string {
	th := s.th
	ls := s.leases()
	if s.cursor >= len(ls) {
		return []string{th.Text(KindMuted, "select a row")}
	}
	l := ls[s.cursor]
	field := func(label, v string) string { return th.Label.Render(padTo(label, 12)) + orDash(v) }
	holder := "holder"
	if l.State == core.LeaseStale {
		holder = "last holder"
	}
	out := []string{
		th.Header.Render(orDash(l.Resource)) + " " + th.Badge(toneKind(l.Tone()), san(string(l.State))), "",
		field(holder, l.Holder), field("expires", san(l.Expiry())),
	}
	it, ok := leaseItem(l)
	return append(out, s.ws.detail(th, it, ok, 12)...)
}

func (s *leasesSection) View() string {
	th := s.th
	w := s.width()
	switch {
	case !s.loaded:
		return joinLines([]string{th.Text(KindMuted, "loading…")})
	case s.got.unavailable != "" && s.got.howToConnect != "":
		return joinLines(notConnectedBox(th, "Leases", s.got.unavailable, s.got.howToConnect, w, s.h))
	case s.got.unavailable != "":
		return joinLines(box(th, "Leases", unavailableBody(th, s.got.unavailable, innerW(w)), w, s.h))
	}
	if box, ok := s.ws.noteBox(th, w, s.h); ok {
		return joinLines(box)
	}
	g := duoLayout(w, s.h)
	list := s.listLines(g)
	return joinLines(g.draw(th, s.listTitle(), list, "Lease", s.detailLines(), s.drilled))
}
