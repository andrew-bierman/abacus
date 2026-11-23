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
	listNav // ids are the lease resources
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
	return append(moveBindings(), enter, back)
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
	case leasesTickMsg:
		return s, tea.Batch(s.fetch(), s.tick())
	case tea.WindowSizeMsg:
		s.resize(m)
		s.fit(s.width())
	case tea.KeyMsg:
		switch {
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
	widths := leaseCols.fitRows(max(g.listInner()-2, 1), rows) // two cells for the selection marker
	lines := make([]string, len(rows))
	for i, r := range rows {
		marker := "  "
		if i == s.cursor {
			marker = "▸ "
		}
		lines[i] = marker + leaseCols.row(widths, r)
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
	return []string{
		th.Header.Render(orDash(l.Resource)) + " " + th.Badge(toneKind(l.Tone()), san(string(l.State))), "",
		field(holder, l.Holder), field("expires", san(l.Expiry())),
	}
}

func (s *leasesSection) View() string {
	th := s.th
	w := s.width()
	head := th.Header.Render("Leases")
	switch {
	case !s.loaded:
		return joinLines([]string{head, th.Text(KindMuted, "loading…")})
	case s.got.unavailable != "" && s.got.howToConnect != "":
		out := []string{head + " " + th.Badge(KindSkip, "not connected"), ""}
		out = append(out, wrap(th.Text(KindWarn, san(s.got.unavailable)), w)...)
		out = append(out, "")
		return joinLines(append(out, wrap(th.Text(KindAccent, "how to connect: ")+san(s.got.howToConnect), w)...))
	case s.got.unavailable != "":
		return joinLines(append([]string{head}, wrap(th.Text(KindWarn, "state unavailable: "+san(s.got.unavailable)), w)...))
	}
	g := duoLayout(w, s.h)
	list := s.listLines(g)
	return joinLines(g.draw(th, s.listTitle(), list, "Lease", s.detailLines(), s.drilled))
}
